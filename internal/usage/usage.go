// Package usage defines the tool-agnostic event model every other package
// speaks. Both Claude Code and Codex logs are normalised into usage.Event so
// pricing and analysis never need to know which agent produced a record.
package usage

import "time"

// Tool identifies the agent that produced an event.
type Tool string

const (
	Claude Tool = "claude"
	Codex  Tool = "codex"
)

// Event is one billable model response (one API call).
//
// Token fields are disjoint: Input is *uncached* input only, so the total
// prompt size is Input + CacheRead + CacheWrite5m + CacheWrite1h. Output
// already includes Reasoning (both Anthropic thinking tokens and OpenAI
// reasoning tokens are billed as output), so Reasoning must never be added to
// Output a second time.
type Event struct {
	Tool      Tool
	SessionID string
	Project   string // working directory the session ran in
	Model     string
	Time      time.Time
	Sidechain bool   // Claude subagent traffic
	Effort    string // reasoning effort when the log records it

	Input        int64
	CacheRead    int64
	CacheWrite5m int64
	CacheWrite1h int64
	Output       int64
	Reasoning    int64

	// Filled in by pricing.
	Cost     float64 // USD
	Estimate bool    // rate was a family fallback, not an exact match
	Unpriced bool    // no rate known; Cost is zero
}

// Context is the full prompt size the model had to read for this call.
func (e Event) Context() int64 {
	return e.Input + e.CacheRead + e.CacheWrite5m + e.CacheWrite1h
}

// CacheWrite is the total of both cache-write TTL classes.
func (e Event) CacheWrite() int64 { return e.CacheWrite5m + e.CacheWrite1h }

// Empty reports whether the event carries no tokens at all.
func (e Event) Empty() bool { return e.Context() == 0 && e.Output == 0 }

// SessionKey groups events into a logical session. Subagent traffic is kept
// apart from its parent so it can be judged on its own workload.
func (e Event) SessionKey() string {
	k := string(e.Tool) + "/" + e.SessionID
	if e.Sidechain {
		k += "#sub"
	}
	return k
}
