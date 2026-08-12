package cdp

import (
	"errors"
	"testing"

	"workground2/internal/browser"
)

func TestValidateNavigationURL(t *testing.T) {
	for _, allowed := range []string{"about:blank", "http://localhost:8080/a", "https://example.com/path?q=1"} {
		if err := validateNavigationURL(allowed); err != nil {
			t.Fatalf("allowed URL %q rejected: %v", allowed, err)
		}
	}
	for _, blocked := range []string{"", "relative", "file:///tmp/a", "data:text/plain,x", "https://user:pass@example.com"} {
		if err := validateNavigationURL(blocked); err == nil {
			t.Fatalf("blocked URL %q accepted", blocked)
		}
	}
}

func TestValidateNavigationURLErrorCodes(t *testing.T) {
	tests := []struct {
		url  string
		code browser.ErrorCode
	}{
		{"relative", browser.ErrInvalidURL},
		{"file:///tmp/a", browser.ErrUnsupportedScheme},
	}
	for _, tc := range tests {
		var got *browser.Error
		if err := validateNavigationURL(tc.url); !errors.As(err, &got) || got.Code != tc.code {
			t.Fatalf("%q error=%v want code=%s", tc.url, err, tc.code)
		}
	}
}

func TestReplacementTab(t *testing.T) {
	tabs := []browser.TabInfo{{ID: "one"}, {ID: "two"}, {ID: "three"}}
	got, err := replacementTab(tabs, "two")
	if err != nil || got != "one" {
		t.Fatalf("replacement=%q err=%v", got, err)
	}
	if _, err := replacementTab(tabs[:1], "one"); err == nil {
		t.Fatal("last tab close should fail")
	}
}

func TestTabPostDispatchFailuresAreOutcomeUnknown(t *testing.T) {
	err := dispatchedError("target created but activation failed", errors.New("context canceled"))
	var dispatched *browser.DispatchError
	if !errors.As(err, &dispatched) || !dispatched.Dispatched {
		t.Fatalf("post-dispatch tab error = %v", err)
	}
}
