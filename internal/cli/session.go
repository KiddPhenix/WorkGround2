package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"workground2/internal/agent"
	sess "workground2/internal/session"
)

func sessionCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: workground2 session <subcommand>")
		fmt.Fprintln(os.Stderr, "  list       list saved sessions")
		fmt.Fprintln(os.Stderr, "  show       show a session's content")
		fmt.Fprintln(os.Stderr, "  rename     rename a session")
		fmt.Fprintln(os.Stderr, "  delete     move a session to trash")
		fmt.Fprintln(os.Stderr, "  trash      list trashed sessions")
		fmt.Fprintln(os.Stderr, "  restore    restore a trashed session")
		fmt.Fprintln(os.Stderr, "  purge      permanently delete a trashed session")
		return 2
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "list":
		return sessionList(rest)
	case "show":
		return sessionShow(rest)
	case "rename":
		return sessionRename(rest)
	case "delete", "rm":
		return sessionDelete(rest)
	case "trash":
		return sessionTrash(rest)
	case "restore":
		return sessionRestore(rest)
	case "purge":
		return sessionPurge(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown session subcommand: %s\n", sub)
		return 2
	}
}

func sessionDir(flagDir string) string {
	if flagDir != "" {
		return flagDir
	}
	return resolveCLISessionDir()
}

// resolveTrashedPath resolves a trashed session path from either a full path
// or just a basename. It scans the .trash/ directory looking for a match.
func resolveTrashedPath(dir, path string) (string, error) {
	// If it looks like a full path inside .trash/, try it directly.
	if strings.Contains(filepath.ToSlash(path), "/.trash/") {
		if _, _, _, err := sess.ValidateTrashedPath(dir, path); err == nil {
			return path, nil
		}
	}
	// Otherwise treat it as a basename and search the trash.
	basename := filepath.Base(path)
	trashed, err := sess.ListTrashed(dir)
	if err != nil {
		return "", err
	}
	for _, tp := range trashed {
		if filepath.Base(tp) == basename {
			return tp, nil
		}
	}
	return "", fmt.Errorf("session not found in trash: %s", basename)
}

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------

func sessionList(args []string) int {
	fs := flag.NewFlagSet("session list", flag.ContinueOnError)
	dirFlag := fs.String("dir", "", "session directory (default: auto-detect)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir := sessionDir(*dirFlag)
	infos, err := agent.ListSessions(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list sessions: %v\n", err)
		return 1
	}
	if len(infos) == 0 {
		fmt.Println("(no sessions)")
		return 0
	}

	titles, _ := sess.LoadTitles(dir)

	// Newest first.
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].LastActivityAt.After(infos[j].LastActivityAt)
	})

	fmt.Printf("%-50s  %6s  %-20s  %s\n", "PATH", "TURNS", "LAST ACTIVITY", "TITLE")
	for _, info := range infos {
		base := filepath.Base(info.Path)
		title := titles[base]
		if title == "" {
			title = info.Preview
		}
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		ts := info.LastActivityAt.Format(time.RFC3339)
		fmt.Printf("%-50s  %6d  %-20s  %s\n", base, info.Turns, ts, title)
	}
	return 0
}

// ---------------------------------------------------------------------------
// show
// ---------------------------------------------------------------------------

func sessionShow(args []string) int {
	fs := flag.NewFlagSet("session show", flag.ContinueOnError)
	dirFlag := fs.String("dir", "", "session directory (default: auto-detect)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: workground2 session show <path>")
		return 2
	}

	dir := sessionDir(*dirFlag)
	path := fs.Arg(0)

	absPath, _, err := sess.ValidatePath(dir, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid session path: %v\n", err)
		return 1
	}

	session, err := agent.LoadSession(absPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load session: %v\n", err)
		return 1
	}

	for i, msg := range session.Messages {
		role := strings.ToUpper(string(msg.Role))
		content := msg.Content
		// Trim long content for display.
		const maxLen = 2000
		if len(content) > maxLen {
			content = content[:maxLen] + "\n... (truncated)"
		}
		fmt.Printf("[%d] %s\n%s\n\n", i+1, role, content)
	}
	return 0
}

// ---------------------------------------------------------------------------
// rename
// ---------------------------------------------------------------------------

func sessionRename(args []string) int {
	fs := flag.NewFlagSet("session rename", flag.ContinueOnError)
	dirFlag := fs.String("dir", "", "session directory (default: auto-detect)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "Usage: workground2 session rename <path> <title>")
		return 2
	}

	dir := sessionDir(*dirFlag)
	path := fs.Arg(0)
	title := strings.Join(fs.Args()[1:], " ")

	if err := sess.SetTitle(dir, path, title); err != nil {
		fmt.Fprintf(os.Stderr, "rename session: %v\n", err)
		return 1
	}
	fmt.Printf("Renamed %s → %q\n", filepath.Base(path), title)
	return 0
}

// ---------------------------------------------------------------------------
// delete (soft-delete to trash)
// ---------------------------------------------------------------------------

func sessionDelete(args []string) int {
	fs := flag.NewFlagSet("session delete", flag.ContinueOnError)
	dirFlag := fs.String("dir", "", "session directory (default: auto-detect)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: workground2 session delete <path>")
		return 2
	}

	dir := sessionDir(*dirFlag)
	path := fs.Arg(0)

	if err := sess.TrashSession(dir, path); err != nil {
		fmt.Fprintf(os.Stderr, "delete session: %v\n", err)
		return 1
	}
	fmt.Printf("Moved %s to trash\n", filepath.Base(path))
	return 0
}

// ---------------------------------------------------------------------------
// trash (list trashed)
// ---------------------------------------------------------------------------

func sessionTrash(args []string) int {
	fs := flag.NewFlagSet("session trash", flag.ContinueOnError)
	dirFlag := fs.String("dir", "", "session directory (default: auto-detect)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir := sessionDir(*dirFlag)
	paths, err := sess.ListTrashed(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list trash: %v\n", err)
		return 1
	}
	if len(paths) == 0 {
		fmt.Println("(trash is empty)")
		return 0
	}

	titles, _ := sess.LoadTitles(dir)

	fmt.Printf("%-50s  %-24s  %s\n", "PATH", "DELETED AT", "TITLE")
	for _, path := range paths {
		base := filepath.Base(path)
		title := titles[base]
		deletedAt := sess.TrashedDeletedAt(path)
		ts := "unknown"
		if deletedAt > 0 {
			ts = time.UnixMilli(deletedAt).Format(time.RFC3339)
		}
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		fmt.Printf("%-50s  %-24s  %s\n", base, ts, title)
	}
	return 0
}

// ---------------------------------------------------------------------------
// restore
// ---------------------------------------------------------------------------

func sessionRestore(args []string) int {
	fs := flag.NewFlagSet("session restore", flag.ContinueOnError)
	dirFlag := fs.String("dir", "", "session directory (default: auto-detect)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: workground2 session restore <path>")
		return 2
	}

	dir := sessionDir(*dirFlag)
	path := fs.Arg(0)

	resolved, err := resolveTrashedPath(dir, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "restore session: %v\n", err)
		return 1
	}

	if err := sess.RestoreTrashedSession(dir, resolved); err != nil {
		fmt.Fprintf(os.Stderr, "restore session: %v\n", err)
		return 1
	}
	fmt.Printf("Restored %s from trash\n", filepath.Base(path))
	return 0
}

// ---------------------------------------------------------------------------
// purge
// ---------------------------------------------------------------------------

func sessionPurge(args []string) int {
	fs := flag.NewFlagSet("session purge", flag.ContinueOnError)
	dirFlag := fs.String("dir", "", "session directory (default: auto-detect)")
	force := fs.Bool("force", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: workground2 session purge <path>")
		return 2
	}

	dir := sessionDir(*dirFlag)
	path := fs.Arg(0)

	resolved, err := resolveTrashedPath(dir, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "purge session: %v\n", err)
		return 1
	}

	if !*force {
		fmt.Printf("Permanently delete %s? [y/N] ", filepath.Base(path))
		var answer string
		fmt.Scanln(&answer)
		if strings.ToLower(answer) != "y" && strings.ToLower(answer) != "yes" {
			fmt.Println("aborted")
			return 0
		}
	}

	if err := sess.PurgeTrashedSession(dir, resolved); err != nil {
		fmt.Fprintf(os.Stderr, "purge session: %v\n", err)
		return 1
	}
	fmt.Printf("Permanently deleted %s\n", filepath.Base(path))
	return 0
}
