package work

import "testing"

func TestCompilePatchPathRestrictedWhitelist(t *testing.T) {
	for _, path := range []string{
		"goal",
		"nodes/n1/title",
		"nodes/n1/blockIds",
		"nodes/n1/producesSlotIds",
		"nodes/n1/consumesSlotIds",
		"artifactSlots/report/required",
		"inputSpecs/topic/defaultValue",
		"blocks/b1/data",
	} {
		if _, err := CompilePatchPath(path); err != nil {
			t.Errorf("valid path %q: %v", path, err)
		}
	}
	for _, path := range []string{
		"", "..", "nodes/n1/id", "blocks/b1/status", "blocks/b1/source",
		"runs/r1/tasks/t1/state", "permission/policy", "actionReceipt/id",
		"PermissionPolicy/mode", "action_receipt/id", "RUNTIME/state",
		"source/ref", "schema/version", "secret/value",
	} {
		if _, err := CompilePatchPath(path); err == nil {
			t.Errorf("expected path %q to be rejected", path)
		}
	}
}
