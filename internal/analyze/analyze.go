// Package analyze turns priced usage events into ranked, non-overlapping
// cost-saving recommendations.
//
// Pipeline:
//
//	events ──price──▶ sessions ──detectors──▶ findings ──rank──▶ Result
//	                     │             ▲
//	                     └──ledger─────┘  (shared, shrinking cost pool)
//
// Each detector asks "how many dollars could this behaviour change save?" and
// must *claim* those dollars from the ledger. A single event's cost can only
// be claimed once (and never entirely), so stacking recommendations cannot
// promise more savings than was actually spent.
package analyze

import (
	"math"
	"sort"
	"time"

	"github.com/rojinebrahimi/budgeteer/internal/pricing"
	"github.com/rojinebrahimi/budgeteer/internal/usage"
)

// Config holds tunables. Zero values are replaced by documented defaults.
type Config struct {
	Table *pricing.Table

	From, To time.Time // analysis window; earlier events only feed the comparison
	Top      int       // sessions/findings to keep (default 5)

	ContextCeiling int64   // prompt size considered bloated (default 120k tokens)
	ContextTarget  int64   // size a compaction is assumed to reach (default 60k)
	MinSessionUSD  float64 // ignore sessions cheaper than this (default $0.25)
	Budget         float64 // optional monthly budget in USD for the run-rate check
	Loc            *time.Location
}

func (c *Config) defaults() {
	if c.Table == nil {
		c.Table = pricing.Default()
	}
	if c.Top <= 0 {
		c.Top = 5
	}
	if c.ContextCeiling <= 0 {
		c.ContextCeiling = 120_000
	}
	if c.ContextTarget <= 0 {
		c.ContextTarget = 60_000
	}
	if c.MinSessionUSD <= 0 {
		c.MinSessionUSD = 0.25
	}
	if c.Loc == nil {
		c.Loc = time.Local
	}
	if c.To.IsZero() {
		c.To = time.Now()
	}
}

// Finding is one actionable recommendation.
type Finding struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Scope      string   `json:"scope"`
	Savings    float64  `json:"savings_usd"`  // ceiling if fully adopted
	Expected   float64  `json:"expected_usd"` // confidence-weighted
	Confidence float64  `json:"confidence"`
	Evidence   []string `json:"evidence"`
	Actions    []string `json:"actions"`
}

// SessionStat is a session summary for the hotspot table.
type SessionStat struct {
	Key        string        `json:"key"`
	Tool       usage.Tool    `json:"tool"`
	Project    string        `json:"project"`
	Model      string        `json:"model"`
	Subagent   bool          `json:"subagent,omitempty"`
	Start      time.Time     `json:"start"`
	Duration   time.Duration `json:"duration_ns"`
	Turns      int           `json:"turns"`
	Cost       float64       `json:"cost_usd"`
	Share      float64       `json:"share"`
	MaxContext int64         `json:"max_context_tokens"`
	Outlier    bool          `json:"outlier"`
}

// Slice is one row of a breakdown table.
type Slice struct {
	Name     string  `json:"name"`
	Cost     float64 `json:"cost_usd"`
	Share    float64 `json:"share"`
	Estimate bool    `json:"estimate,omitempty"`
}

// Day is one calendar day of spend.
type Day struct {
	Date string  `json:"date"`
	Cost float64 `json:"cost_usd"`
}

// Forecast projects the current burn rate.
type Forecast struct {
	DailyEWMA   float64 `json:"daily_ewma_usd"`
	MonthlyRate float64 `json:"monthly_run_rate_usd"`
	Budget      float64 `json:"budget_usd,omitempty"`
	OverBudget  bool    `json:"over_budget,omitempty"`
}

// Totals aggregates raw token counts.
type Totals struct {
	Calls        int     `json:"api_calls"`
	Input        int64   `json:"input_tokens"`
	CacheRead    int64   `json:"cache_read_tokens"`
	CacheWrite   int64   `json:"cache_write_tokens"`
	Output       int64   `json:"output_tokens"`
	Reasoning    int64   `json:"reasoning_tokens"`
	CacheHitRate float64 `json:"cache_hit_rate"`
}

// Result is everything a renderer needs.
type Result struct {
	From      time.Time     `json:"from"`
	To        time.Time     `json:"to"`
	Total     float64       `json:"total_usd"`
	Prev      float64       `json:"previous_period_usd"`
	DeltaPct  *float64      `json:"delta_pct,omitempty"`
	Tokens    Totals        `json:"tokens"`
	Sessions  int           `json:"session_count"`
	ByTool    []Slice       `json:"by_tool"`
	ByModel   []Slice       `json:"by_model"`
	Days      []Day         `json:"days"`
	Top       []SessionStat `json:"top_sessions"`
	Findings  []Finding     `json:"findings"`
	Ceiling   float64       `json:"savings_ceiling_usd"`
	Expected  float64       `json:"savings_expected_usd"`
	Forecast  Forecast      `json:"forecast"`
	TopDecile float64       `json:"top_decile_share"`
	Estimated []string      `json:"estimated_models,omitempty"`
	Unpriced  []string      `json:"unpriced_models,omitempty"`
}

// session is the internal grouping used by detectors.
type session struct {
	key       string
	tool      usage.Tool
	project   string
	sidechain bool
	idx       []int // indices into the event slice, time ordered
	cost      float64
	output    int64
	reasoning int64
	maxCtx    int64
	topModel  string
	start     time.Time
	end       time.Time
	p90Out    float64
}

// env is the shared state handed to every detector.
type env struct {
	cfg      Config
	ev       []usage.Event // current window only, priced
	sessions []*session
	led      *ledger
}

// ledger tracks how much of each event's cost is still unclaimed.
type ledger struct{ left []float64 }

// maxClaim caps the share of any one event's cost that all recommendations
// together may remove: even a perfect habit change never makes a call free.
const maxClaim = 0.9

func newLedger(ev []usage.Event) *ledger {
	l := &ledger{left: make([]float64, len(ev))}
	for i, e := range ev {
		l.left[i] = e.Cost * maxClaim
	}
	return l
}

// claim removes up to want dollars from event i and returns what was granted.
func (l *ledger) claim(i int, want float64) float64 {
	g := math.Max(0, math.Min(want, l.left[i]))
	l.left[i] -= g
	return g
}

type detector func(*env) []Finding

// detectors run in priority order: coarse, high-yield levers claim first so
// finer ones only count what is left.
var detectors = []detector{detectDownshift, detectReasoning, detectContextBloat, detectCacheRebuild}

// Run prices events, groups them into sessions and produces the Result.
func Run(all []usage.Event, cfg Config) *Result {
	cfg.defaults()
	r := &Result{From: cfg.From, To: cfg.To}

	est, unp := map[string]bool{}, map[string]bool{}
	var cur []usage.Event
	for i := range all {
		e := all[i]
		cfg.Table.Apply(&e)
		if e.Estimate {
			est[e.Model] = true
		}
		if e.Unpriced {
			unp[e.Model] = true
		}
		switch {
		case e.Time.Before(cfg.From):
			r.Prev += e.Cost
		case !e.Time.After(cfg.To):
			cur = append(cur, e)
		}
	}
	r.Estimated, r.Unpriced = keys(est), keys(unp)

	e := &env{cfg: cfg, ev: cur, led: newLedger(cur)}
	e.group()
	r.summarise(e)

	for _, d := range detectors {
		r.Findings = append(r.Findings, d(e)...)
	}
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.Expected != b.Expected {
			return a.Expected > b.Expected
		}
		return a.Scope < b.Scope // map iteration is random; keep output stable
	})
	kept := r.Findings[:0]
	for _, f := range r.Findings {
		if f.Expected >= 0.01 {
			kept = append(kept, f)
			r.Ceiling += f.Savings
			r.Expected += f.Expected
		}
	}
	r.Findings = kept
	return r
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// group builds sessions and their per-session statistics.
func (e *env) group() {
	by := map[string]*session{}
	for i, ev := range e.ev {
		k := ev.SessionKey()
		s := by[k]
		if s == nil {
			s = &session{key: k, tool: ev.Tool, project: ev.Project, sidechain: ev.Sidechain, start: ev.Time}
			by[k] = s
			e.sessions = append(e.sessions, s)
		}
		s.idx = append(s.idx, i)
		s.cost += ev.Cost
		s.output += ev.Output
		s.reasoning += ev.Reasoning
		s.maxCtx = max(s.maxCtx, ev.Context())
		s.end = ev.Time
	}
	for _, s := range e.sessions {
		outs := make([]float64, len(s.idx))
		perModel := map[string]float64{}
		for n, i := range s.idx {
			outs[n] = float64(e.ev[i].Output)
			perModel[e.ev[i].Model] += e.ev[i].Cost
		}
		s.p90Out = quantile(outs, 0.9)
		best := -1.0
		for m, c := range perModel {
			if c > best || (c == best && m < s.topModel) {
				best, s.topModel = c, m
			}
		}
	}
}

// summarise fills the descriptive (non-advisory) parts of the Result.
func (r *Result) summarise(e *env) {
	tool, model := map[string]float64{}, map[string]float64{}
	modelEst := map[string]bool{}
	day := map[string]float64{}
	var read, all int64
	for _, ev := range e.ev {
		r.Total += ev.Cost
		tool[string(ev.Tool)] += ev.Cost
		model[ev.Model] += ev.Cost
		modelEst[ev.Model] = modelEst[ev.Model] || ev.Estimate
		day[ev.Time.In(e.cfg.Loc).Format("2006-01-02")] += ev.Cost
		r.Tokens.Calls++
		r.Tokens.Input += ev.Input
		r.Tokens.CacheRead += ev.CacheRead
		r.Tokens.CacheWrite += ev.CacheWrite()
		r.Tokens.Output += ev.Output
		r.Tokens.Reasoning += ev.Reasoning
		read += ev.CacheRead
		all += ev.Context()
	}
	if all > 0 {
		r.Tokens.CacheHitRate = float64(read) / float64(all)
	}
	r.Sessions = len(e.sessions)
	if r.Prev > 0 {
		d := (r.Total - r.Prev) / r.Prev * 100
		r.DeltaPct = &d
	}
	r.ByTool = slices(tool, nil, r.Total)
	r.ByModel = slices(model, modelEst, r.Total)

	// Daily series with explicit zero days so the EWMA sees idle time.
	var series []float64
	for d := dayStart(e.cfg.From, e.cfg.Loc); !d.After(e.cfg.To); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		r.Days = append(r.Days, Day{Date: k, Cost: day[k]})
		series = append(series, day[k])
	}
	r.Forecast.DailyEWMA = ewma(series, 0.3)
	r.Forecast.MonthlyRate = r.Forecast.DailyEWMA * 30.4
	if e.cfg.Budget > 0 {
		r.Forecast.Budget = e.cfg.Budget
		r.Forecast.OverBudget = r.Forecast.MonthlyRate > e.cfg.Budget
	}

	// Hotspots: rank by cost, flag log-cost outliers with the modified z-score.
	ss := append([]*session(nil), e.sessions...)
	sort.Slice(ss, func(i, j int) bool { return ss[i].cost > ss[j].cost })
	logs := make([]float64, len(ss))
	for i, s := range ss {
		logs[i] = math.Log(math.Max(s.cost, 1e-6))
	}
	z := modifiedZ(logs)
	var top10 float64
	cut := int(math.Ceil(float64(len(ss)) / 10))
	for i, s := range ss {
		if i < cut {
			top10 += s.cost
		}
		if len(r.Top) < e.cfg.Top {
			share := 0.0
			if r.Total > 0 {
				share = s.cost / r.Total
			}
			r.Top = append(r.Top, SessionStat{
				Key: s.key, Tool: s.tool, Project: s.project, Model: s.topModel, Subagent: s.sidechain,
				Start: s.start, Duration: s.end.Sub(s.start), Turns: len(s.idx), Cost: s.cost,
				Share: share, MaxContext: s.maxCtx, Outlier: z[i] > 3.5 && len(ss) >= 8,
			})
		}
	}
	if r.Total > 0 {
		r.TopDecile = top10 / r.Total
	}
}

func slices(m map[string]float64, est map[string]bool, total float64) []Slice {
	out := make([]Slice, 0, len(m))
	for k, v := range m {
		s := Slice{Name: k, Cost: v, Estimate: est[k]}
		if total > 0 {
			s.Share = v / total
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func dayStart(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}
