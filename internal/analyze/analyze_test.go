package analyze

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/rojinebrahimi/budgeteer/internal/usage"
)

func TestModifiedZFlagsOutlier(t *testing.T) {
	xs := []float64{1, 1.1, 0.9, 1.05, 0.95, 1, 1.02, 0.98, 25}
	z := modifiedZ(xs)
	if z[len(z)-1] < 3.5 {
		t.Errorf("outlier z = %.2f, want > 3.5", z[len(z)-1])
	}
	for _, v := range z[:len(z)-1] {
		if math.Abs(v) > 3.5 {
			t.Errorf("inlier flagged: %.2f", v)
		}
	}
	for _, v := range modifiedZ([]float64{2, 2, 2, 2}) { // MAD == 0 must not NaN
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Error("constant input produced NaN/Inf")
		}
	}
}

func TestPercentileRanksHandleTies(t *testing.T) {
	r := percentileRanks([]float64{10, 20, 20, 30})
	if r[1] != r[2] {
		t.Errorf("tied values must share a rank: %v", r)
	}
	if !(r[0] < r[1] && r[2] < r[3]) {
		t.Errorf("ranks not monotone: %v", r)
	}
}

func TestEWMAWeightsRecentDays(t *testing.T) {
	spikeThenQuiet := ewma([]float64{100, 0, 0, 0, 0, 0, 0}, 0.3)
	quietThenSpike := ewma([]float64{0, 0, 0, 0, 0, 0, 100}, 0.3)
	if !(quietThenSpike > spikeThenQuiet) {
		t.Errorf("recent spike (%.1f) should outweigh old spike (%.1f)", quietThenSpike, spikeThenQuiet)
	}
}

func TestLedgerNeverOverClaims(t *testing.T) {
	l := newLedger([]usage.Event{{Cost: 10}})
	a := l.claim(0, 8)
	b := l.claim(0, 8)
	if a+b > 10*maxClaim+1e-9 {
		t.Errorf("claimed %.2f of a $10 event; cap is %.2f", a+b, 10*maxClaim)
	}
	if l.claim(0, -5) != 0 {
		t.Error("negative claim must grant nothing")
	}
}

// synthetic builds a plausible workload: many light opus sessions, a few huge
// ones, and some subagent traffic.
func synthetic(seed int64) []usage.Event {
	rng := rand.New(rand.NewSource(seed))
	base := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	var ev []usage.Event
	for s := 0; s < 40; s++ {
		heavy := s%8 == 0
		turns := 8 + rng.Intn(40)
		if heavy {
			turns = 300
		}
		t0 := base.Add(time.Duration(s) * 5 * time.Hour)
		ctx := int64(20_000)
		for i := 0; i < turns; i++ {
			ctx += int64(rng.Intn(2500))
			out := int64(80 + rng.Intn(300))
			if heavy {
				out = int64(500 + rng.Intn(3000))
			}
			gap := time.Duration(20+rng.Intn(60)) * time.Second
			if i%47 == 46 {
				gap = 2 * time.Hour
			}
			t0 = t0.Add(gap)
			e := usage.Event{
				Tool: usage.Claude, SessionID: fmt.Sprint("s", s), Project: "/w/p", Model: "claude-opus-4-5",
				Time: t0, Sidechain: s%5 == 0, Effort: "high",
				Input: 3, CacheRead: ctx, CacheWrite1h: int64(rng.Intn(3000)), Output: out, Reasoning: out / 2,
			}
			if gap >= time.Hour { // cache expired: the whole context is written again
				e.CacheRead, e.CacheWrite1h = 0, ctx
			}
			ev = append(ev, e)
		}
	}
	return ev
}

// Property: however many detectors fire, claimed savings can never exceed what
// was actually spent, and expected <= ceiling.
func TestSavingsNeverExceedSpend(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		ev := synthetic(seed)
		r := Run(ev, Config{From: ev[0].Time.Add(-time.Hour), To: ev[len(ev)-1].Time.Add(time.Hour), Loc: time.UTC})
		if r.Total <= 0 {
			t.Fatalf("seed %d: no spend", seed)
		}
		if r.Ceiling > r.Total*maxClaim+1e-6 {
			t.Errorf("seed %d: ceiling %.2f > %.0f%% of spend %.2f", seed, r.Ceiling, maxClaim*100, r.Total)
		}
		if r.Expected > r.Ceiling+1e-9 {
			t.Errorf("seed %d: expected %.2f > ceiling %.2f", seed, r.Expected, r.Ceiling)
		}
		for _, f := range r.Findings {
			if f.Confidence < 0 || f.Confidence > 1 || f.Savings < f.Expected-1e-9 {
				t.Errorf("seed %d: bad finding %+v", seed, f)
			}
		}
	}
}

func TestRunIsDeterministic(t *testing.T) {
	ev := synthetic(7)
	cfg := Config{From: ev[0].Time.Add(-time.Hour), To: ev[len(ev)-1].Time.Add(time.Hour), Loc: time.UTC}
	a, b := Run(ev, cfg), Run(ev, cfg)
	if fmt.Sprint(a.Findings) != fmt.Sprint(b.Findings) || a.Expected != b.Expected {
		t.Fatal("two runs over identical input differ")
	}
}

func TestDetectorsFireOnObviousWaste(t *testing.T) {
	ev := synthetic(3)
	r := Run(ev, Config{From: ev[0].Time.Add(-time.Hour), To: ev[len(ev)-1].Time.Add(time.Hour), Loc: time.UTC})
	got := map[string]bool{}
	for _, f := range r.Findings {
		got[f.ID] = true
	}
	for _, id := range []string{"model-downshift", "context-bloat", "reasoning-effort", "cache-rebuild"} {
		if !got[id] {
			t.Errorf("expected detector %q to fire on a wasteful workload; got %v", id, got)
		}
	}
}

func TestPreviousPeriodComparison(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	mk := func(at time.Time, out int64) usage.Event {
		return usage.Event{Tool: usage.Claude, SessionID: "s", Model: "claude-sonnet-4-5", Time: at, Output: out}
	}
	ev := []usage.Event{mk(now.Add(-10*24*time.Hour), 1_000_000), mk(now.Add(-2*24*time.Hour), 2_000_000)}
	r := Run(ev, Config{From: now.Add(-7 * 24 * time.Hour), To: now, Loc: time.UTC})
	if r.DeltaPct == nil || math.Abs(*r.DeltaPct-100) > 1e-6 {
		t.Fatalf("delta = %v, want +100%%", r.DeltaPct)
	}
}

func TestBudgetRunRate(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	ev := []usage.Event{{Tool: usage.Claude, SessionID: "s", Model: "claude-sonnet-4-5", Time: now.Add(-time.Hour), Output: 10_000_000}}
	r := Run(ev, Config{From: now.Add(-24 * time.Hour), To: now, Loc: time.UTC, Budget: 1})
	if !r.Forecast.OverBudget {
		t.Errorf("a $150 day against a $1 budget must be over budget: %+v", r.Forecast)
	}
}
