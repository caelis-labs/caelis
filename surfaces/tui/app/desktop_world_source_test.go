package tuiapp

import (
	"fmt"
	"strings"
	"testing"
)

func TestDesktopWorldScriptSummaries(t *testing.T) {
	for _, test := range []struct {
		name, source, want string
	}{
		{"pinned API look and capture", `const first = await app.look({full:true}); return await app.capture();`, "Chrome · Inspect the window and take a screenshot"},
		{"read selector text", `return app.look({role:"button",text:"Next"});`, "Chrome · Look for “Next”"},
		{"click and keypress take precedence over reads", desktopTraceSource, "Chrome · Click “New tab” and press META+L"},
		{"single quoted target shorthand", `return await app.click('Continue');`, "Chrome · Click “Continue”"},
		{"comments and bracket method", `return app /* host */ ['click']({"target":"w1.e12"});`, "Chrome · Click the observed control"},
		{"selector template", "await app.click({selector:{text:`New Tab`}})", "Chrome · Click “New Tab”"},
		{"coordinate click", `await app.click({x:21,y:33});`, "Chrome · Click"},
		{"chord array shorthand", `await app.keypress(['META','A']);`, "Chrome · Press META+A"},
		{"chord string shorthand", `await app.keypress("META+A");`, "Chrome · Press META+A"},
		{"top-level literal chord", `const chord = ['META','A']; await app.keypress({keys:chord});`, "Chrome · Run a script"},
		{"shadowed chord", `const chord = ['META','A']; { const chord = dynamic; await app.keypress({keys:chord}); }`, "Chrome · Run a script"},
		{"mutated chord", `const chord = ['META','A']; chord[1] = 'Z'; await app.keypress(chord);`, "Chrome · Run a script"},
		{"escaped chord", `const chord = ['META','A']; change({chord}); await app.keypress(chord);`, "Chrome · Run a script"},
		{"read text", `return app.readText({target:"w1.e12",limit:10});`, "Chrome · Read text from the observed control"},
		{"multiple continuous operations", `await app.focus(); await app.click("Continue"); await app.quit();`, "Chrome · Focus the window and click “Continue”"},
		{"duplicates do not become counts", `await app.click("A"); await app.click("A"); await app.click("B");`, "Chrome · Click “A” and click “B”"},
		{"strings comments and regex are not calls", `// app.quit()
const fake = "app.scroll()";
const pattern = /app\.keypress\(/;
/* app.focus(); */
return await app.look();
`, "Chrome · Inspect the window"},
		{"source map is ignored", "await app.look();\n//# sourceMappingURL=file:///NONEXISTENT", "Chrome · Inspect the window"},
		{"conditional calls", `if (ready) { await app.click("Next"); } else { await app.keypress("ESC"); }`, "Chrome · Run a script"},
		{"literal dead branch", `if (false) await app.quit(); await app.look({text:"order number"});`, "Chrome · Run a script"},
		{"early return", `await app.look(); return 42; await app.keypress("ESC");`, "Chrome · Inspect the window"},
		{"return expression", `return await app.click("Continue"); await app.keypress("ESC");`, "Chrome · Click “Continue”"},
		{"late shadow affects earlier app", `await app.click("Continue"); return; const app = {};`, "Chrome · Run a script"},
		{"B04 nested await in argument", `await app.keypress({keys:(await app.click("Continue"), ["META","A"])});`, "Chrome · Run a script"},
		{"B05 inactive class initializer", `class Unused { effect = app.keypress({keys:["META","A"]}); } await app.click("Continue");`, "Chrome · Run a script"},
		{"return expression still runs", `{ return await app.click("Continue"); } await app.keypress("ESC");`, "Chrome · Run a script"},
		{"finally runs before return", `try { return 7; } finally { await app.look(); } await app.keypress("ESC");`, "Chrome · Run a script"},
		{"throw skips the rest of its block", `try { throw new Error(); await app.keypress("ESC"); } catch { await app.capture(); }`, "Chrome · Run a script"},
		{"all branches terminate", `if (ready) return await app.look(); else return 0; await app.quit();`, "Chrome · Run a script"},
		{"loop is one lexical site", `for (const row of rows) await app.click(row.name);`, "Chrome · Run a script"},
		{"short circuit", `ready && await app.click("OK");`, "Chrome · Run a script"},
		{"optional call", `await app?.click("OK");`, "Chrome · Run a script"},
		{"dynamic target", "await app.click({selector:{text:`tab ${index}`}});", "Chrome · Run a script"},
		{"spread can replace literal target", `await app.click({target:"A", ...options});`, "Chrome · Run a script"},
		{"dynamic method", `await app.look(); await app[method](arg);`, "Chrome · Run a script"},
		{"hoisted app shadows a direct call", `if (false) { var app; } await app.click('C');`, "Chrome · Run a script"},
		{"shadowed app", `const app = other; await app.click('C');`, "Chrome · Run a script"},
		{"app alias", `const target = app; await target.click('C');`, "Chrome · Run a script"},
		{"invalid JavaScript", `await app.click("unterminated`, "Chrome · Run a script"},
		{"opaque script stays opaque", `throw new Error("app.quit()");`, "Chrome · Run a script"},
		{"sensitive value is only in disclosure", `await app.setValue({selector:{text:"Password"},value:"hidden"});`, "Chrome · Set the value of “Password”"},
		{"typed text is only in disclosure", `await app.typeText("hidden");`, "Chrome · Type text"},
	} {
		t.Run(test.name, func(t *testing.T) {
			display := desktopWorldDisplay{app: "Chrome", actions: desktopWorldScriptActions(test.source)}
			if got := display.label(56, false, false); got != test.want {
				t.Fatalf("summary = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDesktopWorldSummaryDisplayColumns(t *testing.T) {
	cases := []desktopWorldDisplay{
		{app: "Chrome", actions: desktopWorldScriptActions(`await app.click("非常長い🧑‍💻".repeat(30)); await app.keypress("ESC");`)},
		{app: strings.Repeat("👩🏽‍💻漢é", 30), actions: desktopWorldScriptActions(fmt.Sprintf("await app.click(%q); await app.keypress('ESC');", strings.Repeat("页面", 40)))},
		{search: true, query: strings.Repeat("应用 🧑‍💻 ", 32)},
	}
	for _, display := range cases {
		for _, width := range []int{5, 12, 18, 22, 38, 56} {
			for _, done := range []bool{false, true} {
				if got := display.label(width, false, done); displayColumns(got) > width || strings.ContainsAny(got, "\r\n") {
					t.Fatalf("width %d, done=%v, got %d cells in %q", width, done, displayColumns(got), got)
				}
			}
		}
	}
	display := desktopWorldDisplay{app: "Chrome", actions: desktopWorldScriptActions(`await app.click("Really long menu item name"); await app.keypress("ESC");`)}
	got := display.label(18, false, false)
	if !strings.Contains(got, "Ch") || !strings.Contains(got, "Click") || !strings.Contains(got, "press") {
		t.Fatalf("narrow summary loses app or principal actions: %q", got)
	}
}
