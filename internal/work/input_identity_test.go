package work

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func inputIdentityEvent(
	t *testing.T,
	store *FileWorkStore,
	workID, inputID string,
	typ WorkEventType,
	identity map[string]string,
	requestID string,
) WorkEvent {
	t.Helper()
	payload := map[string]any{
		"inputId":          inputID,
		"workId":           identity["workId"],
		"runId":            identity["runId"],
		"taskId":           identity["taskId"],
		"blockId":          identity["blockId"],
		"specId":           identity["specId"],
		"revision":         1,
		"expectedRevision": 0,
	}
	switch typ {
	case EventInputSubmitted:
		payload["value"] = "value"
	case EventInputRejected:
		payload["value"] = "value"
		payload["reason"] = "invalid"
	case EventInputCornerstoneChanged:
		payload["cornerstoneId"] = "cornerstone-1"
		payload["pinned"] = true
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	current, state, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	event := newServiceEventV2(workID, requestID, typ, body, time.Now().UTC())
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1
	event.Object = ObjectContext{
		Kind: ObjectInput, ID: inputID, InputID: inputID,
		WorkID: identity["workId"], RunID: identity["runId"],
		TaskID: identity["taskId"], BlockID: identity["blockId"], SpecID: identity["specId"],
		ExpectedRevision: int64Ptr(0), DefinitionRevision: int64Ptr(current.V2CurrentRevision),
	}
	return event
}

func TestInputMutationIdentity_PreflightRejectsMissingOrMismatchedContext_FileStore(t *testing.T) {
	_, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	identity := map[string]string{
		"workId": workID, "runId": "run-1", "taskId": "task-n1",
		"blockId": "blk-1", "specId": "spec-focus",
	}
	eventTypes := []WorkEventType{
		EventInputSubmitted,
		EventInputRejected,
		EventInputCornerstoneChanged,
	}
	fields := []string{"workId", "runId", "taskId", "blockId", "specId"}

	for _, typ := range eventTypes {
		for _, field := range fields {
			t.Run(string(typ)+"/missing-payload-"+field, func(t *testing.T) {
				event := inputIdentityEvent(
					t, store, workID, inputID, typ, identity,
					fmt.Sprintf("%s/missing/%s", typ, field),
				)
				var payload map[string]any
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				payload[field] = ""
				event.Payload, _ = json.Marshal(payload)
				_, before, _ := store.LoadState(workID, "")
				if _, err := store.CommitEvent(workID, event); err == nil {
					t.Fatal("missing payload identity was appended")
				}
				_, after, _ := store.LoadState(workID, "")
				if after.Revision != before.Revision {
					t.Fatalf("revision advanced from %d to %d", before.Revision, after.Revision)
				}
			})

			t.Run(string(typ)+"/mismatched-context-"+field, func(t *testing.T) {
				event := inputIdentityEvent(
					t, store, workID, inputID, typ, identity,
					fmt.Sprintf("%s/mismatch/%s", typ, field),
				)
				switch field {
				case "workId":
					event.Object.WorkID = "other-work"
				case "runId":
					event.Object.RunID = "other-run"
				case "taskId":
					event.Object.TaskID = "other-task"
				case "blockId":
					event.Object.BlockID = "other-block"
				case "specId":
					event.Object.SpecID = "other-spec"
				}
				_, before, _ := store.LoadState(workID, "")
				if _, err := store.CommitEvent(workID, event); err == nil {
					t.Fatal("mismatched ObjectContext identity was appended")
				}
				_, after, _ := store.LoadState(workID, "")
				if after.Revision != before.Revision {
					t.Fatalf("revision advanced from %d to %d", before.Revision, after.Revision)
				}
			})
		}
	}
}

func TestInputReducer_RejectsInputIDCollisionAcrossFullIdentity_FileStore(t *testing.T) {
	_, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	collision := map[string]string{
		"workId": workID, "runId": "other-run", "taskId": "other-task",
		"blockId": "other-block", "specId": "other-spec",
	}
	eventTypes := []WorkEventType{
		EventInputSubmitted,
		EventInputRejected,
		EventInputCornerstoneChanged,
	}
	for _, typ := range eventTypes {
		t.Run(string(typ), func(t *testing.T) {
			event := inputIdentityEvent(
				t, store, workID, inputID, typ, collision, "collision/"+string(typ),
			)
			_, before, _ := store.LoadState(workID, "")
			if _, err := store.CommitEvent(workID, event); err == nil {
				t.Fatal("inputID collision across run/task identity was accepted")
			} else if !strings.Contains(err.Error(), "identity conflict") {
				t.Fatalf("collision rejected before full-identity reducer check: %v", err)
			}
			_, after, _ := store.LoadState(workID, "")
			if after.Revision != before.Revision {
				t.Fatalf("revision advanced from %d to %d", before.Revision, after.Revision)
			}
		})
	}

	requested := InputRequestedPayload{
		InputID: inputID, WorkID: workID,
		RunID: collision["runId"], TaskID: collision["taskId"],
		BlockID: collision["blockId"], SpecID: collision["specId"],
	}
	body, _ := json.Marshal(requested)
	_, state, _ := store.LoadState(workID, "")
	event := newServiceEventV2(workID, "collision/requested", EventInputRequested, body, time.Now().UTC())
	event.BaseRevision, event.Revision = state.Revision, state.Revision+1
	event.Object = ObjectContext{
		Kind: ObjectInput, ID: inputID, InputID: inputID, WorkID: workID,
		RunID: requested.RunID, TaskID: requested.TaskID,
		BlockID: requested.BlockID, SpecID: requested.SpecID,
		DefinitionRevision: int64Ptr(currentDefinitionRevision(t, store, workID)),
	}
	if _, err := store.CommitEvent(workID, event); err == nil {
		t.Fatal("input.requested reused inputID for a different identity")
	} else if !strings.Contains(err.Error(), "identity conflict") {
		t.Fatalf("request collision rejected before full-identity reducer check: %v", err)
	}
}

func currentDefinitionRevision(t *testing.T, store *FileWorkStore, workID string) int64 {
	t.Helper()
	current, _, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	return current.V2CurrentRevision
}
