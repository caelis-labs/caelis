---
name: review
description: Use when asked to review code changes, patches, diffs, pull requests, staged work, unstaged work, or workspace changes for correctness issues.
metadata:
  short-description: Review code changes
---

# Review

Use this skill for code review. Take a defect-finding stance and keep the response findings-first.

## Review Focus

Prioritize issues that can break users, regress behavior, violate boundaries, lose data, weaken security, or leave risky behavior untested.

Look for:

- Correctness bugs and behavioral regressions.
- Missing or misleading validation and tests.
- API, schema, persistence, replay, or protocol boundary mismatches.
- Concurrency, lifecycle, cancellation, error-handling, and cleanup mistakes.
- Security, privacy, permission, sandbox, and destructive-operation risks.

## Output

Start with findings, ordered by severity. Each finding should include concrete file and line references when available, the user-visible or system-visible impact, and the smallest practical fix direction.

Keep summaries brief and secondary. Include open questions only when they materially affect correctness. If no issues are found, say that clearly and mention any meaningful residual risk or unrun tests.

Do not default to JSON. Do not make code changes during the review unless explicitly asked.
