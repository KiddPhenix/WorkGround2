package work

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReceiptCacheFileName_NoCollisions(t *testing.T) {
	// Distinct request IDs must never map to the same cache file name, even
	// when they differ only in characters that are illegal on Windows.
	ids := []string{
		"a:b", "a?b", "a*b", "a/b", `a\b`, "a<b", "a>b", "a|b",
		"a_b", "a:b:c", "run-1/input/x", "run-1?input?x",
		"token:abc", "token?abc",
	}
	seen := map[string]string{}
	for _, id := range ids {
		name := receiptCacheFileName(id)
		if prev, ok := seen[name]; ok {
			t.Fatalf("collision: %q and %q both map to %q", prev, id, name)
		}
		seen[name] = id
	}
}

func TestReceiptCacheFileName_RoundTrip(t *testing.T) {
	ids := []string{
		"submit-topic-plain",
		"submit-source-materials-test",
		"run-1/input/2:n1",
		"apply-rev3:step",
		"request?id=1&x=2",
		`back\slash:path`,
		"quote\"angle<>pipe|star*",
		"ctl\x01\x1fchar",
	}
	for _, id := range ids {
		name := receiptCacheFileName(id)
		if name == "" {
			t.Fatalf("empty cache name for %q", id)
		}
		// The name must be usable as a single file name on every platform:
		// no path separators, no Windows-invalid bytes, not a device name.
		if filepath.Base(name) != name {
			t.Fatalf("%q produced a path, not a file name: %q", id, name)
		}
		for _, r := range name {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("%q produced control byte %q in %q", id, r, name)
			}
		}
		// Distinct ids must stay distinct.
		for _, other := range ids {
			if other == id {
				continue
			}
			if receiptCacheFileName(other) == name {
				t.Fatalf("round-trip collision: %q and %q both map to %q", id, other, name)
			}
		}
	}
}

func TestReceiptCacheFileName_ReservedDeviceNames(t *testing.T) {
	for _, id := range []string{"CON", "con", "Con.txt", "PRN", "AUX", "NUL", "COM1", "LPT9", "COM1.json"} {
		name := receiptCacheFileName(id)
		if name == id+".json" {
			t.Fatalf("device name %q was not escaped: %q", id, name)
		}
	}
}

func TestReceiptCacheFileName_LegacySafeNamesUnchanged(t *testing.T) {
	// Sidecars written by older builds used requestID + ".json" verbatim for
	// safe request IDs; the encoding must keep those names identical so
	// existing receipts stay readable.
	for _, id := range []string{"submit-topic", "apply-rev3", "request-42", "a_b-c.d"} {
		if got := receiptCacheFileName(id); got != id+".json" {
			t.Fatalf("safe id %q encoded to %q, want %q", id, got, id+".json")
		}
	}
}

func TestReceiptCacheFileName_PersistsAndLoads(t *testing.T) {
	// End-to-end: store a receipt under a request ID with illegal characters,
	// then read it back through the same cache file name mapping.
	store := newTestFileWorkStore(t)
	workID := "w-receipt-cache"
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}
	requestID := "run-7/input:2"
	receipt := &InputIntentReceipt{
		RequestID: requestID, Operation: "SubmitInput", IntentDigest: "digest-1",
		InputID: "input-1", ResultRevision: 2,
	}
	if err := store.StoreInputReceipt(workID, receipt); err != nil {
		t.Fatalf("StoreInputReceipt: %v", err)
	}
	dir, _ := store.workPath(workID)
	path := filepath.Join(dir, inputReceiptSubDir, receiptCacheFileName(requestID))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("receipt sidecar not at encoded path %s: %v", path, err)
	}
	// Same request ID reads the same file back.
	loaded, err := store.LoadInputReceipt(workID, requestID)
	if err != nil {
		t.Fatalf("LoadInputReceipt: %v", err)
	}
	if loaded == nil || loaded.RequestID != requestID || loaded.IntentDigest != "digest-1" {
		t.Fatalf("loaded receipt = %+v", loaded)
	}
}
