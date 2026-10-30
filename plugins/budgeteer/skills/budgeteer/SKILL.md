---
name: budgeteer
description: Use when the user asks how to reduce, understand, or forecast their Codex or Claude Code spend, token usage, or API cost, or asks whether to switch models, compact, or lower reasoning effort to save money.
---

# budgeteer: cost-saving advice from real usage

budgeteer reads local session logs (`~/.codex/sessions`, `~/.claude/projects`), prices every API call and ranks the changes that would save the most money. Prefer it to guessing: its figures come from what the user actually spent.

## Run it

```bash
budgeteer suggest --format markdown --since 7d
```

If the command is not found, install it with Go and retry:

```bash
go install github.com/rojinebrahimi/budgeteer/cmd/budgeteer@latest
```

Variations: `--since 30d`, `--tool codex`, `--budget 200` (check run-rate against a monthly budget), `--format json` for exact numbers, and `budgeteer summary` for a breakdown without advice.

## Reporting rules

- Quote only figures from the output; never compute or invent costs yourself.
- Costs are API-list-price equivalents. On a subscription plan the user is not billed per token, so describe savings as usage headroom.
- Models marked as estimated use a family-default rate; mention it when it matters (`--pricing FILE` overrides).
- Savings are de-duplicated across recommendations, so the totals can be added up.
- Codex settings live in `~/.codex/config.toml` (`model`, `model_reasoning_effort`). Offer to change them; do not edit without being asked.
