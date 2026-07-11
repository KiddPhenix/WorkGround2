package config

import "sync"

// userEditMu serializes in-process read-modify-write cycles on the user config
// file. LoadForEdit+SaveTo is not atomic: two concurrent editors each load,
// mutate their own copy, and save; the second save silently drops the first
// writer's fields.
//
// Cross-process writers can still race. Every runtime in-process editor should
// take this lock around its full LoadForEdit -> mutate -> SaveTo cycle.
var userEditMu sync.Mutex

// LockUserConfigEdits acquires the process-wide user-config edit lock and
// returns the unlock function. Do not hold it across slow non-config work or
// call another LockUserConfigEdits taker while holding it.
func LockUserConfigEdits() func() {
	userEditMu.Lock()
	return userEditMu.Unlock
}
