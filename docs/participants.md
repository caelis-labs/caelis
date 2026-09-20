# Participants

A participant is an addressable Agent conversation within a Caelis work Session.
The main controller, native collaborators running Caelis's built-in runtime,
and external ACP agents share one collaboration network. A participant keeps
its own conversation and can exchange messages with the controller and peers;
finishing a Turn does not prevent later follow-up work.

This guide covers configuration and the terminal workspace. The
[external ACP contract](external-acp-agents.md#input-and-collaborator-sessions)
owns mailbox delivery, authorization, and endpoint capability requirements.

## Configure collaborators

Use `/connect` to add a provider model or external ACP agent, then `/model` to
choose the main Session controller. External executables must already be on the
Host's `PATH`. The ACP catalog includes a **Custom** command for other stdio
agents; see [connection and endpoint setup](external-acp-agents.md#connect).

For Jev, choose **Connect a judgment model** in `/connect`, then `typesafe`,
the API key, and `jev-1.13.0`. This flow omits conversation-only controls.
The key uses the same managed credential store as other providers. Judgment
profiles appear in `/team` and `/disconnect`; they cannot become the main
Session model, a participant, Reviewer, or Memory Steward.

Open `/team` to configure the profiles available for collaboration:

- `self` uses the current Session controller's model, reasoning effort, Fast selection,
  and approval mode. With auto-review, the child's own Guardian handles its
  approvals. Full access is inherited from the shared Host. Manual approval
  continues through the ACP permission bridge.
- `breeze`, `orbit`, and `zenith` are named profiles that you bind to a provider
  model or ACP agent. Their descriptions help the controller select a fit for
  the work; the name itself does not select a model.
- Custom roles give another profile a stable handle and capability description.
- Binding sets save named snapshots of explicit profile bindings.

In the model picker, Tab and Shift+Tab cycle through the selected model's
Model, Effort, and Fast cells. Up/down selects a model and retains the focused
field when supported. Left/right adjusts effort (or speed while Fast is focused);
F toggles Fast. Fixed effort and unsupported Fast controls are skipped. Click a
model once or press Enter to apply; clicking effort or Fast edits the draft.
Escape discards the draft. Fast off is explicit standard speed. Binding sets
preserve independent profile Fast choices.

On the main list, Tab switches between a role's model and its auxiliary field.
A single click opens either selector. Left/right and F also update effort and
Fast directly on an explicitly bound model. Only the focused cell is highlighted;
the filled dot in the picker marks the currently applied model.

The same overlay includes fixed system roles; configuring a binding does not
start a conversation:

| Role | Binding behavior |
| --- | --- |
| ToolSearch | A judgment model ranks ready MCP tools in batches bounded by encoded input size. Unbound or unavailable evaluation retains lexical discovery. |
| Guardian | Uses a provider model, or the Main Agent model when unbound. An auxiliary classifier selector, labeled `Classifier`, appears on the same row when a judgment model is connected; it is off until selected. |
| Reviewer | Uses a provider model or ACP agent for the fixed review scene. |
| Memory Steward | An explicit generation model enables semantic organization. Unbound Memory keeps its durable journal and lexical recall without model calls. |
| Memory Verifier | An optional judgment model checks Steward proposals for clear semantic conflicts. The `Verifier` selector sits beside Memory Steward on the same row. It requires an enabled Steward and cannot generate or apply Memory changes. |

On Guardian's row, use Tab or click the main model or auxiliary classifier to
configure it. A selected classifier runs first, choosing only among the original
approval options. A decisive lead in approve versus deny probability mass settles
the approval; a classifier refusal has no generated explanation. Close or malformed
distributions, an oversized input, or a provider failure continue to Agent review
within the same approval deadline. Protocol-valid options whose kinds are not
supported by screening go directly to the Agent; option names and IDs are never
guessed. The Agent supplies a reason when its review rejects the action.
Screening uses only user messages and the current approval ticket with its exact
action. It receives no historical tool observations and never waits for Tasks.
Diagnostics distinguish input-budget, option-eligibility, provider, and
inconclusive-judgment fallbacks without recording request content. Changing or disabling screening preserves the Agent model selection. See [Guardian's evidence contract](agent-sdk-boundary.md#guardian-evidence-and-context)
and [Memory composition](architecture.md#session-runtime-lifecycle).

`/team` takes no arguments and opens the configuration overlay. The TUI accepts
`/subagent` as an alias; completion shows one `/team (subagent)` entry. Likewise,
`/quit` accepts `/exit` and appears as `/quit (exit)`. Built-in commands take
precedence over same-name Skills and custom roles. A conflicting role stays in
configuration with a name-conflict warning; rename it to use its slash command.

The product term **participant** describes the collaboration role; `subagent`
still names native execution contracts, SDK packages, and existing wire or
storage fields. Those technical names do not imply a separate messaging network.

## Coordinate work

Ask the main controller for a concrete division of work and shared outcome:

```text
Use two participants to investigate this failure: one should trace the code
path, the other should reproduce it with a focused test. Have them exchange
findings before you propose a fix. Do not edit production code yet.
```

The controller starts conversations with `StartThread` and reads their public
results with `ReadThread` or `WaitThread`. Each participant can use `ListThreads`
to discover the Session roster and `SendMessage` to send mail to another handle,
including the reserved controller address `parent`. Follow-up messages reuse the
same conversation rather than starting a new isolated task.

`ReadMessages` lets each Agent observe explicit group messages, including mail
addressed to other participants. The TUI shows only messages addressed to the
Agent in that pane: `parent` in the main transcript, or the selected participant
in a child pane. Successful reads with no matching messages leave no visible tool
row; failed reads remain visible. This display filter does not change model
context, read progress, or mail delivery. Outgoing `SendMessage` rows remain visible.

Only the controller can start participants or observe their public results
through thread tools. Participants receive discovery and messaging tools, not
creation or handoff authority. Their private reasoning and full transcripts do
not become the controller's model context merely because the TUI displays them.

Native tools and the external MCP bridge call the same mailbox service. A send
acknowledges queueing, not delivery or execution. Mail is consumed once, without
automatic retry or redelivery if dispatch or a tool response is lost. External
agents need to accept injected MCP tools to discover peers and send mail; running
input additionally depends on negotiated steering. Without steering, queued mail
can be collected through MCP during a Turn or submitted when the Agent is known
to be idle. See the [delivery contract](external-acp-agents.md#input-and-collaborator-sessions)
for the full guarantees and limits.

## Participant workspace

Click the running/done count in the footer or a participant's label in the
transcript to open one participant pane. Message rows split that click: the peer
label (`handle[agent]`, or `@handle[agent]` when you send) opens the peer's pane,
while the rest of the row expands or collapses the message in place. Long incoming
messages start collapsed in both the main transcript and participant panes. The name
dropdown switches participants;
the layout dropdown chooses **Overlay**, **Split left/right**, or **Split up/down**.
The dropdown lists Agents, not individual command Jobs.

Drag the divider to resize within 30–70%. It previews the new position; the
transcripts reflow once on release. Caelis remembers the layout and separate
horizontal/vertical ratios in the Host's UI preferences. Small terminals
use an overlay temporarily and restore the preferred split when space permits.

### Focus and layout

| Action | Control |
| --- | --- |
| Focus a composer | Click its pane |
| Switch composer focus | F6; Shift+F6 in reverse |
| Show or hide the participant pane | F7 |
| Open the pane and participant dropdown | Ctrl+G from either composer |
| Open the layout dropdown | Ctrl+L while the pane is open |
| Resize with the keyboard | Choose **Resize split**, then use arrows along the divider axis |

F6 also opens the selected participant when hidden. Both composers use the same
focus colors: brighter in dark themes, deeper in light themes. The participant
title also marks focus; both transcripts and progress hints remain readable.
Main-composer Tab completion and Shift+Tab mode switching keep their usual behavior.

Keyboard resizing previews five-percentage-point adjustments. Enter applies and
saves; Esc cancels. Terminal resizing cancels an unconfirmed resize preview.
F7, the footer's clickable **F7 Hide**, or the title's close button hides the pane
while the participant keeps working. The footer shows the short model ID on the
left and the latest ACP-reported context usage at the far right, with **F7 Hide**
immediately to its left. Agents that do not report context usage leave the gauge
empty.

### Send input

Enter sends a prompt to the selected participant. Shift+Enter or Ctrl+J adds a
line. Up recalls the last submitted prompt when that composer's draft is empty.
Drafts and scroll positions survive switching participants during the Session.

Click within the participant composer to position the cursor; drag text to copy
on release. Image paste uses the main composer's platform shortcut: Ctrl+V on
macOS/Linux, Ctrl+Alt+V on Windows/WSL. Images stay with the selected participant's
draft and are included when recalling its last prompt.

Esc dismisses a participant menu or selection; it does not hide the pane or
interrupt an Agent. Main-composer Esc retains its interruption behavior.
Model and context usage are read-only; the main controller retains orchestration.
Direct user input is separate from Agent mail and does not copy the prompt into
the main conversation. A queued input receipt is not proof that the model has
applied it; uncertain delivery outcomes are not automatically resent.
