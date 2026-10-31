---
name: desktop-world
description: DTW scripting and capability reference for app-scoped desktop computer use through standard MCP.
---

# Desktop World

For file choosers, forms, dynamic editing controls, and large semantic trees, read the relevant section of [Common interaction patterns](PATTERNS.md). It describes reusable interactions; obtain application-specific targets from current observations.

## MCP and script contract

The configured DTW server provides `apps`, `api`, `exec`, and `result`; clients may prefix these tool names. `apps` returns stable platform app IDs and advertised capabilities. `api` returns the exact schema and semantics of selected methods.

Call `exec` with exactly **`{app, source}`**. The app is its stable platform ID. Source is an async JavaScript function body with `app`, `state`, `await` and `return`. Serialize the source string once through ordinary JSON. There is no request ID, file path, delivery or approval argument.

One script addresses one app. Each app retains its own JSON `state` when switching apps. Await methods sequentially. Local functions, conditions and loops can compose methods; functions cannot be stored in state. State commits only when the script succeeds and never rolls back desktop effects.

Keep the selected window ref in `state` and group known steps in one script. Prefer selector-based `press`/`setValue` for known controls and `waitForWindow` for modal transitions; DTW handles fresh discovery and waiting internally. Use `observe` or `capture` when a new target, ambiguity, or visual decision needs inspection. A separate `exec` for every native method adds unnecessary round trips. Request `api` only for the methods whose exact schema you need.

Scripts have a 30-second deadline and no native-call quota. Imports, filesystem, network, timers and process APIs are unavailable. Return small JSON results and the images needed for the next decision. Omit undefined object fields or use null.

## Simple action workflow

`app.windows()` returns window refs across instances of the selected app. A ref routes to its exact instance; titles are descriptive and may be empty or non-unique. Select the intended window from returned metadata and observations, then keep its ref in `state`; another `windows()` call is not required before every action. Never construct a ref or substitute a PID or another app ID.

Window inventory is complete only within its declared scope. `association_complete:false` means not every possible helper relationship is established. Related surfaces use the same logical app ID and their own returned window refs. For a blocked target, use `waitForWindow` with `state:'dialog'` to discover a proven related modal when supported; inspect that returned window before new input. If the relationship remains unproven, use fresh inventory or report the boundary.

For a known control, use a selector instead of writing an observation/pagination loop. Selectors combine an exact returned `role` with case-insensitive `text` matching the label, name, or value; provide at least one. The default scope is the selected window; use `selector:{scope:'menu',role:'menu_item',text:...}` for its application menu. Use labels from the current UI:

```javascript
await app.setValue({
  window_ref: state.window,
  selector: {role: 'text_field', text: 'Name'}, value: 'Example'
});
await app.press({
  window_ref: state.window,
  selector: {role: 'button', text: 'Continue'}
});
```

DTW refreshes each target, handles pagination and briefly waits, then acts once. If an inactive application exposes only its window shell (`content_unproven`), selector actions may borrow focus once under the configured delivery policy and resolve afresh before sending input. For `scope:'menu'` actions, DTW first proves a unique match, prepares the selected window's focus and resolves the menu afresh; no separate `focus` call is needed. `find` remains a read. Optional `timeout_ms` defaults to 2000; zero disables polling and cold-window preparation, but retains the menu action's focus preparation. Multiple matches return candidates; incomplete coverage is unproven. Narrow or inspect those cases rather than taking the first match. `app.find({window_ref,selector,timeout_ms?})` waits for one unique element and returns its refs plus `element` metadata, useful for reading its current bounded value. Use `observe` for exploration and large trees requiring a smaller scope.

On supported providers, `app.waitForWindow({window_ref,state:'dialog'})` waits for a proven related modal and returns its own fresh `window_ref`. Use `state:'closed'` to wait for that exact dialog to disappear, or `state:'unblocked'` for the original window's modal gate to clear. It never follows arbitrary new or focused windows. Optional `timeout_ms` has the same default; condition timeout is a normal error and does not replay input. See the file-chooser pattern for nested dialogs.

## Images and discovery

`app.capture({window_ref})` returns an image and a pixel observation ref. Coordinates refer to that image, not desktop or CSS coordinates.

For pixel grounding, use the returned `image.width` and `image.height`, even if a viewer resizes the image. For 0–1000 grounding, pass `coordinate_space:"normalized_1000"` to `click`; DTW converts both axes through the bound capture. Both coordinates must be in the same space; normalized values are at least 0 and less than 1000. Do not manually rescale normalized coordinates and also mark them normalized.

`app.observe` supports these exclusive scopes, subject to `supported_observe_scopes`:

- Window: `{window_ref, role?, text?, limit?}`.
- Subtree: `{window_ref, observation_ref, element_ref, role?, text?, limit?}`, using a current semantic container.
- Region: `{window_ref, observation_ref, region:{x,y,width,height}, role?, text?, limit?}`, using a current capture.
- App menu: `{window_ref, scope:'menu', role?, text?, limit?}`.

Region queries filter native geometry. macOS clips through proven scroll-area viewports and skips disjoint scroll contents; Windows uses UIA bounding rectangles. Region discovery is not OCR or an occlusion guarantee. Capture support does not imply semantic support.

Each page contains `elements`, `observation_ref`, `next` and `coverage`. Element fields are `role,name,value,label,enabled,actions,element_ref,can_press`. Names, labels and values are bounded excerpts. Roles and advertised actions come from the provider; an intended task does not establish a control's name or capabilities.

`limit` bounds matches per page, not traversal work. Narrow the scope and filters when possible. Continue with only `{cursor:page.next}`; a cursor advances once. An empty page can have a continuation. Earlier-page refs share the same observation.

`next:null` does not imply complete discovery. `coverage.complete` describes the live traversal of the selected scope; it is not an atomic snapshot or proof that an element does not exist. Preserve partial-coverage reasons. Missing, disabled, ambiguous and unsupported are different outcomes.

## Atomic actions and text

- `press` also accepts `{window_ref,observation_ref,element_ref}` and requires a current semantic element advertising `press`.
- `setValue` also accepts `{window_ref,observation_ref,element_ref,value}` and requires advertised `setValue`. It replaces the native value and may not emit ordinary typing events.
- `typeText({window_ref,text})` sends Unicode keyboard input to the currently selected editing control. Application formatting can change the resulting text. It does not locate a field or establish which control has editing focus.
- `keypress({window_ref,keys:[...]})` presses and releases one chord. `META` is Command on macOS and the Windows key on Windows; modifier meanings are platform dependent.
- `click({window_ref,observation_ref,x,y,coordinate_space?,button?,count?})` uses a current capture. The default space is `image_pixels`; choose `normalized_1000` for normalized grounding. Buttons are left/right and counts are 1/2.
- `scroll({window_ref,delta_y,delta_x?})` uses wheel steps, positive down/right, at the window center. Supported platforms also accept a current capture's `observation_ref,x,y` or semantic `observation_ref,element_ref` as an anchor.
- `focus({window_ref})` raises the selected window. Its borrowed foreground lifetime belongs to the script; it does not establish persistent focus for a later script.

Some applications defer background actions until activated. If a delivered action has not appeared, use `focus` followed by `find`, `observe` or `capture` in the same script before any further input: activation can reveal the original action. For a window that needs foreground interaction, group `focus` and its dependent actions in one script. App-menu semantic actions also require this same-script focus. Desktop state changes can be asynchronous: a dispatched action does not establish readiness of a later target. Use selector resolution or `waitForWindow` for the relevant readiness condition; never repeat an action merely to wait.

`app.readText({window_ref,observation_ref,element_ref,offset?,limit?})` reads native text in UTF-16 pages without splitting surrogate pairs. Pass returned `next_offset` unchanged until null. A changed text revision requires a new observation; do not join pages from different revisions. Unsupported native text is explicit. Labels and screenshots are not substituted as native text. Return only the relevant text or verification result within the output budget.

## Reference lifecycle and recovery

Each window retains one semantic query and one capture. A new read replaces the same kind on that window; admitted semantic scope replacement uses newly returned refs. Actions consume both kinds on their target window. Other windows and apps retain their state and refs.

Semantic and pixel observations expire at most 15 seconds after acquisition; pagination does not extend that lifetime. Obtain a fresh binding before a later action. Invalid or unsupported scope admission preserves the current refs; a failed continuation retires its query.

On `stale_observation`, `target_changed`, `focus_changed` or `focus_unproven`, stop the affected sequence and inspect the target. Refresh windows if identity changed, or observe/capture the still-current target. Unrelated pointer movement does not invalidate keyboard input.

`context_reset:true` means the native context was replaced. Logical state can survive, but refs embedded in it must be reacquired through fresh inventory and observations. The original failed execution remains separate; do not replay its source automatically.

`dispatched` proves delivery and `completed` proves script execution, not the requested application effect. Verify the relevant native or visual state. Unknown or interrupted input may already have effects: retain the original receipt and inspect before recovery. JSON state rollback does not undo those effects.

## Results and platform conditions

Ordinary `exec` returns synchronously with a compact summary. After cancellation, an execution remains `running` while its cleanup is still settling; retain its original ID and inspect `result` before new work. Use `result` when the original ledger is needed, rather than polling after success. `result({app})` returns this connection's latest accepted execution; a returned execution ID selects that original record. An unreceived request may leave an older latest record. Sliding receipt expiry never permits replay.

macOS selects semantic, background or foreground delivery internally. The service's `auto` policy permits foreground fallback before dispatch when target delivery is unsupported; native modal input can require that route. `background_only` prohibits fallback and explicit focus. `foreground_required` is a service constraint, not an approval request.

Delivery metadata records the actual route and focus return. `restored` proves the original window, `application_restored` only the app, and `changed_externally` preserves a changed foreground. `unconfirmed` does not prove return; missing metadata means unavailable evidence. Windows/Linux input does not promise macOS focus-return behavior. Capabilities vary by platform; locked-screen operation is unsupported.

An `unconfirmed` focus return without a `shutdown_error` permits a fresh read of the target; inspect the original action receipt and current state before new work. It does not authorize replay of the earlier action. A `shutdown_error` indicates failed native cleanup and closes further execution in that session.
