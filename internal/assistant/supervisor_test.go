package assistant

import (
	"testing"
)

func TestParseSupervisorDecisionRejectsInvalid(t *testing.T) {
	cases := []string{
		"no json here",
		`{"action":"bogus","target":"x"}`,
		`{"action":"advance"}`,
		`{"action":"answer"}`,
		`{"action":"steer"}`,
	}
	for _, c := range cases {
		if _, err := ParseSupervisorDecision(c); err == nil {
			t.Fatalf("ParseSupervisorDecision(%q) = nil error, want rejection", c)
		}
	}
	// wait/expand need no target.
	if d, err := ParseSupervisorDecision(`{"action":"wait"}`); err != nil || d.Action != ActionWait {
		t.Fatalf("wait decision = %+v err=%v", d, err)
	}
	if d, err := ParseSupervisorDecision(`{"action":"expand"}`); err != nil || d.Action != ActionExpand {
		t.Fatalf("expand decision = %+v err=%v", d, err)
	}
}

func TestParseSupervisorDecisionAcceptsTargetedActions(t *testing.T) {
	d, err := ParseSupervisorDecision(`{"action":"advance","target":"scan","rationale":"scan is ready"}`)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionAdvance || d.Target != "scan" || d.Rationale != "scan is ready" {
		t.Fatalf("decision = %+v", d)
	}
	if d, err := ParseSupervisorDecision(`{"action":"answer","target":"session-1"}`); err != nil || d.Target != "session-1" {
		t.Fatalf("answer decision = %+v err=%v", d, err)
	}
	if d, err := ParseSupervisorDecision(`{"action":"steer","target":"session-2"}`); err != nil || d.Target != "session-2" {
		t.Fatalf("steer decision = %+v err=%v", d, err)
	}
}

// TestSupervisorDecisionParsedFromSessionTurnText proves the bounded decision
// vocabulary is extracted from real supervisor Session turn output (the final
// assistant message), including text that surrounds the JSON object.
func TestSupervisorDecisionParsedFromSessionTurnText(t *testing.T) {
	text := "根据当前状态，决定如下：\n```json\n{\"action\":\"advance\",\"target\":\"scan\"}\n```\n"
	d, err := ParseSupervisorDecision(text)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionAdvance || d.Target != "scan" {
		t.Fatalf("decision = %+v", d)
	}
}
