package runhub

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestCoreHasNoDshOrDesktopImports guards the package boundary: runhub must not
// import runhub/dsh, Desktop, or any vendor adapter, or the transport-agnostic
// core would start to depend on a concrete runner.
func TestCoreHasNoDshOrDesktopImports(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)

	re := regexp.MustCompile(`"(workground2/internal/runhub/dsh|workground2/desktop)(/|")`)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if re.Match(data) {
			t.Errorf("%s imports runhub/dsh or Desktop", e.Name())
		}
	}
}
