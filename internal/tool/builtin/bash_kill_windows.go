package builtin

import (
	"os/exec"

	"workground2/internal/proc"
)

// setKillTree hides the child's console and makes a cancelled command kill its
// whole process tree. Windows does not cascade a kill to child processes, so
// killing the shell leaves `go test` and the binaries it spawned running after an
// Esc; taskkill /T walks the PID tree and /F forces it.
func setKillTree(cmd *exec.Cmd) {
	proc.HideWindow(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		proc.KillTree(cmd)
		return nil
	}
}

// reapTree is the post-completion process-group cleanup the POSIX build does for
// #3702. On Windows it's a no-op: once the shell leader exits there's no live
// parent for taskkill /T to walk, and spawning taskkill on every bash call would
// tax the hot path for little gain — proper after-exit tree cleanup needs a Job
// Object, which is out of scope here.
func reapTree(*exec.Cmd) {}
