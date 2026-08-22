package vocabulary

import (
	"context"
	"encoding/json"

	"workground2/internal/tool"
)

// rebuildTool exposes the deterministic workspace scan to the agent. It is
// bound to one Session-local Service so a successful rebuild also refreshes the
// completion snapshot used by that Session.
type rebuildTool struct{ service *Service }

// NewRebuildTool returns the write tool used by the built-in
// /rebuild_vocabulary Skill.
func NewRebuildTool(service *Service) tool.Tool { return rebuildTool{service: service} }

func (rebuildTool) Name() string { return "rebuild_vocabulary" }

func (rebuildTool) Description() string {
	return "Deterministically scan the current workspace, rebuild .WorkGround2/vocabulary.toml, and refresh this Session's vocabulary snapshot. Call with an empty object."
}

func (rebuildTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t rebuildTool) Execute(context.Context, json.RawMessage) (string, error) {
	result, err := t.service.RebuildWorkspace()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (rebuildTool) ReadOnly() bool { return false }
