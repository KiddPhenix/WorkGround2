package work

import (
	"encoding/json"
	"testing"
)

func TestValidateArtifactURL(t *testing.T) {
	valid := []string{
		"https://example.com",
		"http://example.com",
		"https://example.com/path?a=1#frag",
		"https://example.com:8443/x",
		"HTTP://example.com",
		"https://user@example.com/x",
		"https://127.0.0.1:8080/health",
	}
	for _, raw := range valid {
		if !ValidateArtifactURL(raw) {
			t.Errorf("ValidateArtifactURL(%q) = false, want true", raw)
		}
	}

	invalid := []string{
		"",
		"   ",
		"javascript:alert(1)",
		"data:text/plain;base64,SGVsbG8=",
		"file:///etc/passwd",
		"ftp://example.com/file",
		"//example.com/path",
		"/relative/path",
		"example.com",
		"https://",
		"http://",
		"https:///path",
		"mailto:a@b.com",
		"vbscript:msgbox(1)",
	}
	for _, raw := range invalid {
		if ValidateArtifactURL(raw) {
			t.Errorf("ValidateArtifactURL(%q) = true, want false", raw)
		}
	}
}

func TestIsURLArtifactKind(t *testing.T) {
	for _, kind := range []string{"url", "link", "URL", "Link", " url ", "link"} {
		if !IsURLArtifactKind(kind) {
			t.Errorf("IsURLArtifactKind(%q) = false, want true", kind)
		}
	}
	for _, kind := range []string{"text", "document", "pdf", "xlsx", "file", "", "data"} {
		if IsURLArtifactKind(kind) {
			t.Errorf("IsURLArtifactKind(%q) = true, want false", kind)
		}
	}
}

func TestArtifactSlotEventRejectsUnsafeURL(t *testing.T) {
	payload, err := json.Marshal(ArtifactSlotUpdatedPayload{
		SlotID: "release", WorkID: "work-url", State: SlotReady, Revision: 1,
		Refs: []ArtifactRef{{ID: "ref-1", Status: ArtifactRefStatusAvailable, URL: "javascript:alert(1)"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateV2WorkEventPayload(EventArtifactSlotUpdated, payload); err == nil {
		t.Fatal("unsafe URL passed the durable event validation seam")
	}
}
