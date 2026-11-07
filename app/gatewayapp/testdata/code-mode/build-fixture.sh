#!/bin/sh
set -eu
root="${1:?task-owned output directory required}"
app="$root/CodeModeFixture.app"
mkdir -p "$app/Contents/MacOS"
xcrun swiftc "$(dirname "$0")/Fixture.swift" -framework AppKit -o "$app/Contents/MacOS/CodeModeFixture"
cat > "$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>dev.caelis.codemode.fixture</string><key>CFBundleName</key><string>CodeModeFixture</string><key>CFBundleExecutable</key><string>CodeModeFixture</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>
PLIST
/usr/bin/open -n "$app"
