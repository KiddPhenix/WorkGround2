//go:build !windows

package main

func setTaskbarBadge(int) error { return nil }
