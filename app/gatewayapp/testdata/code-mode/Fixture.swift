import AppKit

let app = NSApplication.shared
app.setActivationPolicy(.regular)
let window = NSWindow(contentRect: NSRect(x: 200, y: 200, width: 620, height: 300), styleMask: [.titled, .closable], backing: .buffered, defer: false)
window.title = "Code Mode acceptance fixture"
let title = NSTextField(labelWithString: "Code Mode fixture — local synthetic data")
title.font = NSFont.systemFont(ofSize: 22)
title.frame = NSRect(x: 30, y: 210, width: 560, height: 45)
let data = NSTextField(labelWithString: "Orchard A: 17\nOrchard B: 25\nExpected total: 42")
data.font = NSFont.monospacedSystemFont(ofSize: 20, weight: .regular)
data.frame = NSRect(x: 30, y: 70, width: 540, height: 120)
window.contentView?.addSubview(title)
window.contentView?.addSubview(data)
window.center()
window.makeKeyAndOrderFront(nil)
app.activate(ignoringOtherApps: true)
app.run()
