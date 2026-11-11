import AppKit

// Task-owned visual fixture: drawn content is not exposed as accessibility text.
final class FixtureView: NSView {
    override func draw(_ dirtyRect: NSRect) {
        NSColor.white.setFill()
        bounds.fill()
        let triangle = NSBezierPath()
        triangle.move(to: NSPoint(x: 300, y: 290))
        triangle.line(to: NSPoint(x: 190, y: 110))
        triangle.line(to: NSPoint(x: 410, y: 110))
        triangle.close()
        NSColor(red: 0, green: 0.65, blue: 0.65, alpha: 1).setFill()
        triangle.fill()
        let attributes: [NSAttributedString.Key: Any] = [.font: NSFont.boldSystemFont(ofSize: 44), .foregroundColor: NSColor.black]
        ("ORBIT 582" as NSString).draw(at: NSPoint(x: 170, y: 35), withAttributes: attributes)
    }
}
let app = NSApplication.shared
app.setActivationPolicy(.regular)
let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 600, height: 360), styleMask: [.titled, .closable], backing: .buffered, defer: false)
window.title = "CodeModeFixture - read-only acceptance"
window.contentView = FixtureView(frame: NSRect(x: 0, y: 0, width: 600, height: 360))
window.center()
window.makeKeyAndOrderFront(nil)
app.activate(ignoringOtherApps: true)
app.run()
