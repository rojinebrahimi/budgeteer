// Package ingest discovers and parses agent session logs into usage.Events.
//
// Files are parsed concurrently (one goroutine per CPU) because a heavy user
// accumulates hundreds of multi-megabyte JSONL files. Results are merged and
// de-duplicated afterwards so output is deterministic regardless of
// scheduling.
package ingest

import (
	"bufio"
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rojinebrahimi/budgeteer/internal/usage"
)

// Options controls what gets read.
type Options struct {
	ClaudeDir string    // default ~/.claude
	CodexDir  string    // default ~/.codex
	Since     time.Time // zero = everything
	Claude    bool
	Codex     bool
}

// Stats reports what the ingest pass saw, for transparency in the output.
type Stats struct {
	Files      int
	Lines      int // usage-bearing lines before de-duplication
	Duplicates int // lines collapsed into an earlier identical message
}

// DefaultDirs fills empty directories from the user's home.
func (o *Options) DefaultDirs() {
	home, _ := os.UserHomeDir()
	if o.ClaudeDir == "" {
		o.ClaudeDir = filepath.Join(home, ".claude")
	}
	if o.CodexDir == "" {
		o.CodexDir = filepath.Join(home, ".codex")
	}
}

type parsed struct {
	events []keyed
	lines  int
}

// keyed carries the de-duplication identity next to the event.
type keyed struct {
	key string // empty = never merge
	ev  usage.Event
}

type job struct {
	path  string
	tool  usage.Tool
	order int
}

// Load reads every selected tool's logs and returns events sorted by time.
func Load(opt Options) ([]usage.Event, Stats, error) {
	opt.DefaultDirs()
	var jobs []job
	if opt.Claude {
		for _, p := range glob(filepath.Join(opt.ClaudeDir, "projects"), opt.Since) {
			jobs = append(jobs, job{path: p, tool: usage.Claude})
		}
	}
	if opt.Codex {
		for _, sub := range []string{"sessions", "archived_sessions"} {
			for _, p := range glob(filepath.Join(opt.CodexDir, sub), opt.Since) {
				jobs = append(jobs, job{path: p, tool: usage.Codex})
			}
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].path < jobs[j].path })
	for i := range jobs {
		jobs[i].order = i
	}

	results := make([]parsed, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			f, err := os.Open(j.path)
			if err != nil {
				return // unreadable files are skipped, never fatal
			}
			defer f.Close()
			if j.tool == usage.Claude {
				results[j.order] = parseClaude(f)
			} else {
				results[j.order] = parseCodex(f, j.path)
			}
		}(j)
	}
	wg.Wait()

	st := Stats{Files: len(jobs)}
	index := map[string]int{} // dedupe key -> position in out
	var out []usage.Event
	for _, r := range results { // jobs are path-sorted, so "first wins" is stable
		st.Lines += r.lines
		for _, k := range r.events {
			if i, seen := index[k.key]; k.key != "" && seen {
				maxMerge(&out[i], k.ev)
				st.Duplicates++
				continue
			}
			if k.key != "" {
				index[k.key] = len(out)
			}
			out = append(out, k.ev)
		}
	}
	filtered := out[:0]
	for _, e := range out {
		if e.Empty() || (!opt.Since.IsZero() && e.Time.Before(opt.Since)) {
			continue
		}
		filtered = append(filtered, e)
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].Time.Before(filtered[j].Time) })
	return filtered, st, nil
}

// maxMerge folds a duplicate record into the first one. Claude Code logs one
// line per content block of a streamed message; later lines can carry a more
// complete usage block, so each counter takes the maximum seen.
func maxMerge(dst *usage.Event, src usage.Event) {
	dst.Input = max(dst.Input, src.Input)
	dst.CacheRead = max(dst.CacheRead, src.CacheRead)
	dst.CacheWrite5m = max(dst.CacheWrite5m, src.CacheWrite5m)
	dst.CacheWrite1h = max(dst.CacheWrite1h, src.CacheWrite1h)
	dst.Output = max(dst.Output, src.Output)
	dst.Reasoning = max(dst.Reasoning, src.Reasoning)
	if dst.Effort == "" {
		dst.Effort = src.Effort
	}
}

// glob lists *.jsonl under root, skipping files last modified before since
// (a file cannot contain events newer than its mtime).
func glob(root string, since time.Time) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		if !since.IsZero() {
			if info, err := d.Info(); err == nil && info.ModTime().Before(since) {
				return nil
			}
		}
		out = append(out, p)
		return nil
	})
	return out
}

// eachLine streams lines of any length (session logs contain multi-megabyte
// request blobs that overflow bufio.Scanner's default buffer).
func eachLine(r io.Reader, fn func(line []byte)) {
	br := bufio.NewReaderSize(r, 256<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			fn(bytes.TrimRight(line, "\r\n"))
		}
		if err != nil { // io.EOF or a read error: either way, we are done
			return
		}
	}
}
