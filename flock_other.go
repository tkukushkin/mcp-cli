//go:build !unix

package main

import "os"

// ponytail: no locking off unix — Windows has no flock, and codex holds these locks only for
// the file access itself. LockFileEx through golang.org/x/sys/windows is the upgrade path if
// a refresh is ever seen to interleave with codex's own write.
func lockFile(*os.File) error { return nil }

// tryLockFile always succeeds for the same reason: with no lock to take, there is no contender
// to wait for either, so the refresh transaction runs unserialized against another codex.
func tryLockFile(*os.File) (bool, error) { return true, nil }
