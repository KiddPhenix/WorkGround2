package assistanttool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/fileutil"
)

// ProjectConstraintFile is the structured project-constraints file name. It
// lives in the Assistant's allowed Workspace (or the store root when the
// Assistant is global scope); Assistant Memory never stores an overriding copy.
//
// Precedence: AGENTS.md / project memory remain the authoritative free-form
// project instruction source (human-edited); this structured file is the
// machine-writable, revision-tracked view of the same project constraints. It
// is only ever created/updated by project_constraints_update under a request
// receipt + revision CAS, never by the Assistant on its own authority.
const ProjectConstraintFile = "constraints.json"

// projectConstraints is the on-disk authoritative structured constraints record.
type projectConstraints struct {
	Revision    int64    `json:"revision"`
	Constraints []string `json:"constraints"`
}

type projectResult struct {
	Status      string   `json:"status"`
	Constraints []string `json:"constraints,omitempty"`
	Revision    int64    `json:"revision,omitempty"`
	Workspace   string   `json:"workspace,omitempty"`
	Source      string   `json:"source,omitempty"`
	VCS         string   `json:"vcs,omitempty"`
	Message     string   `json:"message,omitempty"`
}

func (r projectResult) String() string {
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Sprintf(`{"status":"retryable_error","message":%q}`, err.Error())
	}
	return string(b)
}

func projectError(status string, err error) string {
	return projectResult{Status: status, Message: err.Error()}.String()
}

type projectTool struct {
	store       *assistant.Store
	assistantID string
}

func newProjectTool(store *assistant.Store, assistantID string) *projectTool {
	return &projectTool{store: store, assistantID: assistantID}
}

// workspaceDir resolves the Assistant's allowed Workspace root, or the store
// root fallback when global scope. It is the boundary project tools may touch.
func (t *projectTool) workspaceDir() (string, error) {
	snap, err := t.store.Get(t.assistantID)
	if err != nil {
		return "", err
	}
	if snap.Assistant.Scope == assistant.ScopeWorkspace && strings.TrimSpace(snap.Assistant.WorkspaceRoot) != "" {
		return strings.TrimSpace(snap.Assistant.WorkspaceRoot), nil
	}
	return "", fmt.Errorf("assistant %q has no workspace; project constraints require a workspace-scoped assistant", t.assistantID)
}

// constraintPath resolves the authoritative constraint file path and rejects
// any escape: symlinks/reparse points in the workspace or at the file that
// resolve outside the workspace are refused.
func (t *projectTool) constraintPath() (string, error) {
	dir, err := t.workspaceDir()
	if err != nil {
		return "", err
	}
	return resolveWithin(dir, filepath.Join(".workground2", ProjectConstraintFile))
}

// resolveWithin resolves root's symlinks and the target under it, then verifies
// the resolved target is still inside root. A missing path resolves its closest
// existing ancestor and re-appends the tail.
func resolveWithin(root, rel string) (string, error) {
	root = filepath.Clean(root)
	realRoot, err := resolveExisting(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(realRoot, rel)
	if resolved, evalErr := filepath.EvalSymlinks(target); evalErr == nil {
		target = resolved
	}
	relPath, err := filepath.Rel(realRoot, target)
	if err != nil || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) || filepath.IsAbs(relPath) {
		return "", fmt.Errorf("constraint path escapes workspace %q", realRoot)
	}
	return target, nil
}

// resolveExisting resolves a possibly-missing path by EvalSymlinks-ing the
// closest existing ancestor and re-appending the missing tail.
func resolveExisting(path string) (string, error) {
	var missing []string
	cur := path
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
}

// lockConstraintFile takes an exclusive cross-process lock beside the constraint
// file. A crashed process leaves a lock file behind; a stale one (older than the
// stale threshold) is removed and retried so recovery is automatic.
func lockConstraintFile(path string) (func(), error) {
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(fmt.Sprintf("%d\n", os.Getpid()))
			_ = f.Sync()
			return func() {
				_ = f.Close()
				_ = os.Remove(lockPath)
			}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("project constraints lock timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// constraintFingerprint hashes the patch intent (constraints + expected
// revision) so a request ID reused with different input is detected.
func constraintFingerprint(constraints []string, expectedRevision int64) string {
	b, _ := json.Marshal(struct {
		Constraints []string `json:"constraints"`
		Expected    int64    `json:"expected"`
	}{constraints, expectedRevision})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// gitShortRev returns a bounded git head for a workspace, or "unknown".
func gitShortRev(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := osexec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	rev := strings.TrimSpace(string(out))
	if rev == "" || len(rev) > 40 {
		return "unknown"
	}
	return rev
}

func (t *projectTool) readConstraintsAt(path string) (projectConstraints, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return projectConstraints{Revision: 0, Constraints: []string{}}, false, nil
	}
	if err != nil {
		return projectConstraints{}, false, err
	}
	var c projectConstraints
	if err := json.Unmarshal(b, &c); err != nil {
		return projectConstraints{}, false, err
	}
	if c.Constraints == nil {
		c.Constraints = []string{}
	}
	return c, true, nil
}

// ---- project_status ---------------------------------------------------------

type projectStatusTool struct{ *projectTool }

// NewProjectStatusTool reads the Assistant's project status: workspace, the
// current plan (revision + responsibility count), the authoritative
// project-constraints source and revision, and a bounded VCS head.
func NewProjectStatusTool(store *assistant.Store, assistantID string) *projectStatusTool {
	return &projectStatusTool{projectTool: newProjectTool(store, assistantID)}
}

func (t *projectStatusTool) Name() string   { return "project_status" }
func (t *projectStatusTool) ReadOnly() bool { return true }

func (t *projectStatusTool) Description() string {
	return "Read the Assistant's project status: workspace root, VCS head, plan revision/responsibility count, and the authoritative project-constraints source + revision."
}

func (t *projectStatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
}

func (t *projectStatusTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	snap, err := t.store.Get(t.assistantID)
	if err != nil {
		return projectError("retryable_error", err), nil
	}
	ws := snap.Assistant.WorkspaceRoot
	if ws == "" {
		ws = "(global)"
	}
	var constraints projectConstraints
	source := "none"
	vcs := "unknown"
	if path, err := t.constraintPath(); err == nil {
		c, ok, err := t.readConstraintsAt(path)
		if err != nil {
			return projectError("retryable_error", err), nil
		}
		if ok {
			constraints, source = c, path
		}
		vcs = gitShortRev(filepath.Dir(filepath.Dir(path))) // workspace root
	} else if snap.Assistant.Scope == assistant.ScopeWorkspace {
		vcs = gitShortRev(strings.TrimSpace(snap.Assistant.WorkspaceRoot))
	}
	return projectResult{
		Status: "accepted", Workspace: ws, Source: source, VCS: vcs, Revision: constraints.Revision,
		Message: fmt.Sprintf("plan revision %d with %d responsibilities", snap.Plan.Revision, len(snap.Plan.Responsibilities)),
	}.String(), nil
}

// ---- project_constraints_get -------------------------------------------------

type projectConstraintsGetTool struct{ *projectTool }

// NewProjectConstraintsGetTool reads the authoritative project constraints.
func NewProjectConstraintsGetTool(store *assistant.Store, assistantID string) *projectConstraintsGetTool {
	return &projectConstraintsGetTool{projectTool: newProjectTool(store, assistantID)}
}

func (t *projectConstraintsGetTool) Name() string   { return "project_constraints_get" }
func (t *projectConstraintsGetTool) ReadOnly() bool { return true }

func (t *projectConstraintsGetTool) Description() string {
	return "Read the authoritative project constraints from the Assistant's workspace (not from Assistant Memory)."
}

func (t *projectConstraintsGetTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
}

func (t *projectConstraintsGetTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	path, err := t.constraintPath()
	if err != nil {
		return projectError(mapStoreError(err), err), nil
	}
	constraints, _, err := t.readConstraintsAt(path)
	if err != nil {
		return projectError(mapStoreError(err), err), nil
	}
	return projectResult{Status: "accepted", Constraints: constraints.Constraints, Revision: constraints.Revision, Source: path}.String(), nil
}

// ---- project_constraints_update ---------------------------------------------

type projectConstraintsUpdateTool struct {
	*projectTool
	name string
}

// NewProjectConstraintsUpdateTool replaces the authoritative project constraints
// under Policy gate, request receipt and revision CAS.
func NewProjectConstraintsUpdateTool(store *assistant.Store, assistantID string) *projectConstraintsUpdateTool {
	return &projectConstraintsUpdateTool{projectTool: newProjectTool(store, assistantID), name: "project_constraints_update"}
}

// NewProjectConstraintsPatchTool is the backward-compatible alias for the update
// tool; both share the same hardened implementation.
func NewProjectConstraintsPatchTool(store *assistant.Store, assistantID string) *projectConstraintsUpdateTool {
	return &projectConstraintsUpdateTool{projectTool: newProjectTool(store, assistantID), name: "project_constraints_patch"}
}

func (t *projectConstraintsUpdateTool) Name() string   { return t.name }
func (t *projectConstraintsUpdateTool) ReadOnly() bool { return false }

func (t *projectConstraintsUpdateTool) Description() string {
	return "Replace the authoritative project constraints. Pass request_id for replay safety and expected_revision to reject a stale edit; returns stale on revision conflict and already_applied on a replayed request_id."
}

func (t *projectConstraintsUpdateTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"constraints":{"type":"array","items":{"type":"string"}},"expected_revision":{"type":"integer"},"request_id":{"type":"string"}},"required":["constraints","request_id"]}`)
}

func (t *projectConstraintsUpdateTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Constraints      []string `json:"constraints"`
		ExpectedRevision int64    `json:"expected_revision"`
		RequestID        string   `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return projectError("invalid", err), nil
	}
	if strings.TrimSpace(in.RequestID) == "" {
		return projectError("invalid", fmt.Errorf("request_id is required")), nil
	}
	if in.Constraints == nil {
		in.Constraints = []string{}
	}

	// Policy gate: an Assistant whose policy denies constraint edits cannot
	// modify the project, regardless of what the model asks for.
	snap, err := t.store.Get(t.assistantID)
	if err != nil {
		return projectError(mapStoreError(err), err), nil
	}
	if snap.Assistant.Policy.ConstraintEdit == assistant.AccessDeny {
		return projectError("policy_denied", fmt.Errorf("assistant policy denies project constraint edits")), nil
	}

	path, err := t.constraintPath()
	if err != nil {
		return projectError(mapStoreError(err), err), nil
	}
	receiptDir := filepath.Dir(path)
	fp := constraintFingerprint(in.Constraints, in.ExpectedRevision)
	key := "project_constraints_update\x00" + in.RequestID

	// Durable request receipt BEFORE any write: a replayed request_id returns
	// already_applied, and a reused request_id with different input is a
	// conflict — never a silent second (or wrong) edit.
	if rec, ok, err := agent.ReadOpReceipt(receiptDir, key); err != nil {
		return projectError("retryable_error", err), nil
	} else if ok {
		if rec.Fingerprint != "" && rec.Fingerprint != fp {
			return projectError("conflict", &agent.OpReceiptConflictError{Key: key, Fingerprint: rec.Fingerprint}), nil
		}
		return alreadyAppliedResult(rec), nil
	}

	unlock, err := lockConstraintFile(path)
	if err != nil {
		return projectError("retryable_error", err), nil
	}
	defer unlock()

	current, _, err := t.readConstraintsAt(path)
	if err != nil {
		return projectError("retryable_error", err), nil
	}
	if current.Revision != in.ExpectedRevision {
		return projectError("stale", fmt.Errorf("expected_revision %d != current %d", in.ExpectedRevision, current.Revision)), nil
	}
	next := projectConstraints{Revision: current.Revision + 1, Constraints: in.Constraints}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return projectError("retryable_error", err), nil
	}
	b = append(b, '\n')
	if err := fileutil.AtomicWriteFile(path, b, 0o600); err != nil {
		return projectError("retryable_error", err), nil
	}

	result := projectResult{Status: "accepted", Constraints: next.Constraints, Revision: next.Revision, Source: path}
	rec, recorded, err := agent.WriteOpReceipt(receiptDir, key, agent.OpReceipt{
		Status: "accepted", Message: result.String(), Fingerprint: fp,
	})
	if err != nil {
		var conflict *agent.OpReceiptConflictError
		if errors.As(err, &conflict) {
			return projectError("conflict", err), nil
		}
		return projectError("retryable_error", err), nil
	}
	if !recorded {
		return alreadyAppliedResult(rec), nil
	}
	return result.String(), nil
}

// alreadyAppliedResult reconstructs a projectResult from a stored op receipt and
// marks it already_applied so a replayed request_id surfaces its prior outcome.
func alreadyAppliedResult(rec agent.OpReceipt) string {
	var r projectResult
	if err := json.Unmarshal([]byte(rec.Message), &r); err != nil {
		return projectResult{Status: "already_applied", Message: rec.Message}.String()
	}
	r.Status = "already_applied"
	return r.String()
}
