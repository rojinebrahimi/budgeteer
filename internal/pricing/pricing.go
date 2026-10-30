// Package pricing turns token counts into dollars.
//
// Model names in agent logs are messy (dated suffixes, "[1m]" context tags,
// brand-new releases the table has never seen). Resolution therefore runs a
// three-step cascade and records how confident the match was, so downstream
// reports can say "estimated" instead of presenting a guess as fact.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/rojinebrahimi/budgeteer/internal/usage"
)

//go:embed prices.json
var defaultPrices []byte

// Rate is USD per one million tokens.
type Rate struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite5m float64 `json:"cache_write_5m"`
	CacheWrite1h float64 `json:"cache_write_1h"`
}

// Model is one priced model plus its position on the cost ladder.
type Model struct {
	ID      string `json:"id"`
	Tier    int    `json:"tier"`
	Cheaper string `json:"cheaper,omitempty"`
	Rate
}

// family is a regex fallback for model names the table has never seen. Rules
// are anchored (^claude-…, ^gpt-…) so that an unrelated vendor's model whose
// name merely contains "mini" or "opus" is never priced as someone else's.
type family struct {
	Match string `json:"match"`
	Use   string `json:"use"`
	re    *regexp.Regexp
}

type file struct {
	Models   []Model  `json:"models"`
	Families []family `json:"families"`
}

// Match describes how a model name was resolved.
type Match int

const (
	None   Match = iota // unknown model, cannot be priced
	Exact               // id or id-with-date-suffix matched
	Prefix              // longest-prefix match (e.g. a point release)
	Family              // fell back to a family default: an estimate
)

// Table is an immutable-after-load price list.
type Table struct {
	models   map[string]Model
	byLength []string // ids, longest first, for prefix matching
	families []family
}

var (
	dateSuffix = regexp.MustCompile(`-(\d{8}|\d{4}-\d{2}-\d{2})$`)
	ctxTag     = regexp.MustCompile(`\[[^\]]*\]$`)

	// Bedrock/Vertex style: "us.anthropic.claude-…", "anthropic.claude-…".
	providerPrefix = regexp.MustCompile(`^([a-z]{2}\.)?anthropic\.`)
)

// Default returns the embedded price table.
func Default() *Table {
	t, err := parse(defaultPrices)
	if err != nil {
		panic("pricing: embedded prices.json is invalid: " + err.Error())
	}
	return t
}

// Load returns the default table overlaid with the models in path. Entries
// with an id already present replace the built-in; new ids are added.
func Load(path string) (*Table, error) {
	t := Default()
	if path == "" {
		return t, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pricing file: %w", err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse pricing file %s: %w", path, err)
	}
	for _, m := range f.Models {
		t.models[strings.ToLower(m.ID)] = m
	}
	t.families = append(f.Families, t.families...) // user families win
	t.reindex()
	return t, nil
}

func parse(raw []byte) (*Table, error) {
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	t := &Table{models: map[string]Model{}, families: f.Families}
	for _, m := range f.Models {
		t.models[strings.ToLower(m.ID)] = m
	}
	t.reindex()
	return t, nil
}

func (t *Table) reindex() {
	for i := range t.families {
		t.families[i].re = regexp.MustCompile(t.families[i].Match)
	}
	t.byLength = t.byLength[:0]
	for id := range t.models {
		t.byLength = append(t.byLength, id)
	}
	sort.Slice(t.byLength, func(i, j int) bool {
		if len(t.byLength[i]) != len(t.byLength[j]) {
			return len(t.byLength[i]) > len(t.byLength[j])
		}
		return t.byLength[i] < t.byLength[j]
	})
}

// Models returns every priced model, frontier tier first.
func (t *Table) Models() []Model {
	out := make([]Model, 0, len(t.models))
	for _, m := range t.models {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tier != out[j].Tier {
			return out[i].Tier > out[j].Tier
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func normalize(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = ctxTag.ReplaceAllString(n, "")
	n = dateSuffix.ReplaceAllString(n, "")
	// Provider-qualified names ("anthropic/claude-…", "us.anthropic.claude-…").
	if i := strings.LastIndex(n, "/"); i >= 0 {
		n = n[i+1:]
	}
	return providerPrefix.ReplaceAllString(n, "")
}

// Lookup resolves a model name. The returned Model is the one whose rates
// apply; Match says how trustworthy the resolution is.
func (t *Table) Lookup(name string) (Model, Match) {
	n := normalize(name)
	if n == "" || n == "<synthetic>" {
		return Model{}, None
	}
	if m, ok := t.models[n]; ok {
		return m, Exact
	}
	for _, id := range t.byLength {
		if strings.HasPrefix(n, id) {
			return t.models[id], Prefix
		}
	}
	for _, f := range t.families {
		if f.re.MatchString(n) {
			if m, ok := t.models[strings.ToLower(f.Use)]; ok {
				return m, Family
			}
		}
	}
	return Model{}, None
}

// Cheaper returns the next model down the ladder, if any.
func (t *Table) Cheaper(m Model) (Model, bool) {
	if m.Cheaper == "" {
		return Model{}, false
	}
	c, ok := t.models[strings.ToLower(m.Cheaper)]
	return c, ok
}

// Cost prices an event under a specific rate card. Providers without a
// separate cache-write price (OpenAI) fall back to the input rate.
func (r Rate) Cost(e usage.Event) float64 {
	w5, w1 := r.CacheWrite5m, r.CacheWrite1h
	if w5 == 0 {
		w5 = r.Input
	}
	if w1 == 0 {
		w1 = w5
	}
	cr := r.CacheRead
	if cr == 0 {
		cr = r.Input
	}
	return (float64(e.Input)*r.Input +
		float64(e.CacheRead)*cr +
		float64(e.CacheWrite5m)*w5 +
		float64(e.CacheWrite1h)*w1 +
		float64(e.Output)*r.Output) / 1e6
}

// InputSide prices only the prompt half of an event, i.e. everything except
// output tokens. Used to size the cost of carrying a large context.
func (r Rate) InputSide(e usage.Event) float64 {
	e.Output = 0
	return r.Cost(e)
}

// Apply prices e in place and returns the resolved model.
func (t *Table) Apply(e *usage.Event) Model {
	m, how := t.Lookup(e.Model)
	switch how {
	case None:
		e.Unpriced, e.Cost = true, 0
	case Family:
		e.Estimate = true
		fallthrough
	default:
		e.Cost = m.Rate.Cost(*e)
	}
	return m
}
