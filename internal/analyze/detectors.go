package analyze

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rojinebrahimi/budgeteer/internal/pricing"
	"github.com/rojinebrahimi/budgeteer/internal/usage"
)

// Tunables for the heuristics. They are deliberately conservative: each one
// shrinks a theoretical saving toward what a person can realistically bank.
const (
	lightCutoff       = 0.40 // sessions below this demand percentile are downshift candidates
	tokenInflation    = 1.15 // a cheaper model may need ~15% more tokens for the same job
	compactEfficiency = 0.60 // a compaction keeps ~40% of the bloat's cost anyway
	rebuildAvoidable  = 0.50 // half of cache rebuilds are unavoidable (real breaks)
	highEffortCut     = 0.35 // share of reasoning cost trimmed by dropping one effort level
	idleReasonCut     = 0.25
	minCacheRebuild   = 20_000 // ignore tiny rewrites
)

func (e *env) model(name string) (pricing.Model, bool) {
	m, how := e.cfg.Table.Lookup(name)
	return m, how != pricing.None
}

func projectName(p string) string {
	if p == "" {
		return "(unknown)"
	}
	return filepath.Base(p)
}

func usd(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("$%.0f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

func alias(id string) string {
	for _, a := range []string{"opus", "sonnet", "haiku"} {
		if strings.Contains(id, a) {
			return a
		}
	}
	return id
}

type example struct {
	name  string
	saved float64
	turns int
}

func topExamples(ex []example, n int) string {
	sort.Slice(ex, func(i, j int) bool { return ex[i].saved > ex[j].saved })
	var parts []string
	for i, x := range ex {
		if i == n {
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%d calls, −%s)", x.name, x.turns, usd(x.saved)))
	}
	return strings.Join(parts, "; ")
}

// ---------------------------------------------------------------- downshift

// detectDownshift finds sessions that ran on a pricier model than their
// workload needed.
//
// "Needed" is judged relative to the user's own history: every session is
// scored on p90 output length, reasoning share and number of calls, each as a
// percentile rank among that tool's sessions, then blended 50/30/20. The
// lightest 40% become candidates, with confidence rising the lighter they
// are. Subagent traffic gets a small bonus: it is mostly search and summary
// work. The counterfactual re-prices the identical tokens on the next
// cheaper model plus a 15% allowance for it being less token-efficient.
func detectDownshift(e *env) []Finding {
	byTool := map[usage.Tool][]*session{}
	for _, s := range e.sessions {
		byTool[s.tool] = append(byTool[s.tool], s)
	}

	type group struct {
		tool            usage.Tool
		model           pricing.Model
		cheaper         pricing.Model
		names           map[string]bool // model names actually seen, not the rate card they resolved to
		saved, expected float64
		sessions, subs  int
		calls           int
		ex              []example
	}
	groups := map[string]*group{}

	for tool, ss := range byTool {
		if len(ss) < 5 { // percentile ranks are meaningless on tiny samples
			continue
		}
		out := make([]float64, len(ss))
		rsn := make([]float64, len(ss))
		turns := make([]float64, len(ss))
		for i, s := range ss {
			out[i] = s.p90Out
			if s.output > 0 {
				rsn[i] = float64(s.reasoning) / float64(s.output)
			}
			turns[i] = float64(len(s.idx))
		}
		rOut, rRsn, rTurn := percentileRanks(out), percentileRanks(rsn), percentileRanks(turns)

		for i, s := range ss {
			if s.cost < e.cfg.MinSessionUSD {
				continue
			}
			demand := 0.5*rOut[i] + 0.3*rRsn[i] + 0.2*rTurn[i]
			if demand >= lightCutoff {
				continue
			}
			conf := 0.25 + 0.5*(lightCutoff-demand)/lightCutoff
			if s.sidechain {
				conf += 0.10
			}
			conf = math.Min(conf, 0.8)

			var sessSaved float64
			for _, ix := range s.idx {
				ev := e.ev[ix]
				m, ok := e.model(ev.Model)
				if !ok {
					continue
				}
				cheaper, ok := e.cfg.Table.Cheaper(m)
				if !ok {
					continue
				}
				want := ev.Cost - cheaper.Rate.Cost(ev)*tokenInflation
				if want <= 0 {
					continue
				}
				got := e.led.claim(ix, want)
				if got == 0 {
					continue
				}
				k := string(tool) + "|" + m.ID
				g := groups[k]
				if g == nil {
					g = &group{tool: tool, model: m, cheaper: cheaper, names: map[string]bool{}}
					groups[k] = g
				}
				g.saved += got
				g.expected += got * conf
				g.calls++
				g.names[ev.Model] = true
				sessSaved += got
			}
			if sessSaved > 0 {
				k := ""
				for _, ix := range s.idx {
					if m, ok := e.model(e.ev[ix].Model); ok {
						k = string(tool) + "|" + m.ID
						break
					}
				}
				g := groups[k]
				g.sessions++
				if s.sidechain {
					g.subs++
				}
				g.ex = append(g.ex, example{projectName(s.project), sessSaved, len(s.idx)})
			}
		}
	}

	var out []Finding
	for _, g := range groups {
		if g.saved <= 0 {
			continue
		}
		cheap := alias(g.cheaper.ID)
		ran := strings.Join(keys(g.names), ", ")
		f := Finding{
			ID:       "model-downshift",
			Title:    fmt.Sprintf("Use %s instead of %s for light work", cheap, alias(g.model.ID)),
			Scope:    fmt.Sprintf("%s · %s · %d sessions", g.tool, ran, g.sessions),
			Savings:  g.saved,
			Expected: g.expected,
			Evidence: []string{
				fmt.Sprintf("%d sessions on %s fall in the lightest 40%% of your own work (short outputs, little reasoning).", g.sessions, ran),
				fmt.Sprintf("Re-pricing the same tokens at %s rates (+15%% token allowance) saves up to %s.", g.cheaper.ID, usd(g.saved)),
				"Largest: " + topExamples(g.ex, 3),
			},
		}
		f.Confidence = f.Expected / f.Savings
		switch g.tool {
		case usage.Claude:
			f.Actions = append(f.Actions,
				fmt.Sprintf("Switch with `/model %s` for exploration, docs, renames and small edits; switch back for design and hard debugging.", cheap))
			if strings.Contains(g.model.ID, "opus") {
				f.Actions = append(f.Actions, "Or start with `claude --model opusplan`: the frontier model plans, Sonnet executes.")
			}
			if g.subs > 0 {
				f.Actions = append(f.Actions,
					fmt.Sprintf("%d of these were subagents: `export CLAUDE_CODE_SUBAGENT_MODEL=%s` runs them on the cheaper model.", g.subs, cheap))
			}
		case usage.Codex:
			f.Actions = append(f.Actions,
				fmt.Sprintf("Run light tasks with `codex -m %s`, or add a profile in `~/.codex/config.toml`.", g.cheaper.ID))
		}
		out = append(out, f)
	}
	return out
}

// ---------------------------------------------------------------- reasoning

// detectReasoning prices the reasoning/thinking tokens (billed as output) and
// estimates what one effort level lower would remove. High-effort sessions
// are trimmed by 35%; sessions with no recorded effort but a reasoning-heavy
// profile (>50% of output) by 25% at lower confidence.
func detectReasoning(e *env) []Finding {
	type agg struct {
		cost, saved, expected float64
		high, calls           int
	}
	by := map[usage.Tool]*agg{}
	for _, s := range e.sessions {
		if s.output == 0 {
			continue
		}
		share := float64(s.reasoning) / float64(s.output)
		for _, ix := range s.idx {
			ev := e.ev[ix]
			if ev.Reasoning == 0 {
				continue
			}
			m, ok := e.model(ev.Model)
			if !ok {
				continue
			}
			level := strings.ToLower(ev.Effort)
			high := level == "high" || level == "xhigh" || level == "max"
			var cut, conf float64
			switch {
			case high:
				cut, conf = highEffortCut, 0.55
			case level == "" && share >= 0.5:
				cut, conf = idleReasonCut, 0.35
			default:
				continue
			}
			rc := float64(ev.Reasoning) * m.Rate.Output / 1e6
			got := e.led.claim(ix, rc*cut)
			if got == 0 {
				continue
			}
			a := by[ev.Tool]
			if a == nil {
				a = &agg{}
				by[ev.Tool] = a
			}
			a.cost += rc
			a.saved += got
			a.expected += got * conf
			a.calls++
			if high {
				a.high++
			}
		}
	}
	var out []Finding
	for tool, a := range by {
		f := Finding{
			ID:       "reasoning-effort",
			Title:    "Lower reasoning effort for routine work",
			Scope:    fmt.Sprintf("%s · %d calls", tool, a.calls),
			Savings:  a.saved,
			Expected: a.expected,
			Evidence: []string{
				fmt.Sprintf("Reasoning tokens cost %s across %d calls (%d at high effort); they are billed at the output rate.", usd(a.cost), a.calls, a.high),
				"Dropping one effort level typically removes 25–35% of reasoning tokens with little quality loss on routine edits.",
			},
		}
		f.Confidence = f.Expected / f.Savings
		if tool == usage.Claude {
			f.Actions = []string{"Pick a lower effort in `/model` (or start with `claude --effort medium`) for refactors, tests and mechanical edits; reserve high effort for architecture and tricky bugs."}
		} else {
			f.Actions = []string{"Set `model_reasoning_effort = \"medium\"` in `~/.codex/config.toml`, or pass `-c model_reasoning_effort=medium` for routine tasks."}
		}
		out = append(out, f)
	}
	return out
}

// ------------------------------------------------------------------ bloat

// detectContextBloat prices the cost of dragging a very large context through
// every turn. Each call re-reads the whole prompt, so a long session's cost
// grows roughly quadratically with its length. For calls whose prompt exceeds
// the ceiling (default 120k) it takes the input-side cost of the tokens above
// the post-compaction target (60k), scaled by 0.6 because a compaction is
// lossy and the summary itself still has to be read.
func detectContextBloat(e *env) []Finding {
	type agg struct {
		saved, excess float64
		calls         int
		sessions      map[string]bool
		peak          int64
		ex            map[string]*example
	}
	by := map[usage.Tool]*agg{}
	for _, s := range e.sessions {
		for _, ix := range s.idx {
			ev := e.ev[ix]
			ctx := ev.Context()
			if ctx <= e.cfg.ContextCeiling {
				continue
			}
			m, ok := e.model(ev.Model)
			if !ok {
				continue
			}
			frac := float64(ctx-e.cfg.ContextTarget) / float64(ctx)
			bloat := m.Rate.InputSide(ev) * frac
			got := e.led.claim(ix, bloat*compactEfficiency)
			if got == 0 {
				continue
			}
			a := by[ev.Tool]
			if a == nil {
				a = &agg{sessions: map[string]bool{}, ex: map[string]*example{}}
				by[ev.Tool] = a
			}
			a.saved += got
			a.excess += bloat
			a.calls++
			a.sessions[s.key] = true
			a.peak = max(a.peak, ctx)
			x := a.ex[s.key]
			if x == nil {
				x = &example{name: projectName(s.project)}
				a.ex[s.key] = x
			}
			x.saved += got
			x.turns++
		}
	}
	var out []Finding
	for tool, a := range by {
		var ex []example
		for _, x := range a.ex {
			ex = append(ex, *x)
		}
		f := Finding{
			ID:       "context-bloat",
			Title:    "Compact or restart before the context balloons",
			Scope:    fmt.Sprintf("%s · %d sessions", tool, len(a.sessions)),
			Savings:  a.saved,
			Expected: a.saved * 0.55,
			Evidence: []string{
				fmt.Sprintf("%d calls carried over %dk tokens of context (peak %dk); every call re-reads all of it.", a.calls, e.cfg.ContextCeiling/1000, a.peak/1000),
				fmt.Sprintf("Reading the portion above %dk cost about %s.", e.cfg.ContextTarget/1000, usd(a.excess)),
				"Worst: " + topExamples(ex, 3),
			},
			Actions: []string{
				"Run `/compact` at a natural checkpoint (feature done, tests green) instead of letting context run to the limit.",
				"Use `/clear` (or a fresh session) when switching tasks; unrelated history is pure overhead.",
			},
		}
		f.Confidence = 0.55
		out = append(out, f)
	}
	return out
}

// ----------------------------------------------------------------- rebuild

// detectCacheRebuild finds idle gaps that outlived the prompt cache. Anthropic
// caches live 5 minutes (or 1 hour when the long TTL is used); resume after
// that and the entire context is written again at the cache-write price
// instead of read at roughly a tenth of the input price. The wasted amount is
// (write price − read price) on the re-written tokens; half is assumed
// avoidable because some breaks are real.
func detectCacheRebuild(e *env) []Finding {
	type agg struct {
		saved, gap float64
		n          int
		ex         []example
	}
	by := map[usage.Tool]*agg{}
	for _, s := range e.sessions {
		var sessSaved float64
		var rebuilds int
		for n := 1; n < len(s.idx); n++ {
			prev, ev := e.ev[s.idx[n-1]], e.ev[s.idx[n]]
			ttl := 5 * time.Minute
			if prev.CacheWrite1h > 0 {
				ttl = time.Hour
			}
			gap := ev.Time.Sub(prev.Time)
			w := ev.CacheWrite()
			if gap <= ttl || w < minCacheRebuild || w*2 < ev.Context() {
				continue
			}
			m, ok := e.model(ev.Model)
			if !ok {
				continue
			}
			waste := (float64(ev.CacheWrite5m)*(m.Rate.CacheWrite5m-m.Rate.CacheRead) +
				float64(ev.CacheWrite1h)*(m.Rate.CacheWrite1h-m.Rate.CacheRead)) / 1e6
			got := e.led.claim(s.idx[n], waste*rebuildAvoidable)
			if got == 0 {
				continue
			}
			a := by[ev.Tool]
			if a == nil {
				a = &agg{}
				by[ev.Tool] = a
			}
			a.saved += got
			a.gap += gap.Minutes()
			a.n++
			sessSaved += got
			rebuilds++
		}
		if rebuilds > 0 {
			a := by[s.tool]
			a.ex = append(a.ex, example{projectName(s.project), sessSaved, rebuilds})
		}
	}
	var out []Finding
	for tool, a := range by {
		f := Finding{
			ID:         "cache-rebuild",
			Title:      "Avoid resuming after the prompt cache expires",
			Scope:      fmt.Sprintf("%s · %d rebuilds", tool, a.n),
			Savings:    a.saved,
			Expected:   a.saved * 0.5,
			Confidence: 0.5,
			Evidence: []string{
				fmt.Sprintf("%d times the whole context was re-written to cache after an idle gap (avg %.0f min).", a.n, a.gap/float64(a.n)),
				"A cache write costs 1.25–2× normal input; a cache read costs ~0.1×.",
				"Worst: " + topExamples(a.ex, 3),
			},
			Actions: []string{
				"Before a long break, `/compact` or wrap up: resuming a huge idle session re-pays for the whole context.",
				"Batch related prompts into one sitting so the cache stays warm.",
			},
		}
		out = append(out, f)
	}
	return out
}
