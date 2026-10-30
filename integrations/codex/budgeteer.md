---
description: Analyze recent Codex + Claude Code spend and suggest the best moves to cut cost
argument-hint: [--since 7d] [--tool claude|codex]
---

Run this command and read its output:

```bash
budgeteer suggest --format markdown $ARGUMENTS
```

If `budgeteer` is not installed: `go install github.com/rojinebrahimi/budgeteer/cmd/budgeteer@latest`.

Using only the figures in the report (never invent numbers): summarise what was spent and the trend in two sentences, list the top three moves with their expected savings and confidence, then ask which one to apply. Settings for Codex live in `~/.codex/config.toml` (`model`, `model_reasoning_effort`); change them only if the user agrees.
