package ingest

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rojinebrahimi/budgeteer/internal/usage"
)

func claudeFixture(id, req, blockType string) string {
	return `{"type":"assistant","timestamp":"2026-10-30T10:00:00.000Z","sessionId":"s1","requestId":"` + req +
		`","uuid":"u-` + blockType + `","cwd":"/work/app","isSidechain":false,"effort":"high","message":{"id":"` + id +
		`","model":"claude-sonnet-4-5","content":[{"type":"` + blockType + `"}],"usage":{"input_tokens":2,` +
		`"cache_creation_input_tokens":300,"cache_read_input_tokens":1000,"output_tokens":50,` +
		`"output_tokens_details":{"thinking_tokens":20},` +
		`"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}}}`
}

func write(t *testing.T, dir, rel string, lines ...string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Regression: Claude Code logs one line per content block. Summing them all
// overstated spend by ~2.1x on real data.
func TestClaudeDedupesStreamedBlocks(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "projects/p/a.jsonl",
		claudeFixture("msg_1", "req_1", "thinking"),
		claudeFixture("msg_1", "req_1", "text"),
		claudeFixture("msg_1", "req_1", "tool_use"),
		claudeFixture("msg_2", "req_2", "text"),
		`{"type":"user","message":{"content":"hi"}}`,
		strings.Repeat("x", 3<<20), // huge junk line must not break the reader
	)
	ev, st, err := Load(Options{ClaudeDir: dir, CodexDir: t.TempDir(), Claude: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 2 {
		t.Fatalf("got %d events, want 2 (3 blocks of msg_1 collapse to one)", len(ev))
	}
	if st.Duplicates != 2 {
		t.Errorf("Duplicates = %d, want 2", st.Duplicates)
	}
	e := ev[0]
	if e.Input != 2 || e.CacheRead != 1000 || e.CacheWrite5m != 100 || e.CacheWrite1h != 200 || e.Output != 50 || e.Reasoning != 20 {
		t.Errorf("token mapping wrong: %+v", e)
	}
	if e.Effort != "high" || e.Project != "/work/app" || e.Tool != usage.Claude {
		t.Errorf("metadata wrong: %+v", e)
	}
}

func TestClaudeLegacyCacheCreationCountsAs5m(t *testing.T) {
	dir := t.TempDir()
	line := strings.Replace(claudeFixture("m", "r", "text"),
		`,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}`, "", 1)
	write(t, dir, "projects/p/a.jsonl", line)
	ev, _, _ := Load(Options{ClaudeDir: dir, CodexDir: t.TempDir(), Claude: true})
	if len(ev) != 1 || ev[0].CacheWrite5m != 300 || ev[0].CacheWrite1h != 0 {
		t.Fatalf("legacy aggregate not mapped to 5m: %+v", ev)
	}
}

func tc(ts string, totalIn, lastIn, cached, out int) string {
	f := func(in int) string {
		return `{"input_tokens":` + itoa(in) + `,"cached_input_tokens":` + itoa(cached) + `,"output_tokens":` + itoa(out) + `,"reasoning_output_tokens":5,"total_tokens":0}`
	}
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
		f(totalIn) + `,"last_token_usage":` + f(lastIn) + `}}}`
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func TestCodexUsesPerCallUsageAndDropsRepeats(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "sessions/2026/10/r.jsonl",
		`{"timestamp":"2026-10-30T09:00:00.000Z","type":"session_meta","payload":{"id":"sess-A","cwd":"/work/cx"}}`,
		`{"timestamp":"2026-10-30T09:00:01.000Z","type":"turn_context","payload":{"model":"gpt-5","effort":"medium"}}`,
		// Forked session: first cumulative total is huge (inherited history) but
		// this call only used 1000 input tokens, 800 of them cached.
		tc("2026-10-30T09:01:00.000Z", 247_000_000, 1000, 800, 40),
		tc("2026-10-30T09:01:00.500Z", 247_000_000, 1000, 800, 40), // re-emitted, same total
		tc("2026-10-30T09:02:00.000Z", 247_002_000, 2000, 1500, 60),
	)
	ev, _, err := Load(Options{ClaudeDir: t.TempDir(), CodexDir: dir, Codex: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 2 {
		t.Fatalf("got %d events, want 2 (repeat dropped)", len(ev))
	}
	a := ev[0]
	// OpenAI counts cached tokens inside input_tokens; they must be split out.
	if a.Input != 200 || a.CacheRead != 800 || a.Output != 40 || a.Reasoning != 5 {
		t.Errorf("first call mapped wrong: %+v", a)
	}
	if a.Context() != 1000 {
		t.Errorf("context = %d, want 1000 (not the 247M cumulative)", a.Context())
	}
	if a.SessionID != "sess-A" || a.Model != "gpt-5" || a.Effort != "medium" || a.Project != "/work/cx" {
		t.Errorf("metadata wrong: %+v", a)
	}
}

func TestSinceFiltersEvents(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "projects/p/a.jsonl", claudeFixture("m1", "r1", "text"))
	since := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	ev, _, _ := Load(Options{ClaudeDir: dir, CodexDir: t.TempDir(), Claude: true, Since: since})
	if len(ev) != 0 {
		t.Fatalf("event before --since should be dropped, got %d", len(ev))
	}
}
