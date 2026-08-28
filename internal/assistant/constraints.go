package assistant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ProjectConstraintFile is the authoritative structured project-constraints
// file name under <workspace>/.workground2/. It is defined here so hosts can
// feed the supervisor's implicit context without importing the tool package
// (assistanttool imports assistant; the dependency points the other way).
const ProjectConstraintFile = "constraints.json"

// projectConstraints is the on-disk authoritative structured constraints record
// (kept byte-compatible with assistanttool's record).
type projectConstraints struct {
	Revision    int64    `json:"revision"`
	Constraints []string `json:"constraints"`
}

// LoadProjectConstraintsSummary reads the authoritative project constraints for
// a workspace and returns a bounded one-line summary plus the file revision. A
// missing file (or one that fails to parse) yields an empty summary with
// revision 0: the supervisor then simply has no structured constraints to show.
// It is a read-only view for the implicit context; the Assistant never keeps an
// overriding copy in Memory.
func LoadProjectConstraintsSummary(workspaceRoot string) (summary string, revision int64) {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return "", 0
	}
	path := filepath.Join(root, ".workground2", ProjectConstraintFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	var rec projectConstraints
	if err := json.Unmarshal(data, &rec); err != nil {
		return "", 0
	}
	if len(rec.Constraints) == 0 {
		return "", rec.Revision
	}
	return strings.Join(rec.Constraints, "；"), rec.Revision
}
