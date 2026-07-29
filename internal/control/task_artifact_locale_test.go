package control

import (
	"testing"

	"workground2/internal/artifact"
	"workground2/internal/work"
)

func TestArtifactFileNamePreservesLocalizedSlotTitle(t *testing.T) {
	slot := work.ArtifactSlot{ID: "final-report", Title: "最终报告.md"}
	if got := artifactFileName(slot, ".md"); got != "最终报告.md" {
		t.Fatalf("localized artifact name = %q, want %q", got, "最终报告.md")
	}
}

func TestArtifactFileNameStripsNewExtensions(t *testing.T) {
	tests := []struct {
		title, ext, want string
	}{
		{"报告.docx", ".pdf", "报告.pdf"},
		{"报告.pdf", ".docx", "报告.docx"},
		{"script.sh", ".ps1", "script.ps1"},
		{"run.bat", ".sh", "run.sh"},
		{"run.cmd", ".bat", "run.bat"},
		{"deploy.ps1", ".sh", "deploy.sh"},
		{"app.exe", ".zip", "app.zip"},
		{"archive.zip", ".7z", "archive.7z"},
		{"data.tar.gz", ".zip", "data.zip"},
		{"data.tar", ".zip", "data.zip"},
		{"FinalReport.TAR.GZ", ".pdf", "FinalReport.pdf"},
	}
	for _, tt := range tests {
		slot := work.ArtifactSlot{ID: "s", Title: tt.title}
		if got := artifactFileName(slot, tt.ext); got != tt.want {
			t.Errorf("artifactFileName(%q, %q) = %q, want %q", tt.title, tt.ext, got, tt.want)
		}
	}
}

func TestArtifactKindMatches(t *testing.T) {
	tests := []struct {
		name, wantKind string
		want           bool
	}{
		// Exact matches by SlotKind (which is derived from extension).
		{"report.docx", "docx", true},
		{"slides.pdf", "pdf", true},
		{"script.sh", "sh", true},
		{"build.bat", "bat", true},
		{"deploy.ps1", "ps1", true},
		{"tool.exe", "exe", true},
		{"bundle.zip", "zip", true},
		{"notes.md", "document", false}, // Discovered markdown is not a DOCX/PDF file.
		{"readme.txt", "text", false},   // SlotKind for .txt with no data is "file", and no text-kind extension check
		{"data.xlsx", "xlsx", true},

		// Old wide kind "document" accepts docx / pdf by extension.
		{"report.docx", "document", true},
		{"slides.pdf", "document", true},
		{"notes.md", "document", false}, // md is not doc/docx/pdf extension

		// Old wide kind "archive" accepts zip by extension.
		{"bundle.zip", "archive", true},
		{"bundle.7z", "archive", true},
		{"data.tar.gz", "archive", true},

		// Old wide kind "file" matches anything except image/video/audio.
		{"readme.txt", "file", true}, // .txt with no data → SlotKind "file"
		{"notes.md", "file", true},   // .md with no data → SlotKind "file"
		{"report.docx", "file", true},
		{"script.sh", "file", true},
		{"tool.exe", "file", true},
		{"photo.png", "file", false}, // image is excluded
		{"clip.mp4", "file", false},  // video is excluded

		// Old wide kind "data" matches csv/json/tsv/xml/yaml/yml/xlsx/xls by extension.
		{"data.csv", "data", true},
		{"data.json", "data", true},
		{"data.xlsx", "data", true},

		// Cross-kind mismatches.
		{"report.docx", "pdf", false},
		{"slides.pdf", "docx", false},
		{"script.sh", "bat", false},
		{"build.bat", "ps1", false},
		{"tool.exe", "zip", false},
		{"bundle.zip", "exe", false},
	}
	for _, tt := range tests {
		item := artifact.Discovered{Name: tt.name}
		if got := artifactKindMatches(item, tt.wantKind); got != tt.want {
			t.Errorf("artifactKindMatches({Name:%q}, %q) = %v, want %v", tt.name, tt.wantKind, got, tt.want)
		}
	}
}

func TestTakeArtifactsCompatibility(t *testing.T) {
	discovered := []artifact.Discovered{
		{Name: "report.docx"},
		{Name: "slides.pdf"},
		{Name: "script.sh"},
		{Name: "build.bat"},
		{Name: "deploy.ps1"},
		{Name: "tool.exe"},
		{Name: "bundle.zip"},
		{Name: "notes.md"},
		{Name: "data.xlsx"},
		{Name: "readme.txt"},
	}
	used := make(map[int]bool)

	// New specific kinds match exactly.
	if idx := takeArtifacts(discovered, used, "docx", 1); len(idx) != 1 || idx[0] != 0 {
		t.Fatalf("docx: got %v, want [0]", idx)
	}
	used[0] = true

	if idx := takeArtifacts(discovered, used, "pdf", 1); len(idx) != 1 || idx[0] != 1 {
		t.Fatalf("pdf: got %v, want [1]", idx)
	}
	used[1] = true

	// Old "document" kind matches docx and pdf subtypes.
	used2 := make(map[int]bool)
	docIdx := takeArtifacts(discovered, used2, "document", 2)
	if len(docIdx) != 2 {
		t.Fatalf("document (wide): got %v, want 2 matches", docIdx)
	}
	for _, i := range docIdx {
		if discovered[i].Name != "report.docx" && discovered[i].Name != "slides.pdf" {
			t.Errorf("document (wide): unexpected match %q", discovered[i].Name)
		}
	}

	// Old "archive" kind matches zip.
	used3 := make(map[int]bool)
	if idx := takeArtifacts(discovered, used3, "archive", 1); len(idx) != 1 || idx[0] != 6 {
		t.Fatalf("archive (wide): got %v, want [6]", idx)
	}

	// Old "file" kind matches everything.
	used4 := make(map[int]bool)
	if idx := takeArtifacts(discovered, used4, "file", 3); len(idx) != 3 {
		t.Fatalf("file (wide): got %v matches, want 3", len(idx))
	}

	// Old "data" kind matches xlsx.
	used5 := make(map[int]bool)
	if idx := takeArtifacts(discovered, used5, "data", 1); len(idx) != 1 || idx[0] != 8 {
		t.Fatalf("data (wide): got %v, want [8]", idx)
	}

	// "document" (wide) matches docx and pdf but not markdown.
	used6 := make(map[int]bool)
	docIdx2 := takeArtifacts(discovered, used6, "document", 2)
	if len(docIdx2) != 2 {
		t.Fatalf("document (wide) round 2: got %v, want 2 matches", docIdx2)
	}
	for _, i := range docIdx2 {
		if discovered[i].Name == "notes.md" {
			t.Errorf("document (wide): should not match notes.md")
		}
	}
}

func TestTextArtifactKindExcludesNewBinaryKinds(t *testing.T) {
	// New binary kinds must NOT be generatable from model plain text.
	binaryKinds := []string{"docx", "pdf", "sh", "bat", "ps1", "exe", "zip"}
	for _, k := range binaryKinds {
		if textArtifactKind(k) {
			t.Errorf("textArtifactKind(%q) = true, want false (must come from real discovered files)", k)
		}
	}
	// Existing text kinds still work.
	textKinds := []string{"text", "txt", "plain_text", "code", "markdown", "md", "document", "xlsx"}
	for _, k := range textKinds {
		if !textArtifactKind(k) {
			t.Errorf("textArtifactKind(%q) = false, want true", k)
		}
	}
}
