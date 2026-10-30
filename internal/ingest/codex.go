package ingest

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/rojinebrahimi/budgeteer/internal/usage"
)

type codexLine struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexCounters struct {
	Input     int64 `json:"input_tokens"`
	Cached    int64 `json:"cached_input_tokens"`
	Output    int64 `json:"output_tokens"`
	Reasoning int64 `json:"reasoning_output_tokens"`
}

func (c codexCounters) zero() bool { return c == codexCounters{} }

type codexPayload struct {
	// session_meta
	ID  string `json:"id"`
	Cwd string `json:"cwd"`
	// turn_context
	Model  string `json:"model"`
	Effort string `json:"effort"`
	// event_msg
	Type string `json:"type"`
	Info *struct {
		Total codexCounters `json:"total_token_usage"`
		Last  codexCounters `json:"last_token_usage"`
	} `json:"info"`
}

var tokenCountTag = []byte(`"token_count"`)

// parseCodex walks one rollout file.
//
// Each token_count event carries both a cumulative total and the usage of the
// single call that just finished (last_token_usage). The per-call figure is
// the source of truth: differencing cumulative totals looks equivalent but
// breaks on forked or resumed sessions, whose first total already includes
// the parent's entire history (observed: a 248M-token "call"). The cumulative
// total is used only to drop re-emitted events, which repeat an unchanged
// total.
func parseCodex(r io.Reader, path string) parsed {
	var (
		p                 parsed
		session           = strings.TrimSuffix(filepath.Base(path), ".jsonl")
		cwd, model, level string
		prev              codexCounters
		havePrev          bool
	)
	eachLine(r, func(line []byte) {
		isTokens := bytes.Contains(line, tokenCountTag)
		if !isTokens && !bytes.Contains(line, []byte(`"session_meta"`)) && !bytes.Contains(line, []byte(`"turn_context"`)) {
			return
		}
		var l codexLine
		if json.Unmarshal(line, &l) != nil {
			return
		}
		var pl codexPayload
		if json.Unmarshal(l.Payload, &pl) != nil {
			return
		}
		switch l.Type {
		case "session_meta":
			if pl.ID != "" {
				session = pl.ID
			}
			if pl.Cwd != "" {
				cwd = pl.Cwd
			}
		case "turn_context":
			if pl.Model != "" {
				model = pl.Model
			}
			if pl.Cwd != "" {
				cwd = pl.Cwd
			}
			level = pl.Effort
		case "event_msg":
			if pl.Type != "token_count" || pl.Info == nil || model == "" {
				return
			}
			dup := havePrev && pl.Info.Total == prev
			prev, havePrev = pl.Info.Total, true
			delta := pl.Info.Last
			if dup || delta.zero() {
				return
			}
			p.lines++
			cached := min(delta.Cached, delta.Input)
			p.events = append(p.events, keyed{ev: usage.Event{
				Tool:      usage.Codex,
				SessionID: session,
				Project:   cwd,
				Model:     model,
				Time:      l.Timestamp,
				Effort:    level,
				// OpenAI counts cached tokens *inside* input_tokens.
				Input:     delta.Input - cached,
				CacheRead: cached,
				Output:    delta.Output,
				Reasoning: min(delta.Reasoning, delta.Output),
			}})
		}
	})
	return p
}
