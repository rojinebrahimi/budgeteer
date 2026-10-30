---
description: Analyze your recent Claude Code + Codex spend and suggest the best moves to cut cost
argument-hint: "[--since 7d] [--tool claude|codex] [--budget USD]"
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/bin/budgeteer-run:*)
---

Local usage report, generated just now from the user's own session logs:

!`"${CLAUDE_PLUGIN_ROOT}/bin/budgeteer-run" suggest --format markdown $ARGUMENTS`

Using only the numbers in the report above (never invent figures):

1. In two sentences, say what was spent and whether it is trending up or down.
2. List the top three moves by expected savings. For each: the action, the dollar figure, and the confidence.
3. Ask which one the user wants to apply now. If they choose one, carry it out in this session when it is something you can do (for example switching model with the instructions given, or suggesting `/compact`), and otherwise show the exact setting to change.
