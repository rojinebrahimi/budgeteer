package ingest

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"github.com/rojinebrahimi/budgeteer/internal/usage"
)

// claudeLine is the subset of a Claude Code transcript record we need.
// Decoding only these fields (and pre-filtering on the type tag) keeps the
// pass fast: most lines in a transcript are not billable.
type claudeLine struct {
	Timestamp   time.Time       `json:"timestamp"`
	SessionID   string          `json:"sessionId"`
	RequestID   string          `json:"requestId"`
	UUID        string          `json:"uuid"`
	Cwd         string          `json:"cwd"`
	IsSidechain bool            `json:"isSidechain"`
	Effort      json.RawMessage `json:"effort"`
	Message     struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input        int64 `json:"input_tokens"`
			CacheCreate  int64 `json:"cache_creation_input_tokens"`
			CacheRead    int64 `json:"cache_read_input_tokens"`
			Output       int64 `json:"output_tokens"`
			OutputDetail struct {
				Thinking int64 `json:"thinking_tokens"`
			} `json:"output_tokens_details"`
			Breakdown struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

var assistantTag = []byte(`"type":"assistant"`)

func parseClaude(r io.Reader) parsed {
	var p parsed
	eachLine(r, func(line []byte) {
		if !bytes.Contains(line, assistantTag) {
			return
		}
		var l claudeLine
		if json.Unmarshal(line, &l) != nil || l.Message.Usage == nil {
			return
		}
		m := l.Message
		if m.Model == "" || m.Model == "<synthetic>" {
			return
		}
		u := m.Usage
		ev := usage.Event{
			Tool:      usage.Claude,
			SessionID: l.SessionID,
			Project:   l.Cwd,
			Model:     m.Model,
			Time:      l.Timestamp,
			Sidechain: l.IsSidechain,
			Effort:    unquote(l.Effort),

			Input:        u.Input,
			CacheRead:    u.CacheRead,
			CacheWrite5m: u.Breakdown.Ephemeral5m,
			CacheWrite1h: u.Breakdown.Ephemeral1h,
			Output:       u.Output,
			Reasoning:    u.OutputDetail.Thinking,
		}
		// Older logs have only the aggregate; treat it as the 5-minute class.
		if rest := u.CacheCreate - ev.CacheWrite5m - ev.CacheWrite1h; rest > 0 {
			ev.CacheWrite5m += rest
		}
		p.lines++

		// One API message is logged once per content block (thinking, text,
		// each tool_use), all repeating the same usage. The message id plus
		// request id uniquely names the API call.
		key := ""
		switch {
		case m.ID != "":
			key = "c:" + m.ID + "|" + l.RequestID
		case l.UUID != "":
			key = "c:u:" + l.UUID
		}
		p.events = append(p.events, keyed{key: key, ev: ev})
	})
	return p
}

// unquote turns a raw JSON string into a Go string; anything else becomes "".
func unquote(raw json.RawMessage) string {
	var s string
	if len(raw) > 0 && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}
