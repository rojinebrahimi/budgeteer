package pricing

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/rojinebrahimi/budgeteer/internal/usage"
)

func TestLookupCascade(t *testing.T) {
	tb := Default()
	cases := []struct {
		name  string
		want  string
		match Match
	}{
		{"claude-opus-4-5", "claude-opus-4-5", Exact},
		{"claude-opus-4-5-20251101", "claude-opus-4-5", Exact},                // dated suffix stripped
		{"claude-sonnet-4-5[1m]", "claude-sonnet-4-5", Exact},                 // context tag stripped
		{"us.anthropic.claude-haiku-4-5-20251001", "claude-haiku-4-5", Exact}, // Bedrock prefix
		{"anthropic/claude-sonnet-4-5", "claude-sonnet-4-5", Exact},
		{"gpt-5.1-codex-max", "gpt-5", Prefix}, // must not be mangled by the '.'
		{"claude-sonnet-9-9", "claude-sonnet-4-5", Family},
		{"gpt-9-mini", "gpt-5-mini", Family},
	}
	for _, c := range cases {
		m, how := tb.Lookup(c.name)
		if m.ID != c.want || how != c.match {
			t.Errorf("Lookup(%q) = %q/%v, want %q/%v", c.name, m.ID, how, c.want, c.match)
		}
	}
}

// Regression: "MiniMax-M3" was once priced as gpt-5-mini by a substring rule.
func TestForeignModelsAreNotMispriced(t *testing.T) {
	tb := Default()
	for _, n := range []string{"MiniMax-M3", "kimi-k3", "glm-5.3", "deepseek-v4-pro", "<synthetic>", ""} {
		if _, how := tb.Lookup(n); how != None {
			t.Errorf("Lookup(%q) matched (%v); want None", n, how)
		}
	}
}

func TestCostArithmetic(t *testing.T) {
	r := Rate{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite5m: 3.75, CacheWrite1h: 6}
	e := usage.Event{Input: 1_000_000, CacheRead: 1_000_000, CacheWrite5m: 1_000_000, CacheWrite1h: 1_000_000, Output: 1_000_000}
	if got, want := r.Cost(e), 3+0.3+3.75+6+15.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("Cost = %v, want %v", got, want)
	}
	// OpenAI-style card with no cache-write price falls back to the input rate.
	o := Rate{Input: 1.25, Output: 10, CacheRead: 0.125}
	if got, want := o.Cost(usage.Event{CacheWrite5m: 1_000_000}), 1.25; math.Abs(got-want) > 1e-9 {
		t.Fatalf("fallback write price = %v, want %v", got, want)
	}
}

func TestApplyFlagsEstimatesAndUnpriced(t *testing.T) {
	tb := Default()
	est := usage.Event{Model: "claude-opus-9", Output: 1_000_000}
	tb.Apply(&est)
	if !est.Estimate || est.Cost == 0 {
		t.Errorf("family fallback should price and flag Estimate: %+v", est)
	}
	unk := usage.Event{Model: "kimi-k3", Output: 1_000_000}
	tb.Apply(&unk)
	if !unk.Unpriced || unk.Cost != 0 {
		t.Errorf("unknown model should be Unpriced with zero cost: %+v", unk)
	}
}

func TestLoadOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.json")
	_ = os.WriteFile(p, []byte(`{"models":[{"id":"kimi-k3","tier":2,"input":1,"output":2}]}`), 0o644)
	tb, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if m, how := tb.Lookup("kimi-k3"); how != Exact || m.Output != 2 {
		t.Errorf("override not applied: %v %v", m, how)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("expected error for a missing file")
	}
}
