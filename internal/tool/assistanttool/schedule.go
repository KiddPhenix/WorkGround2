package assistanttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"workground2/internal/assistant"
)

// scheduleResult is the bounded, structured output shared by every schedule
// write. Status is one of accepted / already_applied / stale / invalid /
// retryable_error / blocked_by_policy and is always accompanied by the current
// routine and revision so a lost reply can be replayed safely.
type scheduleResult struct {
	Status   string              `json:"status"`
	Routine  *assistant.Routine  `json:"routine,omitempty"`
	Routines []assistant.Routine `json:"routines,omitempty"`
	Run      *assistant.Run      `json:"run,omitempty"`
	Revision int64               `json:"revision,omitempty"`
	Message  string              `json:"message,omitempty"`
}

func (r scheduleResult) String() string {
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Sprintf(`{"status":"retryable_error","message":%q}`, err.Error())
	}
	return string(b)
}

func scheduleError(status string, err error) string {
	return scheduleResult{Status: status, Message: err.Error()}.String()
}

// scheduleScheduleArgs is the wire form of an assistant.Schedule accepted by the
// create/update tools. Kind and interval_seconds are required; weekday, day and
// month are optional integers matching time.Weekday / time.Month.
type scheduleScheduleArgs struct {
	Kind            string `json:"kind"`
	IntervalSeconds int64  `json:"interval_seconds,omitempty"`
	Timezone        string `json:"timezone,omitempty"`
	At              string `json:"at,omitempty"`
	Weekday         int    `json:"weekday,omitempty"`
	Day             int    `json:"day,omitempty"`
	Month           int    `json:"month,omitempty"`
}

func (s scheduleScheduleArgs) schedule() assistant.Schedule {
	return assistant.Schedule{
		Kind:            assistant.ScheduleKind(s.Kind),
		IntervalSeconds: s.IntervalSeconds,
		Timezone:        s.Timezone,
		At:              s.At,
		Weekday:         time.Weekday(s.Weekday),
		Day:             s.Day,
		Month:           time.Month(s.Month),
	}
}

type scheduleTool struct {
	store       *assistant.Store
	assistantID string
}

func newScheduleTool(store *assistant.Store, assistantID string) *scheduleTool {
	return &scheduleTool{store: store, assistantID: assistantID}
}

func (t *scheduleTool) resolveAssistantID(args json.RawMessage) (string, error) {
	var in struct {
		AssistantID string `json:"assistant_id"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in)
	}
	if strings.TrimSpace(in.AssistantID) == "" {
		return t.assistantID, nil
	}
	return strings.TrimSpace(in.AssistantID), nil
}

// ---- schedule_list ----------------------------------------------------------

type scheduleListTool struct{ *scheduleTool }

// NewScheduleListTool lists the Routines of one Assistant.
func NewScheduleListTool(store *assistant.Store, assistantID string) *scheduleListTool {
	return &scheduleListTool{scheduleTool: newScheduleTool(store, assistantID)}
}

func (t *scheduleListTool) Name() string   { return "schedule_list" }
func (t *scheduleListTool) ReadOnly() bool { return true }

func (t *scheduleListTool) Description() string {
	return "List the Assistant's scheduled tasks (Routines): id, title, schedule, timezone, enabled, catch-up policy, next run cursor, and revision. Use schedule_get for one routine, schedule_create/update/pause/resume/delete to edit, and schedule_run_now to run immediately."
}

func (t *scheduleListTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"assistant_id":{"type":"string","description":"Assistant ID (defaults to the assistant being served)."}},"required":[]}`)
}

func (t *scheduleListTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	id, err := t.resolveAssistantID(args)
	if err != nil {
		return scheduleError("invalid", err), nil
	}
	routines, err := t.store.Routines(id)
	if err != nil {
		return scheduleError("retryable_error", err), nil
	}
	return scheduleResult{Status: "accepted", Routines: routines}.String(), nil
}

// ---- schedule_get -----------------------------------------------------------

type scheduleGetTool struct{ *scheduleTool }

// NewScheduleGetTool reads one Routine.
func NewScheduleGetTool(store *assistant.Store, assistantID string) *scheduleGetTool {
	return &scheduleGetTool{scheduleTool: newScheduleTool(store, assistantID)}
}

func (t *scheduleGetTool) Name() string   { return "schedule_get" }
func (t *scheduleGetTool) ReadOnly() bool { return true }

func (t *scheduleGetTool) Description() string {
	return "Read one scheduled task by id, returning its full definition and current revision."
}

func (t *scheduleGetTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"assistant_id":{"type":"string"},"routine_id":{"type":"string","description":"Routine ID."}},"required":["routine_id"]}`)
}

func (t *scheduleGetTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		AssistantID string `json:"assistant_id"`
		RoutineID   string `json:"routine_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return scheduleError("invalid", err), nil
	}
	id, err := t.resolveAssistantID(args)
	if err != nil {
		return scheduleError("invalid", err), nil
	}
	routines, err := t.store.Routines(id)
	if err != nil {
		return scheduleError("retryable_error", err), nil
	}
	for _, r := range routines {
		if r.ID == in.RoutineID {
			return scheduleResult{Status: "accepted", Routine: &r, Revision: r.Revision}.String(), nil
		}
	}
	return scheduleError("invalid", fmt.Errorf("routine %q not found", in.RoutineID)), nil
}

// ---- schedule_create --------------------------------------------------------

type scheduleCreateTool struct{ *scheduleTool }

// NewScheduleCreateTool creates a Routine.
func NewScheduleCreateTool(store *assistant.Store, assistantID string) *scheduleCreateTool {
	return &scheduleCreateTool{scheduleTool: newScheduleTool(store, assistantID)}
}

func (t *scheduleCreateTool) Name() string   { return "schedule_create" }
func (t *scheduleCreateTool) ReadOnly() bool { return false }

func (t *scheduleCreateTool) Description() string {
	return "Create a new scheduled task. schedule.kind is manual, interval, daily, weekly, biweekly, monthly, or yearly. Provide a stable request_id so a lost reply can be replayed without creating a duplicate."
}

func (t *scheduleCreateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "assistant_id":{"type":"string"},
    "routine_id":{"type":"string","description":"Stable routine ID (e.g. daily-build-check)."},
    "title":{"type":"string"},
    "prompt":{"type":"string","description":"The work instruction to run."},
    "schedule":{"type":"object","properties":{"kind":{"type":"string"},"interval_seconds":{"type":"integer"},"timezone":{"type":"string"},"at":{"type":"string","description":"HH:MM for daily/weekly/monthly/yearly."},"weekday":{"type":"integer"},"day":{"type":"integer"},"month":{"type":"integer"}},"required":["kind"]},
    "enabled":{"type":"boolean"},
    "catch_up":{"type":"string","description":"coalesce_latest or skip."},
    "request_id":{"type":"string"}
  },
  "required":["routine_id","title","prompt","schedule","request_id"]
}`)
}

func (t *scheduleCreateTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		AssistantID string               `json:"assistant_id"`
		RoutineID   string               `json:"routine_id"`
		Title       string               `json:"title"`
		Prompt      string               `json:"prompt"`
		Schedule    scheduleScheduleArgs `json:"schedule"`
		Enabled     *bool                `json:"enabled"`
		CatchUp     string               `json:"catch_up"`
		RequestID   string               `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return scheduleError("invalid", err), nil
	}
	id, err := t.resolveAssistantID(args)
	if err != nil {
		return scheduleError("invalid", err), nil
	}
	if in.RequestID == "" || in.RoutineID == "" {
		return scheduleError("invalid", fmt.Errorf("routine_id and request_id are required")), nil
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	routine := assistant.Routine{
		ID: in.RoutineID, AssistantID: id, Title: in.Title, Prompt: in.Prompt,
		Schedule: in.Schedule.schedule(), Enabled: enabled,
		CatchUp: assistant.CatchUpPolicy(defaultCatchUp(in.CatchUp)),
	}
	created, err := t.store.PutRoutine(assistant.RoutineInput{RequestID: in.RequestID, Routine: routine, Now: time.Now().UTC()})
	if err != nil {
		return scheduleError(mapStoreError(err), err), nil
	}
	return scheduleResult{Status: "accepted", Routine: &created, Revision: created.Revision}.String(), nil
}

// ---- schedule_update --------------------------------------------------------

type scheduleUpdateTool struct{ *scheduleTool }

// NewScheduleUpdateTool updates an existing Routine under revision CAS.
func NewScheduleUpdateTool(store *assistant.Store, assistantID string) *scheduleUpdateTool {
	return &scheduleUpdateTool{scheduleTool: newScheduleTool(store, assistantID)}
}

func (t *scheduleUpdateTool) Name() string   { return "schedule_update" }
func (t *scheduleUpdateTool) ReadOnly() bool { return false }

func (t *scheduleUpdateTool) Description() string {
	return "Update a scheduled task. Pass expected_revision to reject a stale edit that would overwrite a concurrent change. Omitted title/prompt/schedule fields are reset, so pass the full desired definition."
}

func (t *scheduleUpdateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "assistant_id":{"type":"string"},
    "routine_id":{"type":"string"},
    "title":{"type":"string"},
    "prompt":{"type":"string"},
    "schedule":{"type":"object","properties":{"kind":{"type":"string"},"interval_seconds":{"type":"integer"},"timezone":{"type":"string"},"at":{"type":"string"},"weekday":{"type":"integer"},"day":{"type":"integer"},"month":{"type":"integer"}},"required":["kind"]},
    "enabled":{"type":"boolean"},
    "catch_up":{"type":"string"},
    "expected_revision":{"type":"integer"},
    "request_id":{"type":"string"}
  },
  "required":["routine_id","request_id"]
}`)
}

func (t *scheduleUpdateTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		AssistantID      string               `json:"assistant_id"`
		RoutineID        string               `json:"routine_id"`
		Title            string               `json:"title"`
		Prompt           string               `json:"prompt"`
		Schedule         scheduleScheduleArgs `json:"schedule"`
		Enabled          *bool                `json:"enabled"`
		CatchUp          string               `json:"catch_up"`
		ExpectedRevision int64                `json:"expected_revision"`
		RequestID        string               `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return scheduleError("invalid", err), nil
	}
	id, err := t.resolveAssistantID(args)
	if err != nil {
		return scheduleError("invalid", err), nil
	}
	if in.RequestID == "" || in.RoutineID == "" {
		return scheduleError("invalid", fmt.Errorf("routine_id and request_id are required")), nil
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	routine := assistant.Routine{
		ID: in.RoutineID, AssistantID: id, Title: in.Title, Prompt: in.Prompt,
		Schedule: in.Schedule.schedule(), Enabled: enabled,
		CatchUp: assistant.CatchUpPolicy(defaultCatchUp(in.CatchUp)),
	}
	updated, err := t.store.PutRoutine(assistant.RoutineInput{RequestID: in.RequestID, Routine: routine, ExpectedRevision: in.ExpectedRevision, Now: time.Now().UTC()})
	if err != nil {
		return scheduleError(mapStoreError(err), err), nil
	}
	return scheduleResult{Status: "accepted", Routine: &updated, Revision: updated.Revision}.String(), nil
}

// ---- schedule_pause / schedule_resume ---------------------------------------

type scheduleToggleTool struct {
	*scheduleTool
	enabled bool
	name    string
}

// NewSchedulePauseTool disables a routine from the next occurrence.
func NewSchedulePauseTool(store *assistant.Store, assistantID string) *scheduleToggleTool {
	return &scheduleToggleTool{scheduleTool: newScheduleTool(store, assistantID), enabled: false, name: "schedule_pause"}
}

// NewScheduleResumeTool re-enables a routine.
func NewScheduleResumeTool(store *assistant.Store, assistantID string) *scheduleToggleTool {
	return &scheduleToggleTool{scheduleTool: newScheduleTool(store, assistantID), enabled: true, name: "schedule_resume"}
}

func (t *scheduleToggleTool) Name() string   { return t.name }
func (t *scheduleToggleTool) ReadOnly() bool { return false }

func (t *scheduleToggleTool) Description() string {
	if t.enabled {
		return "Re-enable a scheduled task so it triggers again from the next occurrence. Pass expected_revision to reject a stale edit."
	}
	return "Pause a scheduled task so it stops triggering from the next occurrence. Already-running Sessions are not cancelled. Pass expected_revision to reject a stale edit."
}

func (t *scheduleToggleTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"assistant_id":{"type":"string"},"routine_id":{"type":"string"},"expected_revision":{"type":"integer"},"request_id":{"type":"string"}},"required":["routine_id","request_id"]}`)
}

func (t *scheduleToggleTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		AssistantID      string `json:"assistant_id"`
		RoutineID        string `json:"routine_id"`
		ExpectedRevision int64  `json:"expected_revision"`
		RequestID        string `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return scheduleError("invalid", err), nil
	}
	id, err := t.resolveAssistantID(args)
	if err != nil {
		return scheduleError("invalid", err), nil
	}
	if in.RequestID == "" || in.RoutineID == "" {
		return scheduleError("invalid", fmt.Errorf("routine_id and request_id are required")), nil
	}
	routines, err := t.store.Routines(id)
	if err != nil {
		return scheduleError("retryable_error", err), nil
	}
	var current *assistant.Routine
	for i := range routines {
		if routines[i].ID == in.RoutineID {
			current = &routines[i]
			break
		}
	}
	if current == nil {
		return scheduleError("invalid", fmt.Errorf("routine %q not found", in.RoutineID)), nil
	}
	current.Enabled = t.enabled
	updated, err := t.store.PutRoutine(assistant.RoutineInput{
		RequestID: in.RequestID, Routine: *current,
		ExpectedRevision: in.ExpectedRevision, Now: time.Now().UTC(),
	})
	if err != nil {
		return scheduleError(mapStoreError(err), err), nil
	}
	return scheduleResult{Status: "accepted", Routine: &updated, Revision: updated.Revision}.String(), nil
}

// ---- schedule_delete --------------------------------------------------------

type scheduleDeleteTool struct{ *scheduleTool }

// NewScheduleDeleteTool deletes a routine idempotently.
func NewScheduleDeleteTool(store *assistant.Store, assistantID string) *scheduleDeleteTool {
	return &scheduleDeleteTool{scheduleTool: newScheduleTool(store, assistantID)}
}

func (t *scheduleDeleteTool) Name() string   { return "schedule_delete" }
func (t *scheduleDeleteTool) ReadOnly() bool { return false }

func (t *scheduleDeleteTool) Description() string {
	return "Delete a scheduled task. Deleting only stops future triggers and never cancels an already-running Session. Pass expected_revision to reject a stale delete."
}

func (t *scheduleDeleteTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"assistant_id":{"type":"string"},"routine_id":{"type":"string"},"expected_revision":{"type":"integer"},"request_id":{"type":"string"}},"required":["routine_id","request_id"]}`)
}

func (t *scheduleDeleteTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		AssistantID      string `json:"assistant_id"`
		RoutineID        string `json:"routine_id"`
		ExpectedRevision int64  `json:"expected_revision"`
		RequestID        string `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return scheduleError("invalid", err), nil
	}
	id, err := t.resolveAssistantID(args)
	if err != nil {
		return scheduleError("invalid", err), nil
	}
	if in.RequestID == "" || in.RoutineID == "" {
		return scheduleError("invalid", fmt.Errorf("routine_id and request_id are required")), nil
	}
	deleted, err := t.store.DeleteRoutine(assistant.DeleteRoutineInput{
		RequestID: in.RequestID, AssistantID: id, RoutineID: in.RoutineID,
		ExpectedRevision: in.ExpectedRevision, Now: time.Now().UTC(),
	})
	if err != nil {
		return scheduleError(mapStoreError(err), err), nil
	}
	return scheduleResult{Status: "accepted", Routine: deleted, Revision: deleted.Revision}.String(), nil
}

// ---- schedule_run_now -------------------------------------------------------

type scheduleRunNowTool struct{ *scheduleTool }

// NewScheduleRunNowTool fires a routine immediately as one independent run.
func NewScheduleRunNowTool(store *assistant.Store, assistantID string) *scheduleRunNowTool {
	return &scheduleRunNowTool{scheduleTool: newScheduleTool(store, assistantID)}
}

func (t *scheduleRunNowTool) Name() string   { return "schedule_run_now" }
func (t *scheduleRunNowTool) ReadOnly() bool { return false }

func (t *scheduleRunNowTool) Description() string {
	return "Run a scheduled task immediately, once. The stable request_id guarantees a duplicated request (retry, leader switch, restart) does not create a second run/session."
}

func (t *scheduleRunNowTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"assistant_id":{"type":"string"},"routine_id":{"type":"string"},"request_id":{"type":"string"}},"required":["routine_id","request_id"]}`)
}

func (t *scheduleRunNowTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		AssistantID string `json:"assistant_id"`
		RoutineID   string `json:"routine_id"`
		RequestID   string `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return scheduleError("invalid", err), nil
	}
	id, err := t.resolveAssistantID(args)
	if err != nil {
		return scheduleError("invalid", err), nil
	}
	if in.RequestID == "" || in.RoutineID == "" {
		return scheduleError("invalid", fmt.Errorf("routine_id and request_id are required")), nil
	}
	run, err := t.store.RunNow(assistant.RunNowInput{AssistantID: id, RoutineID: in.RoutineID, RequestID: in.RequestID, Now: time.Now().UTC()})
	if err != nil {
		return scheduleError(mapStoreError(err), err), nil
	}
	return scheduleResult{Status: "accepted", Run: &run, Revision: run.Revision}.String(), nil
}

// ---- helpers ----------------------------------------------------------------

func defaultCatchUp(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return string(assistant.CatchUpCoalesceLatest)
	}
	return v
}

// mapStoreError maps the assistant Store's typed errors onto the bounded tool
// outcome vocabulary so the model can branch without parsing error text.
func mapStoreError(err error) string {
	switch {
	case err == nil:
		return "accepted"
	case errorsIs(err, assistant.ErrConflict):
		return "stale"
	case errorsIs(err, assistant.ErrIdempotency):
		return "invalid"
	case errorsIs(err, assistant.ErrNotFound):
		return "invalid"
	case errorsIs(err, assistant.ErrWorkPaused):
		return "blocked_by_policy"
	case errorsIs(err, assistant.ErrTransition):
		return "invalid"
	default:
		return "retryable_error"
	}
}

func errorsIs(err error, target error) bool {
	if err == nil {
		return false
	}
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
