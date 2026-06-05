---
name: subagent-creator
description: Use only when the user explicitly asks to create or edit a reusable subagent markdown profile. Do not use for delegation, running/listing subagents, or configuring model bindings.
metadata:
  short-description: Create or edit a subagent
---

# Subagent Creator

Use this skill only to create or edit Markdown profile files in `~/.caelis/agents`.

## Workflow

1. Pick a stable lowercase id and write `~/.caelis/agents/<id>.md`.
2. If the file already exists, read it before editing and preserve the user's intent.
3. Use this profile shape:

```markdown
---
id: reviewer
name: Reviewer
description: Reviews code changes for defects, regressions, and missing validation.
capabilities: code-review, tests
---

Review the requested code change from a bug-finding stance.

Prioritize correctness bugs, regressions, security or data-loss risks, and missing validation.
Ground findings in concrete files, commands, tests, or observed behavior.
```

4. Keep instructions role-level. Do not include one-off task details, secrets, provider names, model aliases, binding choices, or runtime configuration.
5. Do not create or edit `guardian.md`; guardian is system-managed and is not a normal profile file.
6. After writing or editing a profile, tell the user to restart the TUI for discovery to take effect. The user can then run `/subagent list` to inspect the result.
