package main

import "testing"

func TestTaskbarBadgeLabel(t *testing.T) {
	tests := []struct {
		count int
		want  string
	}{
		{count: -1, want: ""},
		{count: 0, want: ""},
		{count: 4, want: "4"},
		{count: 99, want: "99"},
		{count: 100, want: "99+"},
	}
	for _, test := range tests {
		if got := taskbarBadgeLabel(test.count); got != test.want {
			t.Fatalf("taskbarBadgeLabel(%d) = %q, want %q", test.count, got, test.want)
		}
	}
}
