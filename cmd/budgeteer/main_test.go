package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	ok := map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "1m": 30 * 24 * time.Hour}
	for in, d := range ok {
		got, err := parseSince(in, now)
		if err != nil || !got.Equal(now.Add(-d)) {
			t.Errorf("parseSince(%q) = %v, %v", in, got, err)
		}
	}
	if got, err := parseSince("2026-10-01", now); err != nil || got.Day() != 1 {
		t.Errorf("date form: %v %v", got, err)
	}
	for _, bad := range []string{"", "abc", "0d", "-3d", "2099-01-01", "7x"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("parseSince(%q) should fail", bad)
		}
	}
}

func TestRunCommands(t *testing.T) {
	empty := t.TempDir()
	base := []string{"--claude-dir", empty, "--codex-dir", empty, "--no-color"}
	for _, c := range []struct {
		args []string
		want string
	}{
		{append([]string{"suggest"}, base...), "No usage found"},
		{append([]string{"--format", "markdown"}, base...), "No usage found"},
		{[]string{"version"}, "budgeteer"},
		{[]string{"prices"}, "claude-opus-4-5"},
	} {
		var out, errw bytes.Buffer
		if err := run(c.args, &out, &errw); err != nil || !strings.Contains(out.String(), c.want) {
			t.Errorf("run(%v) = %v, out=%q", c.args, err, out.String())
		}
	}
	var out, errw bytes.Buffer
	if err := run([]string{"nope"}, &out, &errw); err == nil {
		t.Error("unknown command must error")
	}
	if err := run(append([]string{"--tool", "gemini"}, base...), &out, &errw); err == nil {
		t.Error("bad --tool must error")
	}
	if err := run(append([]string{"--format", "xml"}, base...), &out, &errw); err == nil {
		t.Error("bad --format must error")
	}
}
