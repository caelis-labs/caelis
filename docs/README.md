# Documentation Map

This directory is for current architecture and contract references.

Historical implementation plans, migration prompts, and audit notes are kept out
of the repository once the corresponding work has landed. The code and tests are
the source of truth for completed implementation details.

## Current References

- [architecture.md](architecture.md): current repository layout, entry flow, and
  ownership boundaries.

## Documentation Rules

- Keep docs tied to active code boundaries or durable product contracts.
- Do not commit one-off worker prompts, dated task plans, or completed migration
  checklists.
- If a design note becomes obsolete, fold the still-current rule into one of the
  current references above and delete the obsolete note.
