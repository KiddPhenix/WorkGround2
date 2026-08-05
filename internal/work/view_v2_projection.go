package work

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// promoteV2View builds the transport projection from authoritative Work and
// Definition state. Persistence-only receipt/runtime maps remain inside the
// Service and are never exposed as mutable frontend snapshots.
func promoteV2View(view *WorkView, definition *WorkDefinitionRevision) *WorkView {
	if view == nil || view.Work == nil || view.Work.SchemaVersion < SchemaVersionV2 {
		return view
	}
	view.SchemaVersion = WorkViewSchemaVersionV2
	view.Definition = cloneDefinitionForView(definition)
	view.ArtifactSlots = append([]ArtifactSlot{}, view.Work.V2ArtifactSlots...)
	sort.SliceStable(view.ArtifactSlots, func(i, j int) bool {
		if view.ArtifactSlots[i].DefinitionRev != view.ArtifactSlots[j].DefinitionRev {
			return view.ArtifactSlots[i].DefinitionRev < view.ArtifactSlots[j].DefinitionRev
		}
		return view.ArtifactSlots[i].ID < view.ArtifactSlots[j].ID
	})
	view.Inputs = make([]WorkInput, len(view.Work.V2Inputs))
	for i := range view.Work.V2Inputs {
		view.Inputs[i] = cloneWorkInput(view.Work.V2Inputs[i])
	}
	sort.SliceStable(view.Inputs, func(i, j int) bool {
		if view.Inputs[i].ID != view.Inputs[j].ID {
			return view.Inputs[i].ID < view.Inputs[j].ID
		}
		return view.Inputs[i].Revision < view.Inputs[j].Revision
	})

	titles := make(map[string]string)
	if definition != nil {
		for _, node := range definition.Nodes {
			titles[node.ID] = node.Title
		}
	}
	view.Tasks = make([]TaskV2View, 0, len(view.Work.V2TaskRuntimes))
	for _, runtime := range view.Work.V2TaskRuntimes {
		if runtime == nil {
			continue
		}
		var sessionRef *SessionRef
		if runtime.SessionRef != nil {
			cloned := *runtime.SessionRef
			sessionRef = &cloned
		}
		view.Tasks = append(view.Tasks, TaskV2View{
			ID:              runtime.TaskID,
			RunID:           runtime.RunID,
			NodeID:          runtime.NodeID,
			Title:           titles[runtime.NodeID],
			State:           runtime.State,
			Progress:        runtime.Progress,
			SessionRef:      sessionRef,
			WaitingInputIDs: append([]string{}, runtime.WaitingInputIDs...),
			SkillName:       view.Work.V2NodeSkillBindings[runtime.NodeID],
			Error:           runtime.Error,
			Retryable:       runtime.State == TaskFailedRetryable || runtime.State == TaskInvalidated,
			UpdatedAt:       runtime.UpdatedAt,
		})
	}
	sort.SliceStable(view.Tasks, func(i, j int) bool {
		if view.Tasks[i].RunID != view.Tasks[j].RunID {
			return view.Tasks[i].RunID < view.Tasks[j].RunID
		}
		if view.Tasks[i].NodeID != view.Tasks[j].NodeID {
			return view.Tasks[i].NodeID < view.Tasks[j].NodeID
		}
		return view.Tasks[i].ID < view.Tasks[j].ID
	})

	view.PatchPreviews = make([]WorkPatchPreview, 0, len(view.Work.V2PatchPreviews))
	for _, preview := range view.Work.V2PatchPreviews {
		view.PatchPreviews = append(view.PatchPreviews, preview)
	}
	sort.SliceStable(view.PatchPreviews, func(i, j int) bool {
		return view.PatchPreviews[i].ID < view.PatchPreviews[j].ID
	})
	return view
}

func cloneDefinitionForView(value *WorkDefinitionRevision) *WorkDefinitionRevision {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Nodes = append([]NodeDef{}, value.Nodes...)
	for i := range clone.Nodes {
		clone.Nodes[i].DependsOn = append([]string{}, value.Nodes[i].DependsOn...)
		clone.Nodes[i].BlockIDs = append([]string{}, value.Nodes[i].BlockIDs...)
		clone.Nodes[i].ProducesSlotIDs = append([]string{}, value.Nodes[i].ProducesSlotIDs...)
		clone.Nodes[i].ConsumesSlotIDs = append([]string{}, value.Nodes[i].ConsumesSlotIDs...)
		clone.Nodes[i].InputSpecIDs = append([]string{}, value.Nodes[i].InputSpecIDs...)
		clone.Nodes[i].ToolHints = append([]string{}, value.Nodes[i].ToolHints...)
	}
	clone.ArtifactSlots = append([]ArtifactSlotDef{}, value.ArtifactSlots...)
	clone.InputSpecs = append([]InputSpec{}, value.InputSpecs...)
	return &clone
}

func stripV2PersistenceFields(value *Work) {
	if value == nil {
		return
	}
	value.V2RevisionStates = nil
	value.V2ArtifactSlots = nil
	value.V2ArtifactReceipts = nil
	value.V2Inputs = nil
	value.V2InputReceipts = nil
	value.V2PatchPreviews = nil
	value.V2PatchReceipts = nil
	value.V2TaskRuntimes = nil
}

// AsV1WorkView removes V2-only top-level transport fields. Controller uses it
// when collaboration_workbench_v2 is disabled; the authoritative Work remains
// untouched and can be exposed again after the feature is enabled.
func AsV1WorkView(view *WorkView) *WorkView {
	if view == nil {
		return nil
	}
	clone := *view
	if view.Work != nil {
		workClone := *view.Work
		clone.Work = &workClone
		stripV2PersistenceFields(clone.Work)
	}
	clone.SchemaVersion = WorkViewSchemaVersion
	clone.Definition = nil
	clone.ArtifactSlots = nil
	clone.Tasks = nil
	clone.Inputs = nil
	clone.PatchPreviews = nil
	return &clone
}

func (s *Service) emitV2MutationSnapshot(view *WorkView, baseRevision int64, requestID string) error {
	if s == nil || !s.v2Transport.Load() || view == nil || view.Work == nil ||
		view.Work.SchemaVersion < SchemaVersionV2 || view.Revision <= baseRevision {
		return nil
	}
	if err := s.prepareTransportView(view); err != nil {
		return err
	}
	s.assessView(view)
	payload, err := json.Marshal(view)
	if err != nil {
		return fmt.Errorf("work: encode V2 WorkView snapshot: %w", err)
	}
	s.sink.EmitWorkView(WorkViewEvent{
		SchemaVersion: WorkViewSchemaVersionV2,
		Type:          ViewSnapshot,
		WorkID:        view.Work.ID,
		EventID:       fmt.Sprintf("work-view-v2m-%s-%d", view.Work.ID, view.Revision),
		Revision:      view.Revision,
		BaseRevision:  0,
		RequestID:     requestID,
		Object:        ObjectContext{Kind: ObjectWork, ID: view.Work.ID, WorkID: view.Work.ID},
		Payload:       payload,
		CreatedAt:     time.Now().UTC(),
	})
	return nil
}
