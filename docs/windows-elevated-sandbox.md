# Windows Elevated Sandbox Design

This legacy design is archived. It described the old Windows elevated sandbox
direction based on separate sandbox users, helper binaries, a command runner,
profile manipulation, and network firewall policy.

The active Windows sandbox implementation target is
[`windows-workspace-write-sandbox.md`](windows-workspace-write-sandbox.md). New
work should use the current-user workspace-write sandbox and must not recreate
the old elevated setup or runner path.
