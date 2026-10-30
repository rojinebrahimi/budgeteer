// Command budgeteer analyses your Claude Code and Codex usage and suggests the
// best moves to cut cost.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rojinebrahimi/budgeteer/internal/analyze"
	"github.com/rojinebrahimi/budgeteer/internal/ingest"
	"github.com/rojinebrahimi/budgeteer/internal/pricing"
	"github.com/rojinebrahimi/budgeteer/internal/render"
)

// Set at release time via -ldflags "-X main.version=...".
var version = "dev"

const usageText = `budgeteer — find the best moves to cut your Claude Code and Codex spend.

Usage:
  budgeteer [suggest] [flags]   ranked cost-saving recommendations (default)
  budgeteer summary   [flags]   spend breakdown only, no advice
  budgeteer prices    [flags]   show the price table in effect
  budgeteer version

Common flags:
  --since WINDOW     7d (default), 24h, 2w, 3m, or a date like 2026-10-01
  --tool NAME        all (default), claude, or codex
  --format FORMAT    text (default), markdown, or json
  --budget USD       monthly budget; flags when the run-rate exceeds it
  --top N            recommendations and hotspot sessions to list (default 5)
  --pricing FILE     JSON price overrides (also $BUDGETEER_PRICING)
  --claude-dir DIR   default ~/.claude        --codex-dir DIR   default ~/.codex
  --no-color         disable ANSI colour (also honours $NO_COLOR; $FORCE_COLOR forces it on)
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "budgeteer:", err)
			os.Exit(1)
		}
	}
}

func run(args []string, out, errw io.Writer) error {
	cmd := "suggest"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "help":
		fmt.Fprint(out, usageText)
		return nil
	case "version":
		fmt.Fprintln(out, "budgeteer", version)
		return nil
	case "suggest", "summary", "prices":
	default:
		fmt.Fprint(errw, usageText)
		return fmt.Errorf("unknown command %q", cmd)
	}

	fs := flag.NewFlagSet("budgeteer "+cmd, flag.ContinueOnError)
	fs.SetOutput(errw)
	fs.Usage = func() { fmt.Fprint(errw, usageText) }
	var (
		since     = fs.String("since", "7d", "")
		tool      = fs.String("tool", "all", "")
		format    = fs.String("format", "text", "")
		budget    = fs.Float64("budget", 0, "")
		top       = fs.Int("top", 5, "")
		pricePath = fs.String("pricing", os.Getenv("BUDGETEER_PRICING"), "")
		claudeDir = fs.String("claude-dir", "", "")
		codexDir  = fs.String("codex-dir", "", "")
		noColor   = fs.Bool("no-color", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	table, err := pricing.Load(*pricePath)
	if err != nil {
		return err
	}
	if cmd == "prices" {
		return printPrices(out, table)
	}

	now := time.Now()
	from, err := parseSince(*since, now)
	if err != nil {
		return err
	}
	window := now.Sub(from)
	prevFrom := from.Add(-window)

	opt := ingest.Options{
		ClaudeDir: *claudeDir, CodexDir: *codexDir, Since: prevFrom,
		Claude: *tool == "all" || *tool == "claude",
		Codex:  *tool == "all" || *tool == "codex",
	}
	if !opt.Claude && !opt.Codex {
		return fmt.Errorf("--tool must be all, claude or codex (got %q)", *tool)
	}
	events, st, err := ingest.Load(opt)
	if err != nil {
		return err
	}

	res := analyze.Run(events, analyze.Config{Table: table, From: from, To: now, Top: *top, Budget: *budget})
	if cmd == "summary" {
		res.Findings, res.Ceiling, res.Expected = nil, 0, 0
	}

	ro := render.Options{
		Plain:  cmd == "summary",
		Max:    *top,
		Width:  termWidth(),
		Window: strings.TrimSpace(*since),
		Color:  !*noColor && os.Getenv("NO_COLOR") == "" && (isTTY(out) || os.Getenv("FORCE_COLOR") != ""),
		Note: []string{fmt.Sprintf("Read %d files, %d usage lines → %d unique calls (%d duplicate log lines merged).",
			st.Files, st.Lines, st.Lines-st.Duplicates, st.Duplicates)},
	}
	switch *format {
	case "json":
		return render.JSON(out, res)
	case "markdown", "md":
		render.Markdown(out, res, ro)
	case "text":
		render.Text(out, res, ro)
	default:
		return fmt.Errorf("--format must be text, markdown or json (got %q)", *format)
	}
	return nil
}

var sinceRe = regexp.MustCompile(`^(\d+)\s*([hdwm])$`)

// parseSince accepts "36h", "7d", "2w", "3m" (30-day months) or "2026-10-01".
func parseSince(s string, now time.Time) (time.Time, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if m := sinceRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n <= 0 {
			return time.Time{}, fmt.Errorf("--since must be positive (got %q)", s)
		}
		unit := map[string]time.Duration{"h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour, "m": 30 * 24 * time.Hour}[m[2]]
		return now.Add(-time.Duration(n) * unit), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		if t.After(now) {
			return time.Time{}, fmt.Errorf("--since date %s is in the future", s)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since %q: use forms like 24h, 7d, 2w, 3m or 2026-10-01", s)
}

func printPrices(w io.Writer, t *pricing.Table) error {
	fmt.Fprintf(w, "%-20s %4s %8s %8s %9s %9s %9s\n", "model", "tier", "input", "output", "cache_rd", "write_5m", "write_1h")
	for _, m := range t.Models() {
		fmt.Fprintf(w, "%-20s %4d %8.3f %8.3f %9.3f %9.3f %9.3f\n", m.ID, m.Tier, m.Input, m.Output, m.CacheRead, m.CacheWrite5m, m.CacheWrite1h)
	}
	fmt.Fprintln(w, "\nUSD per 1M tokens. Unknown models fall back to a family default and are marked as estimates.")
	return nil
}

// termWidth honours $COLUMNS, defaulting to 100 so wrapped text is readable
// when piped.
func termWidth() int {
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n >= 40 {
		return min(n, 140)
	}
	return 100
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
