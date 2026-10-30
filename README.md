# budgeteer

**Find the best moves to cut your Claude Code and Codex spend, based on what you actually spent.**

budgeteer reads the session logs your agents already write to disk, prices every API call, and tells you which habit changes would save the most money, ranked, with dollar estimates and the exact setting or command to change. It runs entirely on your machine, makes no network calls, and ships three ways: a standalone Go CLI, a Claude Code plugin (`/budgeteer:suggest`), and a Codex custom prompt.

```text
budgeteer  last 7d · Oct 23 → Oct 30

SPEND  $184.20   ▲ +14% vs previous 7d
  claude $151.40 (82%) · codex $32.80 (18%)
  ▁▆█▃▂▂▁▂  daily
  96 sessions · 4120 calls · cache hit 91% · out 2.1M tok (0.9M reasoning)
  run-rate ≈ $401/mo  — over your $300 budget

SAVINGS  $41.60 expected · (ceiling $93.10, ~23% of spend)

1. Compact or restart before the context balloons  claude · 31 sessions
   $19.80 expected · up to $36.00 · 55% confidence
   why: 920 calls carried over 120k tokens of context (peak 410k); every call re-reads all of it.
   why: Reading the portion above 60k cost about $72.40.
   do:  Run /compact at a natural checkpoint (feature done, tests green) …
   do:  Use /clear (or a fresh session) when switching tasks …

2. Use sonnet instead of opus for light work  claude · claude-opus-4-5 · 9 sessions
   $14.10 expected · up to $38.50 · 37% confidence
   do:  Switch with /model sonnet for exploration, docs, renames and small edits …
   do:  Or start with claude --model opusplan: the frontier model plans, Sonnet executes.
   …
```

> The numbers above are illustrative. Run it on your own logs to see yours.

---

## Contents

- [Install](#install) · [Usage](#usage) · [What it recommends](#what-it-recommends)
- [How it works](#how-it-works) · [Accuracy and limits](#accuracy-and-limits) · [Pricing overrides](#pricing-overrides)
- [The Claude Code plugin](#the-claude-code-plugin) · [Codex](#codex) · [Publishing to a marketplace](#publishing-to-a-claude-code-marketplace)
- [Development and the CI/CD pipeline](#development-and-the-cicd-pipeline) · [Roadmap](#roadmap) · [License](#license)

## Install

### As a Claude Code plugin (recommended)

```text
/plugin marketplace add rojinebrahimi/budgeteer
/plugin install budgeteer@budgeteer
```

Then run `/budgeteer:suggest` (or `/budgeteer:summary`). The plugin also ships a **skill**, so asking Claude "how can I spend less on tokens?" triggers the same analysis automatically.

On first use the bundled launcher provisions the binary (see [launcher](#the-launcher)); nothing else to install.

### As a standalone CLI

```bash
go install github.com/rojinebrahimi/budgeteer/cmd/budgeteer@latest
```

or download a prebuilt archive for macOS / Linux / Windows from the [Releases](https://github.com/rojinebrahimi/budgeteer/releases) page. Every release publishes a `checksums.txt`.

### For Codex

Install the CLI (above), then either add the native plugin (`codex plugin marketplace add rojinebrahimi/budgeteer && codex plugin add budgeteer@budgeteer`) or run `make codex-install` for a `/prompts:budgeteer` custom prompt. Details in [Codex](#codex).

## Usage

```bash
budgeteer                          # same as `budgeteer suggest`, last 7 days, both tools
budgeteer suggest --since 30d      # wider window
budgeteer suggest --tool codex     # only Codex
budgeteer suggest --budget 300     # also check run-rate against a $300/month budget
budgeteer summary                  # spend breakdown, no advice
budgeteer suggest --format json    # machine-readable (pipe to jq, dashboards, CI)
budgeteer suggest --format markdown
budgeteer prices                   # the price table in effect
```

| Flag | Meaning | Default |
|---|---|---|
| `--since` | Window: `24h`, `7d`, `2w`, `3m` (30-day months) or a date `2026-10-01` | `7d` |
| `--tool` | `all`, `claude` or `codex` | `all` |
| `--format` | `text`, `markdown` or `json` | `text` |
| `--budget` | Monthly budget in USD; reports whether the run-rate exceeds it | off |
| `--top` | Hotspot sessions to list | `5` |
| `--pricing` | JSON file with price overrides (or `$BUDGETEER_PRICING`) | built-in |
| `--claude-dir`, `--codex-dir` | Log locations | `~/.claude`, `~/.codex` |
| `--no-color` | Disable ANSI colour (also honours `$NO_COLOR`) | auto |

"Last spent" is the `--since` window; budgeteer also loads the equally long window before it so it can show the change versus the previous period.

## What it recommends

Four detectors run in priority order. Each one claims dollars from a shared ledger (see [the ledger](#2-the-savings-ledger-no-double-counting)), so the total is honest.

| Recommendation | Looks for | Typical action |
|---|---|---|
| **Model downshift** | Sessions on an expensive model whose workload is in the lightest 40 % of *your own* work (short outputs, little reasoning, few calls). Subagent traffic is judged separately. | `/model sonnet`, `claude --model opusplan`, `CLAUDE_CODE_SUBAGENT_MODEL=haiku`, `codex -m gpt-5-mini` |
| **Context bloat** | Calls whose prompt exceeds 120k tokens. Each call re-reads the whole context, so cost grows roughly quadratically with session length. | `/compact` at checkpoints, `/clear` between tasks |
| **Reasoning effort** | Reasoning/thinking tokens (billed as output) at high effort. | `claude --effort medium`, `model_reasoning_effort = "medium"` for Codex |
| **Cache rebuild** | Idle gaps that outlived the prompt cache, forcing the full context to be re-written at 1.25–2× instead of read at ~0.1×. | Compact before long breaks; batch work |

Alongside the advice you get **hotspots** (your costliest sessions, outliers flagged with a robust statistic) and a **run-rate** forecast.

## How it works

```text
~/.claude/projects/**/*.jsonl ─┐                                   ┌─▶ text
                               ├─▶ ingest ─▶ price ─▶ sessions ─▶ detectors ─▶ rank ─┼─▶ markdown
~/.codex/sessions/**/*.jsonl  ─┘  (parallel,   (table +   (group)     │  ▲                └─▶ json
                                   de-duped)   fallbacks)            └──┴── savings ledger
```

### 1. Ingest: turning logs into trustworthy events

Both agents log token usage, in different shapes. Both are normalised into one `usage.Event` (`internal/usage`), so nothing downstream knows which tool it came from.

| | Claude Code | Codex |
|---|---|---|
| Source | `~/.claude/projects/**/*.jsonl` (including subagent files) | `~/.codex/sessions/**` and `archived_sessions/` |
| Usage record | `assistant` lines, `message.usage` | `token_count` events, `last_token_usage` |
| Cache | read + 5-minute and 1-hour writes | cached tokens (a subset of input) |
| Reasoning | `output_tokens_details.thinking_tokens` | `reasoning_output_tokens` |

Three correctness details matter, and each was found by running against real logs:

- **One API call is logged many times.** Claude Code writes a line per content block (thinking, text, every tool call), each repeating the same usage. Summing naively overstated spend **2.1×** (39,911 lines → 18,605 real calls on the author's machine). Events are de-duplicated on `message.id + requestId`, taking the maximum of each counter.
- **Codex totals are cumulative, but forks start mid-way.** Differencing consecutive totals produced a phantom 248M-token "call" at the start of a forked session. budgeteer uses the per-call `last_token_usage` and only uses the cumulative total to drop re-emitted events.
- **OpenAI counts cached tokens inside `input_tokens`.** They are split out so cached and uncached input are priced separately, and reasoning tokens (already inside output) are never added twice.

Files are parsed concurrently (one worker per CPU), files older than the window are skipped by modification time, and lines are streamed so multi-megabyte request blobs cannot overflow a scanner buffer. About 200 files / 10k calls take roughly 3 seconds.

### 2. The savings ledger (no double-counting)

The naive way to build an advisor is to run independent heuristics and add up their savings. That breaks the moment two of them describe the same dollars. A long session on Opus is both "wrong model" *and* "bloated context", and you cannot save the same $10 twice.

So every event starts with `cost × 0.9` in a ledger, and each detector must **claim** its savings from it. A claim is granted only from what is left; claimed dollars are gone for later detectors. Detectors run coarse-to-fine (downshift, reasoning, bloat, cache rebuild), so the high-yield lever claims first. The test suite checks the invariant on 25 randomised workloads: *total claimed savings never exceed the amount spent*, and *expected ≤ ceiling*.

### 3. Detectors and their math

**Model downshift.** For each tool, each session gets a *demand* score: the percentile rank among your own sessions of its p90 output length (weight 0.5), reasoning share (0.3) and number of calls (0.2). Ranking against your own history is deliberate: nearly every agentic turn is short, so an absolute threshold would flag everything. Sessions below the 40th percentile (and above $0.25) are candidates, with `confidence = 0.25 + 0.5·(0.4 − demand)/0.4` (+0.10 for subagents, capped at 0.8). The counterfactual re-prices the identical tokens on the next model down the ladder with a 15 % allowance for the cheaper model being less token-efficient: `saving = cost − 1.15 × cost_on_cheaper`.

**Context bloat.** For a call with prompt size `c` above the 120k ceiling: `saving = 0.6 × input_side_cost × (c − 60k)/c`. The 60k is the size a compaction is assumed to reach, and 0.6 reflects that compaction is lossy and the summary still has to be read. Confidence 0.55.

**Reasoning effort.** Reasoning tokens × the output rate, trimmed by 35 % when the log records high effort (confidence 0.55), or by 25 % when effort is unknown but over half the output is reasoning (confidence 0.35).

**Cache rebuild.** For consecutive calls in a session where the gap exceeds the cache TTL (5 min, or 1 h if the previous call used the long TTL) and ≥ 50 % of the new prompt was cache-*written*: waste = `tokens × (write_price − read_price)`, half assumed avoidable. Confidence 0.5.

**Hotspots.** Sessions are ranked by cost. Outliers are flagged using the Iglewicz–Hoaglin *modified z-score* (`0.6745·(x − median)/MAD`, threshold 3.5) on log-cost. Median/MAD instead of mean/stddev so the very outliers you are hunting cannot hide themselves by inflating the baseline. The top-decile share (Pareto) is reported too.

**Forecast.** Daily spend, with idle days included as zeros, goes through an exponentially weighted moving average (α = 0.3) so a one-off expensive day fades. Run-rate = EWMA × 30.4.

### 4. Ranking

Each finding carries a *ceiling* (if fully adopted) and an *expected* value (confidence-weighted). Findings are sorted by expected savings; headline numbers sum the ledger-backed values. Ties are broken deterministically so output is stable run to run.

## Accuracy and limits

- **Costs are API-list-price equivalents.** If you are on Claude Pro/Max or ChatGPT Plus, you are not billed per token; read the dollars as "how much capacity this uses".
- **Recommendations are estimates, not guarantees.** Confidence is reported so you can discount appropriately. Downshifting a task that turns out to be hard costs you retries.
- **Unknown models.** A model the table does not know falls back to a family default (`claude-*opus*`, `gpt-*mini*`, …) and is marked `*` / "estimated". A model with no plausible family (third-party models routed through a gateway such as Kimi, GLM or DeepSeek) is **not priced** and counts as $0, which is listed explicitly rather than guessed. Add rates with `--pricing`.
- Resumed Claude sessions that copy earlier history into a new file are de-duplicated by message ID; attribution goes to the first file in path order.
- Only what the agents log is visible: Codex does not log cache writes, and neither tool logs the content-level reason a turn was expensive.
- **Privacy:** budgeteer only reads token counts, model names, timestamps, session IDs and working directories. It never reads message content, and the binary has no networking code at all.

## Pricing overrides

The built-in table is `internal/pricing/prices.json`. Prices change; to override or add models without recompiling, pass a file (or set `BUDGETEER_PRICING`). Entries with an existing `id` replace the built-in; new ones are added. Rates are USD per million tokens. `tier` and `cheaper` place a model on the downshift ladder.

```json
{
  "models": [
    {"id": "kimi-k3", "tier": 2, "input": 0.6, "output": 2.5, "cache_read": 0.15},
    {"id": "claude-sonnet-4-5", "tier": 2, "input": 3, "output": 15,
     "cache_read": 0.3, "cache_write_5m": 3.75, "cache_write_1h": 6, "cheaper": "claude-haiku-4-5"}
  ],
  "families": [{"match": "^claude-.*sonnet", "use": "claude-sonnet-4-5"}]
}
```

Model names are normalised first: case, `-20251101` date suffixes, `[1m]` context tags and `us.anthropic.` / `anthropic/` prefixes are stripped. Resolution is exact id, then longest prefix, then a family regex.

## The Claude Code plugin

```text
.claude-plugin/plugin.json        plugin manifest (name, version, icon, license)
.claude-plugin/marketplace.json   makes this repo an installable marketplace
commands/suggest.md               /budgeteer:suggest
commands/summary.md               /budgeteer:summary
skills/budgeteer/SKILL.md         auto-triggers on "how do I reduce my token spend?"
bin/budgeteer-run                 launcher that finds or provisions the binary
```

The slash commands use Claude Code's `` !`command` `` injection: the report is generated by the CLI *before* the model sees the prompt, then the model summarises it using only those figures. The model never computes costs itself, so the numbers cannot be hallucinated.

### The launcher

A plugin is only files, but budgeteer is a compiled binary. `bin/budgeteer-run` tries, in order: a `budgeteer` on `PATH`; a cached copy for this version; the GitHub release asset for this exact version (SHA-256 verified against `checksums.txt`); building the Go source bundled in the plugin; `go install`. After the first run it adds ~25 ms.

## Codex

Codex support has three layers; use whichever fits.

1. **The CLI reads Codex logs natively.** `budgeteer suggest --tool codex` needs no Codex-side setup, and Codex-specific advice names Codex's own settings (`codex -m`, `model_reasoning_effort` in `~/.codex/config.toml`).
2. **A native Codex plugin** (`plugins/budgeteer/`, listed by `.agents/plugins/marketplace.json`) that adds a skill telling Codex to run the CLI and report only its figures:

   ```bash
   codex plugin marketplace add rojinebrahimi/budgeteer
   codex plugin add budgeteer@budgeteer
   ```

   Its layout follows the Codex marketplace format used by installed plugins (`.codex-plugin/plugin.json` + `skills/`). It was validated structurally and by tests, but not installed into a live Codex session, so please report anything that differs on your Codex version.
3. **A custom prompt** (`integrations/codex/budgeteer.md`), the lightest option: `make codex-install`, then `/prompts:budgeteer` inside Codex.

Layers 2 and 3 need the CLI on your `PATH` (see [Install](#as-a-standalone-cli)).

## Publishing to a Claude Code marketplace

A marketplace is just a Git repo with `.claude-plugin/marketplace.json`. This repo is already one, because the plugin lives at the root (`"source": "./"`).

**1. Self-hosted marketplace (works today).** Push to GitHub. Anyone can then:

```text
/plugin marketplace add rojinebrahimi/budgeteer
/plugin install budgeteer@budgeteer
```

**2. Cut a release** so the launcher can download a prebuilt binary:

```bash
# bump the version in: .claude-plugin/{plugin,marketplace}.json, bin/budgeteer-run,
#                      plugins/budgeteer/.codex-plugin/plugin.json, .agents/plugins/marketplace.json
make test && make plugin-validate
git commit -am "chore: release v0.1.1" && git tag v0.1.1 && git push --follow-tags
```

The `Release` workflow refuses a tag that does not match `plugin.json`, runs the tests, then GoReleaser builds six binaries (macOS / Linux / Windows × amd64 / arm64) and publishes them with `checksums.txt`. A test (`internal/plugin`) fails CI if any of the five version strings drift apart.

**3. Anthropic's official directory.** `make plugin-validate` runs `claude plugin validate . --profile directory`, the same checks the directory applies (README, icon, manifest). When it passes, submit through Anthropic's plugin submission flow described in the Claude Code plugin docs. The directory additionally runs its own safety and policy review.

## Development and the CI/CD pipeline

```bash
make test             # unit tests with the race detector
make lint             # gofmt, go vet, shellcheck
make run              # build and run on your own logs
make plugin-install   # install this checkout into Claude Code as a local marketplace
make help             # everything else
```

Zero third-party dependencies (standard library only), so the supply chain is just the Go toolchain.

| Workflow | Trigger | Jobs |
|---|---|---|
| `ci.yml` | push to `main`, every PR | **test** (Ubuntu + macOS × Go 1.22 + stable, `-race`, coverage) · **lint** (gofmt, vet, `go mod tidy` clean, shellcheck) · **vuln** (`govulncheck`) · **plugin** (manifests valid, versions in sync, launcher executable) · **build** (GoReleaser snapshot cross-compile) |
| `release.yml` | tag `v*` | verify tag = plugin version → test → GoReleaser publishes archives + `checksums.txt` to GitHub Releases |
| Dependabot | weekly | Actions and Go module updates |

Layout:

```text
cmd/budgeteer/       CLI entry, flags, window parsing
internal/usage/      the unified Event model
internal/pricing/    price table, name normalisation, fallbacks
internal/ingest/     Claude + Codex parsers, concurrency, de-duplication
internal/analyze/    sessions, ledger, detectors, stats, forecast
internal/render/     text / markdown / JSON
internal/plugin/     tests guarding the plugin packaging
```

## Roadmap

- Month-to-date tracking against a budget, with an alert hook
- Detector for repeated large file reads and tool-output bloat
- More agents (Gemini CLI, Cursor) behind the same `usage.Event`
- A Claude Code `SessionStart` hook that surfaces the top tip once a day (opt-in)

## License

Apache-2.0. See [LICENSE](LICENSE).
