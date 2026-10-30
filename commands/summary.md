---
description: Show a spend breakdown (tool, model, day, hotspots) without recommendations
argument-hint: "[--since 7d] [--tool claude|codex]"
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/bin/budgeteer-run:*)
---

!`"${CLAUDE_PLUGIN_ROOT}/bin/budgeteer-run" summary --format markdown $ARGUMENTS`

Present the report above as-is, then add one sentence on the single biggest cost driver. Do not invent figures.
