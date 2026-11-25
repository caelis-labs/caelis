//go:build unix

package policyfs

import "golang.org/x/sys/unix"

// A special file substituted during open must not block before the identity check.
const ceilingOpenFlags = unix.O_NONBLOCK | unix.O_NOFOLLOW
