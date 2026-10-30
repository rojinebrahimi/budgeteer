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
	Max    int  // recommendations to show; 0 = all
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
	width := o.Width
	if width <= 0 {
		width = 100
	}
	p := func(f string, a ...any) { fmt.Fprintf(w, f+"\n", a...) }

	p("")
	p(" %s  %s", st.bold(st.cyan("budgeteer")), st.dim(fmt.Sprintf("last %s · %s → %s", o.Window, r.From.Format("Jan 02"), r.To.Format("Jan 02"))))
	p(" %s", st.dim(strings.Repeat("─", min(width, 100)-1)))
	if r.Total == 0 {
		p("")
		p(" No usage found in this window. Check --claude-dir / --codex-dir or widen --since.")
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
			delta = st.dim(fmt.Sprintf("● flat vs previous %s", o.Window))
		}
	}
	p("")
	p(" %s  %s  %s", st.bold("SPEND"), st.bold(Money(r.Total)), delta)
	run := fmt.Sprintf("run-rate ≈ %s/mo", Money(r.Forecast.MonthlyRate))
	if r.Forecast.Budget > 0 {
		if r.Forecast.OverBudget {
			run += "  " + st.red(fmt.Sprintf("● over your %s budget", Money(r.Forecast.Budget)))
		} else {
			run += "  " + st.green(fmt.Sprintf("● within your %s budget", Money(r.Forecast.Budget)))
		}
	}
	p("   %s", st.dim(run))
	p("")
	for _, s := range r.ByTool {
		p("   %-9s %s %8s %s", s.Name, bar(st, s.Share, 24), Money(s.Cost), st.dim(fmt.Sprintf("%3.0f%%", s.Share*100)))
	}
	p("")
	p("   %-9s %s", "daily", st.cyan(Spark(r.Days)))
	p("   %s", st.dim(fmt.Sprintf("%s sessions · %s calls · cache hit %.0f%% · %s output tokens (%s reasoning)",
		Num(r.Sessions), Num(r.Tokens.Calls), r.Tokens.CacheHitRate*100, Tok(r.Tokens.Output), Tok(r.Tokens.Reasoning))))

	p("")
	p(" %s", st.bold("MODELS"))
	for i, s := range r.ByModel {
		if i == 4 {
			p("   %s", st.dim(fmt.Sprintf("+%d more", len(r.ByModel)-4)))
			break
		}
		name := trunc(s.Name, 24)
		if s.Estimate {
			name += "*"
		}
		p("   %-26s %s %8s %s", name, bar(st, s.Share, 18), Money(s.Cost), st.dim(fmt.Sprintf("%3.0f%%", s.Share*100)))
	}

	if !o.Plain {
		p("")
		if len(r.Findings) == 0 {
			p(" %s  %s", st.bold("SAVINGS"), "Nothing material to trim, your usage looks efficient for this window.")
		} else {
			pct := 100 * r.Expected / r.Total
			p(" %s  %s expected  %s", st.bold("SAVINGS"), st.green(st.bold(Money(r.Expected))),
				st.dim(fmt.Sprintf("(up to %s · ≈%.0f%% of spend)", Money(r.Ceiling), pct)))
			shown, hidden := cut(r.Findings, o.Max)
			for i, f := range shown {
				p("")
				p("  %s  %s", st.cyan(st.bold(fmt.Sprintf("%d", i+1))), st.bold(f.Title))
				p("     %s", st.dim(f.Scope))
				p("     %s %s   %s   %s", st.dim("saves"), st.green(st.bold("≈ "+Money(f.Expected))),
					st.dim("up to "+Money(f.Savings)), dots(st, f.Confidence))
				for _, e := range f.Evidence {
					wrapped(w, "     "+st.dim("why")+"  ", 10, e, width)
				}
				for _, a := range f.Actions {
					wrapped(w, "     "+st.cyan("do")+"   ", 10, strip(a), width)
				}
			}
			if hidden > 0 {
				p("")
				p("     %s", st.dim(fmt.Sprintf("+ %d smaller moves not shown (≈%s expected). Use --top N to see more.", hidden, Money(r.Expected-sumExpected(shown)))))
			}
		}
	}

	if len(r.Top) > 0 {
		p("")
		p(" %s  %s", st.bold("HOTSPOTS"), st.dim(fmt.Sprintf("top 10%% of sessions = %.0f%% of spend", r.TopDecile*100)))
		p("   %s", st.dim(fmt.Sprintf("%8s %5s  %-7s %-20s %6s %6s %9s", "cost", "share", "tool", "model", "calls", "span", "peak ctx")))
		for _, s := range r.Top {
			flag := ""
			if s.Outlier {
				flag = " " + st.red("⚑")
			}
			who := string(s.Tool)
			if s.Subagent {
				who += "·sub"
			}
			p("   %8s %4.0f%%  %-7s %-20s %6s %6s %9s%s", Money(s.Cost), s.Share*100, who, trunc(s.Model, 20), Num(s.Turns), Dur(s.Duration), Tok(s.MaxContext), flag)
			p("            %s %s", st.dim("└"), st.dim(trunc(s.Project, 70)))
		}
	}
	p("")
	notes(w, r, st, o)
}

// cut returns the first max findings (all when max <= 0) and how many were dropped.
func cut(fs []analyze.Finding, max int) ([]analyze.Finding, int) {
	if max <= 0 || len(fs) <= max {
		return fs, 0
	}
	return fs[:max], len(fs) - max
}

func sumExpected(fs []analyze.Finding) float64 {
	var t float64
	for _, f := range fs {
		t += f.Expected
	}
	return t
}

// bar draws a share as a filled/empty block gauge of fixed width.
func bar(st style, share float64, width int) string {
	n := int(math.Round(share * float64(width)))
	if share > 0 && n == 0 {
		n = 1
	}
	n = min(n, width)
	return st.cyan(strings.Repeat("█", n)) + st.dim(strings.Repeat("░", width-n))
}

// dots renders confidence as five pips plus the percentage.
func dots(st style, conf float64) string {
	n := int(math.Round(conf * 5))
	return st.cyan(strings.Repeat("●", n)) + st.dim(strings.Repeat("○", 5-n)) + st.dim(fmt.Sprintf(" %.0f%% confidence", conf*100))
}

// Num formats an integer with thousands separators.
func Num(n int) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func notes(w io.Writer, r *analyze.Result, st style, o Options) {
	width := o.Width
	if width <= 0 {
		width = 100
	}
	say := func(text string) {
		for _, ln := range wrapLines(text, width-2) {
			fmt.Fprintf(w, " %s\n", st.dim(ln))
		}
	}
	say("Costs are API-list-price equivalents from local logs; subscription plans (Pro/Max/Plus) are not billed per token.")
	if len(r.Estimated) > 0 {
		say("* Estimated rate (unknown model → family default): " + strings.Join(r.Estimated, ", ") + ". Override with --pricing.")
	}
	if len(r.Unpriced) > 0 {
		say("Not priced (no rate known): " + strings.Join(r.Unpriced, ", "))
	}
	for _, n := range o.Note {
		say(n)
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
	if !o.Plain {
		p("\n### Best moves to cut cost")
	}
	if o.Plain {
		// summary: no advice section, and no heading for one
	} else if len(r.Findings) == 0 {
		p("\nNothing material to trim in this window.")
	} else {
		p("\nExpected savings **%s** (ceiling %s, ≈%.0f%% of spend).", Money(r.Expected), Money(r.Ceiling), 100*r.Expected/r.Total)
		shown, hidden := cut(r.Findings, o.Max)
		for i, f := range shown {
			p("\n**%d. %s** — %s\n- Saves ≈ **%s** (up to %s, %.0f%% confidence)", i+1, f.Title, f.Scope, Money(f.Expected), Money(f.Savings), f.Confidence*100)
			for _, e := range f.Evidence {
				p("- Why: %s", e)
			}
			for _, a := range f.Actions {
				p("- Do: %s", a)
			}
		}
		if hidden > 0 {
			p("\n_+ %d smaller moves not shown. Use `--top N` to see more._", hidden)
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

// Money formats USD: two decimals, thousands separators from $1,000 up.
func Money(v float64) string {
	if v >= 1000 {
		return "$" + Num(int(math.Round(v)))
	}
	return fmt.Sprintf("$%.2f", v)
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

// wrapLines word-wraps text into lines of at most room runes.
func wrapLines(text string, room int) []string {
	var out []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && len([]rune(line))+1+len([]rune(word)) > room {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	return append(out, line)
}

// wrapped prints text word-wrapped to width columns. The first line follows
// prefix (whose visible width is indent); continuation lines are indented to
// match, so long sentences stay readable instead of breaking mid-word.
func wrapped(w io.Writer, prefix string, indent int, text string, width int) {
	if width <= 0 {
		width = 100
	}
	for i, ln := range wrapLines(text, max(width-indent, 30)) {
		if i == 0 {
			fmt.Fprintln(w, prefix+ln)
		} else {
			fmt.Fprintln(w, strings.Repeat(" ", indent)+ln)
		}
	}
}

// strip removes Markdown backticks for terminal output.
func strip(s string) string { return strings.ReplaceAll(s, "`", "") }
