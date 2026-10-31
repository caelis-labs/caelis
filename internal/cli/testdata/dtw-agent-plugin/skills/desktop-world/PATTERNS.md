# Common interaction patterns

Use these patterns with the API and reference lifecycle in [SKILL.md](SKILL.md). They apply to classes of desktop controls, independently of any particular application or task. Names, values, coordinates, window refs, and supported actions must come from the current UI and the user's task.

## File choosers and native dialogs

Opening a chooser may create a separate sheet, dialog, or related helper surface. After the opening action, use `app.waitForWindow({window_ref: parent, state:"dialog"})` when supported; DTW waits for the transition and returns the uniquely proven related modal ref. Retain parent and dialog refs separately. Unsupported or unproven relationships require fresh `app.windows()` and inspection; empty titles, list position, and newly focused windows do not establish a relationship.

Capture the dialog once to identify the filename/path field, directory contents, and confirmation controls. Large directory lists can dominate an AX/UIA traversal even when filtering for text fields. Query a semantic subtree or a capture region around the relevant controls before scanning the entire dialog. Regions are image coordinates; derive them from the current capture rather than a remembered layout.

On macOS, a native file chooser commonly supports **Command+Shift+G** to enter a directory path. This can open another sheet. Wait for its proven relationship before entering the path, then wait for that exact sheet to close before continuing in the chooser:

```javascript
await app.keypress({window_ref: state.chooser, keys:['META','SHIFT','G']});
state.path_sheet = (await app.waitForWindow({window_ref: state.chooser, state:'dialog'})).window_ref;
return await app.observe({window_ref: state.path_sheet, role:'text_field'});
```

Inspect the path field and its supported action. After confirming navigation once, use `waitForWindow({window_ref: state.path_sheet, state:'closed'})`, then reacquire chooser controls. The path-navigation field and filename field are different targets. Other platforms and custom choosers may use different controls; inspect their actual UI.

For a known filename field, `setValue` with a selector locates it afresh and sets it once, provided it advertises that action. Otherwise establish its editing focus using a current semantic action or capture-based click, then use the platform's select-all chord and `typeText`. DTW chooses delivery internally, including foreground fallback for native modal input. Do not assume a delivered keystroke updated the field: read or capture the value before confirming the chooser.

Confirm once after checking the selected path/name and the enabled control. If waiting for closure times out, the application may have opened a nested confirmation or directory-access dialog. Inspect a fresh capture or call `waitForWindow({window_ref: state.chooser, state:'dialog', timeout_ms:0})` to discover a proven related modal before new input; do not click the old confirmation again. Window waits refer to native surfaces: closing a document or tab can leave its containing window open, so inspect the resulting UI for those transitions. A closed dialog alone does not prove the requested save, open, or upload completed. Inspect the receiving application's resulting state. If confirmation has an unknown outcome, inspect that state and the original receipt before deciding what new work is needed; do not submit it again automatically.

## Forms and dynamic editing controls

Discover a form's controls with role/text filters and, when useful, a subtree or region. Select controls by the observed label, role, enabled state, and advertised action together. A text match can be a label rather than an editable field. If several candidates remain, inspect their container or geometry instead of choosing the first one.

`setValue` replaces a native value without promising ordinary typing/change events. `typeText` sends Unicode keyboard input to the selected editing control, without locating that control. On macOS, newline characters generate Return (CRLF once) and tabs generate Tab; rich editors may continue lists, indent text, or move focus. Verify the resulting content, including any automatic formatting. Choose the method supported by the current field, and verify the effect that matters to the application. Do not silently try both methods after an uncertain write.

A semantic press or shortcut can start an asynchronous UI transition. Delivery does not prove that the new editing control is ready or focused. For a known next control, selector actions resolve a unique target and wait while it is explicitly disabled, using reads only. `find` waits only for a unique match and reports its current `enabled` state; it can return a disabled control. For unstructured editors, observe or capture and establish editing focus before typing. Use `waitForWindow` for modal transitions. Do not repeatedly press or type as a waiting strategy.

Actions consume the target window's current semantic and pixel bindings. For a multi-field form, retain selectors and intended values; selector actions reacquire each target internally. Do not collect all element refs once and reuse them after earlier edits. Stable selectors and small JSON values can be stored in `state`; helper functions belong in the current script.

For toggles, inspect the actual state before pressing: pressing changes state rather than assigning a desired value. For multiline fields, pass real newline characters in the JavaScript string and let the MCP client's JSON serializer escape source once. Literal backslash-plus-`n` text is different from a newline.

For example, the JavaScript source `text: 'first line\nsecond line'` produces a newline in the text value. Let the client serialize that source as JSON; do not manually add another layer of backslash escaping.

Native value updates can be asynchronous. To wait for a short expected value, use `find` with the field role and that value as `text`, then compare its returned `element.value`; a role-only match can return the old value immediately. This only waits for native field state, not persistence or application validation.

After editing, check relevant field values, validation messages, and commit-control state. Commit once, then verify the application's resulting state. Tool delivery, script completion, a field displaying new text, and persisted application data are distinct evidence.

## Large trees and bounded discovery

For a long list or table, first look for the application's own search, filter, or page controls. Native `observe` filters the currently exposed accessibility tree; it does not load other application pages or materialize virtual rows. Use the application's controls to bring the relevant records into view before discovering their actions. Accessibility cursor pagination and application pagination are different operations.

`limit` limits returned matches, not native traversal work. Use the smallest relevant scope: a known container, a visible region from a current capture, or the app-menu scope. Add role/text filters based on observed controls. Scanning a full window repeatedly for each field is expensive, especially in long documents, directory views, and browser accessibility trees.

Generic pagination, using a window ref already selected into state:

```javascript
let page = await app.observe({
  window_ref: state.window, role: 'text_field', limit: 12
});
const elements = [];
for (;;) {
  elements.push(...page.elements);
  if (!page.next) break;
  page = await app.observe({cursor: page.next});
}
return {elements, coverage: page.coverage};
```

An empty page can still have `next`. Continue with `{cursor: next}` when further matches are needed. Keep continuation reads together in one script when practical; bindings have a lifetime and pagination does not renew it. Stop when the question is answered, or preserve the final coverage and its partial reasons. Do not infer absence from `next:null` alone.

If a broad query is incomplete or too costly, use a fresh capture to choose a narrower region, or obtain a container and query its subtree. Reacquire the target after scrolling or any action. A screenshot-backed coordinate action remains a valid route when semantics are unsupported or do not expose the necessary control; use current image coordinates and verify the result.

Some applications expose only window decorations while inactive. If a capture shows controls but semantic discovery returns only the window shell or `content_unproven`, try `focus` followed by `observe` in the same script when foreground delivery is permitted. This can reveal semantic controls without guessing coordinates. If they remain unavailable, use a fresh capture and preserve the partial coverage.

Return a compact decision record: relevant candidates or values, unresolved ambiguity, coverage reasons, and an image only when it helps the next decision. Keep traversal details and unrelated text out of the model response.

## Menus and focus-dependent interactions

For a known application-menu command, use `app.press({window_ref, selector:{scope:'menu',role:'menu_item',text:...}})`. DTW proves a unique match, prepares focus and resolves the menu again before pressing once. `app.find` with the same selector reads without focusing. Menu scope must be advertised by the provider; it does not search window controls or context menus.

Use `app.observe({window_ref,scope:'menu',...})` to explore the application's menu tree. For direct ref-based app-menu actions, establish focus and observe/act within the same script; app-menu actions apply to the focused window. An explicit `focus` does not reserve foreground focus for a later script.

Some applications defer a background semantic or coordinate action until their window becomes active. A delivered action with no visible effect may still be pending. First use `focus` and a fresh read in the same script, without repeating the action; the original effect may then appear. When the resulting state calls for new input, keep `focus` and the dependent actions in one script. This also applies to custom controls whose semantic actions are unavailable.

After an action opens a modal, use `waitForWindow` on the selected parent when supported; otherwise refresh inventory and inspect. DTW resolves target ownership, delivery, and foreground return internally. An unrelated pointer movement does not invalidate keyboard input, but a changed target or unproven focus requires fresh inspection. Keep unknown effects separate from new work, and never replay an earlier script merely because its state did not commit.
