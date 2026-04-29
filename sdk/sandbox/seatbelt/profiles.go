package seatbelt

// Seatbelt profile sections adapted from OpenAI's Codex project.
// These provide fine-grained macOS sandbox permissions that go beyond the
// system.sb import, enabling complex programs like Chrome/Playwright to run
// inside the sandbox without crashing.
//
// Reference:
//   codex-rs/core/src/seatbelt_base_policy.sbpl
//   codex-rs/core/src/seatbelt_platform_defaults.sbpl
//   codex-rs/core/src/seatbelt_network_policy.sbpl

// seatbeltCoreExtensions provides permissions for PTY, IPC, IOKit, and
// system calls that are missing from the base system.sb import but
// required by many development tools and browsers.
//
// Inspired by Chrome's own sandbox policy:
// https://source.chromium.org/chromium/chromium/src/+/main:sandbox/policy/mac/common.sb
const seatbeltCoreExtensions = `
; PTY support for interactive terminals
(allow pseudo-tty)
(allow file-read* file-write* file-ioctl (literal "/dev/ptmx"))
(allow file-read* file-write*
  (require-all
    (regex #"^/dev/ttys[0-9]+")
    (extension "com.apple.sandbox.pty")))
(allow file-ioctl (regex #"^/dev/ttys[0-9]+"))

; IPC semaphores (needed by Python multiprocessing, browsers, etc.)
(allow ipc-posix-sem)

; IOKit for hardware queries
(allow iokit-open (iokit-registry-entry-class "RootDomainUserClient"))

; Java/JVM CPU type detection (classified as write but is conceptually a read)
(allow sysctl-write (sysctl-name "kern.grade_cputype"))

; Allow guarded vnodes
(allow system-mac-syscall (mac-policy-name "vnguard"))

; Allow sandbox container determination
(allow system-mac-syscall
  (require-all
    (mac-policy-name "Sandbox")
    (mac-syscall-number 67)))

; Allow alternate chflags
(allow system-fsctl (fsctl-command FSIOC_CAS_BSDFLAGS))
`

// seatbeltMachServices lists macOS mach services required by development
// tools, browsers, and system libraries.  These enable logging, crash
// reporting, trust evaluation, notification delivery, preference access,
// and other system integration points.
const seatbeltMachServices = `
; System services required by browsers, development tools, and system libraries
(allow mach-lookup
  (global-name "com.apple.system.opendirectoryd.libinfo")
  (global-name "com.apple.system.opendirectoryd.membership")
  (global-name "com.apple.system.DirectoryService.libinfo_v1")
  (global-name "com.apple.system.notification_center")
  (global-name "com.apple.system.logger")
  (global-name "com.apple.PowerManagement.control")
  (global-name "com.apple.analyticsd")
  (global-name "com.apple.analyticsd.messagetracer")
  (global-name "com.apple.appsleep")
  (global-name "com.apple.bsd.dirhelper")
  (global-name "com.apple.cfprefsd.agent")
  (global-name "com.apple.cfprefsd.daemon")
  (global-name "com.apple.diagnosticd")
  (global-name "com.apple.dt.automationmode.reader")
  (global-name "com.apple.espd")
  (global-name "com.apple.logd")
  (global-name "com.apple.logd.events")
  (global-name "com.apple.runningboard")
  (global-name "com.apple.secinitd")
  (global-name "com.apple.trustd")
  (global-name "com.apple.trustd.agent")
  (global-name "com.apple.xpc.activity.unmanaged")
  (global-name "com.apple.audio.audiohald")
  (global-name "com.apple.audio.AudioComponentRegistrar")
  (local-name "com.apple.cfprefsd.agent"))

; macOS notification center shared memory
(allow ipc-posix-shm-read*
  (ipc-posix-name "apple.shm.notification_center"))

; Syslog socket for system logging (needed even without network access)
(allow network-outbound (literal "/private/var/run/syslog"))
`

// seatbeltDeviceAndFramework provides access to device files and system
// framework mapping required by compiled programs and browsers.
const seatbeltDeviceAndFramework = `
; Device file write access
(allow file-write-data
  (require-all (path "/dev/null") (vnode-type CHARACTER-DEVICE)))
(allow file-read* file-write* (literal "/dev/null"))
(allow file-read* file-write* (literal "/dev/zero"))
(allow file-read* file-write* (literal "/dev/tty"))
(allow file-read-data file-test-existence file-write-data (subpath "/dev/fd"))

; Debug/trace helper
(allow file-read* file-test-existence file-write-data file-ioctl
  (literal "/dev/dtracehelper"))

; Terminal device metadata
(allow file-read-metadata (literal "/dev"))
(allow file-read-metadata (regex "^/dev/.*$"))
(allow file-read-metadata (literal "/dev/stdin"))
(allow file-read-metadata (literal "/dev/stdout"))
(allow file-read-metadata (literal "/dev/stderr"))
(allow file-read-metadata (regex "^/dev/tty[^/]*$"))
(allow file-read-metadata (regex "^/dev/pty[^/]*$"))
(allow file-read* file-write* (regex "^/dev/ttys[0-9]+$"))
(allow file-read* file-write* (literal "/dev/ptmx"))
(allow file-ioctl (regex "^/dev/ttys[0-9]+$"))

; System framework dynamic library mapping
(allow file-map-executable
  (subpath "/Library/Apple/System/Library/Frameworks")
  (subpath "/Library/Apple/System/Library/PrivateFrameworks")
  (subpath "/Library/Apple/usr/lib")
  (subpath "/System/Library/Extensions")
  (subpath "/System/Library/Frameworks")
  (subpath "/System/Library/PrivateFrameworks")
  (subpath "/System/Library/SubFrameworks")
  (subpath "/System/iOSSupport/System/Library/Frameworks")
  (subpath "/System/iOSSupport/System/Library/PrivateFrameworks")
  (subpath "/System/iOSSupport/System/Library/SubFrameworks")
  (subpath "/usr/lib"))

; App sandbox extensions
(allow file-read* (extension "com.apple.app-sandbox.read"))
(allow file-read* file-write* (extension "com.apple.app-sandbox.read-write"))

; Read access to common system paths
(allow file-read* (subpath "/Library/Preferences"))
(allow file-read* (subpath "/opt/homebrew/lib"))
(allow file-read* (subpath "/usr/local/lib"))
(allow file-read* (subpath "/Applications"))

; Allow processes to get their current working directory
(allow file-read* file-test-existence (literal "/"))

; Firmlink metadata traversal
(allow file-read-metadata (literal "/System/Volumes") (vnode-type DIRECTORY))
(allow file-read-metadata (literal "/System/Volumes/Data") (vnode-type DIRECTORY))
(allow file-read-metadata (literal "/System/Volumes/Data/Users") (vnode-type DIRECTORY))
`

// seatbeltNetworkExtensions provides additional mach services and system
// socket permissions required when network access is enabled.  These enable
// TLS certificate validation, DNS resolution, and proxy configuration.
const seatbeltNetworkExtensions = `
; System socket for local platform services
(allow system-socket
  (require-all
    (socket-domain AF_SYSTEM)
    (socket-protocol 2)))

; Network-specific mach services for TLS, DNS, and system configuration
(allow mach-lookup
  (global-name "com.apple.SecurityServer")
  (global-name "com.apple.networkd")
  (global-name "com.apple.ocspd")
  (global-name "com.apple.SystemConfiguration.DNSConfiguration")
  (global-name "com.apple.SystemConfiguration.configd"))
`
