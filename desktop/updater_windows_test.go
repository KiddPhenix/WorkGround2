//go:build windows

package main

import "testing"

func TestInstallerCommandPassesUnquotedDFlagLast(t *testing.T) {
	cmd := installerCommand(`C:\Temp\WorkGround2-update-1.exe`, `D:\Tools\WorkGround2 App`)
	if cmd.SysProcAttr == nil {
		t.Fatal("expected a raw command line forcing the install dir")
	}
	got := cmd.SysProcAttr.CmdLine
	want := `"C:\Temp\WorkGround2-update-1.exe" /D=D:\Tools\WorkGround2 App`
	if got != want {
		t.Fatalf("CmdLine = %q, want %q", got, want)
	}
}

func TestInstallerCommandWithoutDirSkipsDFlag(t *testing.T) {
	cmd := installerCommand(`C:\Temp\WorkGround2-update-1.exe`, "")
	if cmd.SysProcAttr != nil {
		t.Fatalf("no dir should leave NSIS InstallDir logic intact, got CmdLine %q", cmd.SysProcAttr.CmdLine)
	}
}
