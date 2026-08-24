package boot

import (
	"context"
	"testing"

	"workground2/internal/agent/testutil"
	"workground2/internal/event"
)

func bootToolNameSet(t *testing.T, toml string) map[string]bool {
	t.Helper()
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	writeFile(t, dir, "WorkGround2.toml", toml)

	registerBootTokenProfileTestProvider()
	prov := testutil.NewMock("token-profile", testutil.Turn{Text: "done"})
	setBootTokenProfileTestProvider(t, prov)

	ctrl, err := Build(context.Background(), Options{Sink: event.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	names := map[string]bool{}
	for _, e := range ctrl.ToolContractEntries() {
		names[e.Name] = true
	}
	return names
}

func TestViewImageExposedOnlyToDirectVisionModel(t *testing.T) {
	t.Run("vision-model", func(t *testing.T) {
		names := bootToolNameSet(t, `default_model = "vision-main"

[agent]
system_prompt = "BASE"

[[providers]]
name = "vision-main"
kind = "boot-token-profile-test"
model = "x"
capabilities = ["vision"]
`)
		if !names["view_image"] {
			t.Fatalf("direct vision model must expose view_image; tools=%v", names)
		}
	})

	t.Run("text-only-model", func(t *testing.T) {
		names := bootToolNameSet(t, `default_model = "text-main"

[agent]
system_prompt = "BASE"

[[providers]]
name = "text-main"
kind = "boot-token-profile-test"
model = "x"
`)
		if names["view_image"] {
			t.Fatalf("text-only model must not expose view_image; tools=%v", names)
		}
	})

	t.Run("delegate-only-main-model", func(t *testing.T) {
		names := bootToolNameSet(t, `default_model = "text-main"

[agent]
system_prompt = "BASE"
vision_delegate = "vision-delegate"

[[providers]]
name = "text-main"
kind = "boot-token-profile-test"
model = "x"

[[providers]]
name = "vision-delegate"
kind = "boot-token-profile-test"
model = "x"
capabilities = ["vision"]
`)
		if names["view_image"] {
			t.Fatalf("vision-delegate-only main model must not expose view_image; tools=%v", names)
		}
	})
}
