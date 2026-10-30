// Package render formats an analyze.Result as terminal text, Markdown or JSON.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/rojinebrahimi/budgeteer/internal/analyze"
)

// Options tweak output.
type Options struct {
	Color  bool
	Window string // human label, e.g. "7d"
	Note   []string
	Width  int  // terminal width for word-wrapping text output; 0 = 100
	Plain  bool // summary mode: omit the savings section entirely
}

// JSON writes the machine-readable form.
func JSON(w io.Writer, r *analyze.Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

type style struct{ on bool }

func (s style) wrap(code, t string) string {
	if !s.on {
		return t
	}
	return "\x1b[" + code + "m" + t + "\x1b[0m"
}
func (s style) bold(t string) string  { return s.wrap("1", t) }
func (s style) dim(t string) string   { return s.wrap("2", t) }
func (s style) green(t string) string { return s.wrap("32", t) }
func (s style) red(t string) string   { return s.wrap("31", t) }
func (s style) cyan(t string) string  { return s.wrap("36", t) }

// Text renders the terminal report.
func Text(w io.Writer, r *analyze.Result, o Options) {
	st := style{o.Color}
	p := func(f string, a ...any) { fmt.Fprintf(w, f+"\n", a...) }

	p("%s  %s", st.bold("budgeteer"), st.dim(fmt.Sprintf("last %s · %s → %s", o.Window, r.From.Format("Jan 02"), r.To.Format("Jan 02"))))
	p("")
	if r.Total == 0 {
		p("No usage found in this window. Check --claude-dir / --codex-dir or widen --since.")
		return
	}

	delta := ""
	if r.DeltaPct != nil {
		d := *r.DeltaPct
		switch {
		case d > 5:
			delta = st.red(fmt.Sprintf("▲ %+.0f%% vs previous %s", d, o.Window))
		case d < -5:
			delta = st.green(fmt.Sprintf("▼ %+.0f%% vs previous %s", d, o.Window))
		default:
			delta = st.dim(fmt.Sprintf("≈ flat vs previous %s", o.Window))
		}
	}
	p("%s  %s   %s", st.bold("SPEND"), st.bold(Money(r.Total)), delta)
	p("  %s", joinSlices(r.ByTool, 3))
	p("  %s  %s", Spark(r.Days), st.dim("daily"))
	p("  %s", st.dim(fmt.Sprintf("%d sessions · %d calls · cache hit %.0f%% · out %s tok (%s reasoning)",
		r.Sessions, r.Tokens.Calls, r.Tokens.CacheHitRate*100, Tok(r.Tokens.Output), Tok(r.Tokens.Reasoning))))
	run := fmt.Sprintf("  run-rate ≈ %s/mo", Money(r.Forecast.MonthlyRate))
	if r.Forecast.Budget > 0 {
		if r.Forecast.OverBudget {
			run += st.red(fmt.Sprintf("  — over your %s budget", Money(r.Forecast.Budget)))
		} else {
			run += st.green(fmt.Sprintf("  — within your %s budget", Money(r.Forecast.Budget)))
		}
	}
	p("%s", run)
	p("")
	p("%s  %s", st.bold("MODELS"), joinSlices(r.ByModel, 4))
	p("")

	if o.Plain {
		// summary: no advice section at all
	} else if len(r.Findings) == 0 {
		p("%s  %s", st.bold("SAVINGS"), "Nothing material to trim — your usage looks efficient for this window.")
	} else {
		pct := 100 * r.Expected / r.Total
		p("%s  %s expected %s (ceiling %s, ~%.0f%% of spend)", st.bold("SAVINGS"), st.green(Money(r.Expected)), st.dim("·"), Money(r.Ceiling), pct)
		p("")
		for i, f := range r.Findings {
			p("%s %s  %s", st.cyan(fmt.Sprintf("%d.", i+1)), st.bold(f.Title), st.dim(f.Scope))
			p("   %s expected · up to %s · %.0f%% confidence", st.green(Money(f.Expected)), Money(f.Savings), f.Confidence*100)
			for _, e := range f.Evidence {
				wrapped(w, "   "+st.dim("why:")+" ", 8, e, o.Width)
			}
			for _, a := range f.Actions {
				wrapped(w, "   "+st.cyan("do: ")+" ", 8, strip(a), o.Width)
			}
			p("")
		}
	}

	if len(r.Top) > 0 {
		p("%s  %s", st.bold("HOTSPOTS"), st.dim(fmt.Sprintf("top 10%% of sessions = %.0f%% of spend", r.TopDecile*100)))
		for _, s := range r.Top {
			flag := ""
			if s.Outlier {
				flag = st.red(" outlier")
			}
			sub := ""
			if s.Subagent {
				sub = " (subagent)"
			}
			p("  %8s %4.0f%%  %-6s %-22s %4d calls %s peak ctx %s%s%s",
				Money(s.Cost), s.Share*100, s.Tool, trunc(s.Model, 22), s.Turns, Dur(s.Duration), Tok(s.MaxContext), sub, flag)
			p("           %s", st.dim(trunc(s.Project, 70)))
		}
		p("")
	}
	notes(w, r, st, o)
}

func notes(w io.Writer, r *analyze.Result, st style, o Options) {
	p := func(f string, a ...any) { fmt.Fprintf(w, f+"\n", a...) }
	p("%s", st.dim("Costs are API-list-price equivalents from local logs; subscription plans (Pro/Max/Plus) are not billed per token."))
	if len(r.Estimated) > 0 {
		p("%s", st.dim("Estimated rates (unknown model → family default): "+strings.Join(r.Estimated, ", ")+". Override with --pricing."))
	}
	if len(r.Unpriced) > 0 {
		p("%s", st.dim("Not priced (no rate known): "+strings.Join(r.Unpriced, ", ")))
	}
	for _, n := range o.Note {
		p("%s", st.dim(n))
	}
}

// Markdown renders the same report for chat surfaces (Claude Code slash commands).
func Markdown(w io.Writer, r *analyze.Result, o Options) {
	p := func(f string, a ...any) { fmt.Fprintf(w, f+"\n", a...) }
	p("## budgeteer — last %s (%s → %s)", o.Window, r.From.Format("Jan 02"), r.To.Format("Jan 02"))
	if r.Total == 0 {
		p("\nNo usage found in this window.")
		return
	}
	d := ""
	if r.DeltaPct != nil {
		d = fmt.Sprintf(" (%+.0f%% vs previous %s)", *r.DeltaPct, o.Window)
	}
	p("\n**Spend: %s**%s · run-rate ≈ %s/mo · %d sessions · cache hit %.0f%%", Money(r.Total), d, Money(r.Forecast.MonthlyRate), r.Sessions, r.Tokens.CacheHitRate*100)
	if r.Forecast.Budget > 0 {
		verdict := "within"
		if r.Forecast.OverBudget {
			verdict = "**over**"
		}
		p("\nRun-rate is %s your %s monthly budget.", verdict, Money(r.Forecast.Budget))
	}
	p("\n`%s`  daily", Spark(r.Days))
	p("\n| Tool | Spend | Share |\n|---|---:|---:|")
	for _, s := range r.ByTool {
		p("| %s | %s | %.0f%% |", s.Name, Money(s.Cost), s.Share*100)
	}
	p("\n| Model | Spend | Share |\n|---|---:|---:|")
	for i, s := range r.ByModel {
		if i == 6 {
			break
		}
		est := ""
		if s.Estimate {
			est = " *(est.)*"
		}
		p("| `%s`%s | %s | %.0f%% |", s.Name, est, Money(s.Cost), s.Share*100)
	}
	p("\n### Best moves to cut cost")
	if o.Plain {
		p("\n_Summary only. Run `suggest` for recommendations._")
	} else if len(r.Findings) == 0 {
		p("\nNothing material to trim in this window.")
	} else {
		p("\nExpected savings **%s** (ceiling %s, ≈%.0f%% of spend).", Money(r.Expected), Money(r.Ceiling), 100*r.Expected/r.Total)
		for i, f := range r.Findings {
			p("\n**%d. %s** — %s\n- Saves ≈ **%s** (up to %s, %.0f%% confidence)", i+1, f.Title, f.Scope, Money(f.Expected), Money(f.Savings), f.Confidence*100)
			for _, e := range f.Evidence {
				p("- Why: %s", e)
			}
			for _, a := range f.Actions {
				p("- Do: %s", a)
			}
		}
	}
	p("\n### Hotspots (top 10%% of sessions = %.0f%% of spend)\n\n| Cost | Tool | Model | Calls | Peak ctx | Project |\n|---:|---|---|---:|---:|---|", r.TopDecile*100)
	for _, s := range r.Top {
		p("| %s | %s | `%s` | %d | %s | %s |", Money(s.Cost), s.Tool, s.Model, s.Turns, Tok(s.MaxContext), trunc(s.Project, 50))
	}
	p("\n_API-list-price equivalents from local logs; subscription plans aren't billed per token._")
	if len(r.Estimated) > 0 {
		p("_Estimated rates for: %s._", strings.Join(r.Estimated, ", "))
	}
}

// ---------------------------------------------------------------- helpers

// Money formats USD with precision proportional to size.
func Money(v float64) string {
	switch {
	case v >= 1000:
		return fmt.Sprintf("$%.0f", v)
	case v >= 100:
		return fmt.Sprintf("$%.1f", v)
	default:
		return fmt.Sprintf("$%.2f", v)
	}
}

// Tok formats a token count with k/M/B suffixes.
func Tok(n int64) string {
	f := float64(n)
	switch {
	case f >= 1e9:
		return fmt.Sprintf("%.1fB", f/1e9)
	case f >= 1e6:
		return fmt.Sprintf("%.1fM", f/1e6)
	case f >= 1e3:
		return fmt.Sprintf("%.0fk", f/1e3)
	}
	return fmt.Sprintf("%d", n)
}

// Dur formats a duration compactly.
func Dur(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%.0fd", d.Hours()/24)
	case d >= time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// Spark draws daily spend as a unicode sparkline.
func Spark(days []analyze.Day) string {
	bars := []rune("▁▂▃▄▅▆▇█")
	var peak float64
	for _, d := range days {
		peak = math.Max(peak, d.Cost)
	}
	var b strings.Builder
	for _, d := range days {
		i := 0
		if peak > 0 {
			i = int(math.Round(d.Cost / peak * float64(len(bars)-1)))
		}
		b.WriteRune(bars[i])
	}
	return b.String()
}

func joinSlices(ss []analyze.Slice, n int) string {
	var parts []string
	for i, s := range ss {
		if i == n {
			parts = append(parts, fmt.Sprintf("+%d more", len(ss)-n))
			break
		}
		est := ""
		if s.Estimate {
			est = "*"
		}
		parts = append(parts, fmt.Sprintf("%s%s %s (%.0f%%)", s.Name, est, Money(s.Cost), s.Share*100))
	}
	return strings.Join(parts, " · ")
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n+1:])
}

// wrapped prints text word-wrapped to width columns. The first line follows
// prefix (whose visible width is indent); continuation lines are indented to
// match, so long sentences stay readable instead of breaking mid-word.
func wrapped(w io.Writer, prefix string, indent int, text string, width int) {
	if width <= 0 {
		width = 100
	}
	room := max(width-indent, 30)
	line, first := "", true
	flush := func() {
		if first {
			fmt.Fprintln(w, prefix+line)
		} else {
			fmt.Fprintln(w, strings.Repeat(" ", indent)+line)
		}
		line, first = "", false
	}
	for _, word := range strings.Fields(text) {
		if line != "" && len([]rune(line))+1+len([]rune(word)) > room {
			flush()
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	flush()
}

// strip removes Markdown backticks for terminal output.
func strip(s string) string { return strings.ReplaceAll(s, "`", "") }
