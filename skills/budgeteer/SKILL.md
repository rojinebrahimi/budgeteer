---
name: budgeteer
description: Use when the user asks how to reduce, understand, or forecast their Claude Code or Codex spend, token usage, or API cost, or asks whether they should switch models, compact, or change reasoning effort to save money.
---

# budgeteer: cost-saving advice from real usage

budgeteer reads the user's local session logs (`~/.claude/projects`, `~/.codex/sessions`), prices every API call, and ranks the changes that would save the most money. Prefer it over guessing: its figures come from what the user actually spent.

## How to use

Run the launcher shipped with this plugin (it finds or builds the binary on first use):

```bash
"${CLAUDE_PLUGIN_ROOT}/bin/budgeteer-run" suggest --format markdown --since 7d
```

Useful variations:

- `--since 30d` or `--since 2026-10-01` to change the window
- `--tool claude` or `--tool codex` to focus on one agent
- `--budget 200` to check the monthly run-rate against a budget
- `--format json` when you need to reason over exact numbers
- `summary` instead of `suggest` for a breakdown with no advice

## Reporting rules

- Quote only figures from the output. Costs are API-list-price equivalents; on a subscription plan (Pro/Max/Plus) the user is not billed per token, so frame savings as "usage headroom" in that case.
- Models marked as estimated use a family-default rate; say so when it matters, and point to `--pricing FILE` for overrides.
- Savings are already de-duplicated across recommendations (one dollar is never counted twice), so the totals can be added up.
- Offer to apply the top recommendation; do not change settings without being asked.
