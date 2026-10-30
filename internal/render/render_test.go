package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rojinebrahimi/budgeteer/internal/analyze"
	"github.com/rojinebrahimi/budgeteer/internal/usage"
)

func sample() *analyze.Result {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	var ev []usage.Event
	for i := 0; i < 30; i++ {
		ev = append(ev, usage.Event{
			Tool: usage.Claude, SessionID: "s", Project: "/w/app", Model: "claude-opus-4-5",
			Time: now.Add(-time.Duration(i) * time.Hour), CacheRead: 200_000, Output: 400,
		})
	}
	return analyze.Run(ev, analyze.Config{From: now.Add(-48 * time.Hour), To: now, Loc: time.UTC})
}

func TestTextAndMarkdownSections(t *testing.T) {
	r := sample()
	var txt, md bytes.Buffer
	Text(&txt, r, Options{Window: "2d"})
	Markdown(&md, r, Options{Window: "2d"})
	for _, want := range []string{"SPEND", "MODELS", "HOTSPOTS", "API-list-price"} {
		if !strings.Contains(txt.String(), want) {
			t.Errorf("text output missing %q", want)
		}
	}
	for _, want := range []string{"## budgeteer", "### Best moves", "### Hotspots"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown output missing %q", want)
		}
	}
	if strings.Contains(txt.String(), "\x1b[") {
		t.Error("colour codes emitted although Color is off")
	}
}

func TestJSONRoundTrips(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, sample()); err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b.Bytes(), &back); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, k := range []string{"total_usd", "findings", "forecast", "tokens"} {
		if _, ok := back[k]; !ok {
			t.Errorf("JSON missing key %q", k)
		}
	}
}

func TestHelpers(t *testing.T) {
	if Money(0.5) != "$0.50" || Money(1234.5) != "$1234" {
		t.Errorf("Money: %s %s", Money(0.5), Money(1234.5))
	}
	if Tok(1_500_000) != "1.5M" || Tok(999) != "999" {
		t.Errorf("Tok: %s %s", Tok(1_500_000), Tok(999))
	}
	if s := Spark([]analyze.Day{{Cost: 0}, {Cost: 5}, {Cost: 10}}); []rune(s)[0] != '▁' || []rune(s)[2] != '█' {
		t.Errorf("Spark = %q", s)
	}
}

func TestSummaryOmitsAdviceAndTextWraps(t *testing.T) {
	r := sample()
	var b bytes.Buffer
	Text(&b, r, Options{Window: "2d", Plain: true})
	if strings.Contains(b.String(), "SAVINGS") {
		t.Error("summary output must not contain a SAVINGS section")
	}
	b.Reset()
	Markdown(&b, r, Options{Window: "2d", Plain: true})
	if strings.Contains(b.String(), "Best moves") || !strings.Contains(b.String(), "### Hotspots") {
		t.Errorf("markdown summary must drop the advice heading but keep hotspots:\n%s", b.String())
	}
	b.Reset()
	Text(&b, r, Options{Window: "2d", Width: 70})
	for _, ln := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "why:") || strings.HasPrefix(strings.TrimSpace(ln), "do:") {
			if n := len([]rune(ln)); n > 70 {
				t.Errorf("line exceeds width 70 (%d): %q", n, ln)
			}
		}
	}
}
