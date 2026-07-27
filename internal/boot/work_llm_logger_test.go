package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"workground2/internal/provider"
	"workground2/internal/work"
)

func TestWorkLLMInteractionLogger_NilNoFile(t *testing.T) {
	dir := t.TempDir()
	// nil logger must be a no-op — no file, no panic.
	var log *workLLMInteractionLogger
	log.logRequest("id1", "definition", "w1", "test-prov", 0, nil, 0.7, 4096)
	log.logResponse("id1", "definition", "w1", 0, "{}", nil)
	// No files should exist in temp dir.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("nil logger must not create any files")
	}
}

func TestWorkLLMInteractionLogger_DisabledZeroFile(t *testing.T) {
	// When LLMInteractionLog is false, nil logger is in use — no file created.
	prov := &definitionPlannerProviderStub{chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: validPlanJSON()},
		{Type: provider.ChunkDone},
	}}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	_, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "test",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Planner ran without panic — success for nil logger.
}

func TestWorkLLMInteractionLogger_RequestResponsePair(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")
	log := newWorkLLMInteractionLogger(logPath)
	if log == nil {
		t.Fatal("logger must be created")
	}
	defer log.Close()

	iid := interactionID("definition", "w1", 0)
	log.logRequest(iid, "definition", "w1", "fake-prov", 0,
		[]provider.Message{{Role: provider.RoleSystem, Content: "sys"}},
		0.7, 4096,
	)
	log.logResponse(iid, "definition", "w1", 0, `{"goal":"ok"}`, nil)

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitJSONLLines(t, raw)
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSONL lines, got %d", len(lines))
	}

	var req map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &req); err != nil {
		t.Fatalf("line 0 not valid JSON: %v\n%s", err, lines[0])
	}
	if req["type"] != "request" {
		t.Fatalf("type=%v, want request", req["type"])
	}
	if req["kind"] != "definition" {
		t.Fatalf("kind=%v, want definition", req["kind"])
	}
	if req["workId"] != "w1" {
		t.Fatalf("workId=%v, want w1", req["workId"])
	}
	if req["attempt"] != float64(0) {
		t.Fatalf("attempt=%v", req["attempt"])
	}
	if req["provider"] != "fake-prov" {
		t.Fatalf("provider=%v", req["provider"])
	}
	if req["temperature"] != float64(0.7) {
		t.Fatalf("temperature=%v", req["temperature"])
	}
	if req["maxTokens"] != float64(4096) {
		t.Fatalf("maxTokens=%v", req["maxTokens"])
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("line 1 not valid JSON: %v\n%s", err, lines[1])
	}
	if resp["type"] != "response" {
		t.Fatalf("type=%v, want response", resp["type"])
	}
	if resp["interactionId"] != iid {
		t.Fatalf("interactionId mismatch: req=%v resp=%v", req["interactionId"], resp["interactionId"])
	}
	if resp["rawResponse"] != `{"goal":"ok"}` {
		t.Fatalf("rawResponse=%v", resp["rawResponse"])
	}
	if _, hasErr := resp["error"]; hasErr {
		t.Fatal("success response must not have error field")
	}
}

func TestWorkLLMInteractionLogger_ErrorResponse(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "error.jsonl")
	log := newWorkLLMInteractionLogger(logPath)
	if log == nil {
		t.Fatal("logger must be created")
	}
	defer log.Close()

	iid := interactionID("patch", "w2", 1)
	log.logRequest(iid, "patch", "w2", "prov", 1, nil, 0.5, 2048)
	log.logResponse(iid, "patch", "w2", 1, "partial text", fmt.Errorf("chunk error: model overloaded"))

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitJSONLLines(t, raw)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["rawResponse"] != "partial text" {
		t.Fatalf("rawResponse=%v", resp["rawResponse"])
	}
	errStr, ok := resp["error"].(string)
	if !ok || !strings.Contains(errStr, "model overloaded") {
		t.Fatalf("error=%v", resp["error"])
	}
}

func TestWorkLLMInteractionLogger_DefinitionPlannerIntegrated(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "integrated.jsonl")
	log := newWorkLLMInteractionLogger(logPath)
	if log == nil {
		t.Fatal("logger must be created")
	}
	defer log.Close()

	prov := &definitionPlannerProviderStub{chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: validPlanJSON()},
		{Type: provider.ChunkDone},
	}}
	planner := newBootDefinitionPlanner(prov, 0.2, 2048, log)
	_, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "integrated test",
		Work:   &work.Work{ID: "w-int"},
		Base:   &work.WorkDefinitionRevision{WorkID: "w-int", Revision: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitJSONLLines(t, raw)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (1 request + 1 response), got %d", len(lines))
	}

	var req, resp map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &req); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatal(err)
	}
	if req["interactionId"] != resp["interactionId"] {
		t.Fatalf("interactionId mismatch: %v vs %v", req["interactionId"], resp["interactionId"])
	}
	if req["workId"] != "w-int" {
		t.Fatalf("workId=%v", req["workId"])
	}
	if req["kind"] != "definition" {
		t.Fatalf("kind=%v", req["kind"])
	}
	if req["attempt"] != float64(1) {
		t.Fatalf("attempt=%v, want 1", req["attempt"])
	}
	if resp["type"] != "response" {
		t.Fatalf("resp type=%v", resp["type"])
	}
}

func TestWorkLLMInteractionLogger_DefinitionRepairLogsParseError(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "definition-repair.jsonl")
	log := newWorkLLMInteractionLogger(logPath)

	prov := &sequenceProvider{sequences: [][]provider.Chunk{
		{chunkT(validPlanJSON() + "\n" + validPlanJSON()), chunkD},
		{chunkT(validPlanJSON()), chunkD},
	}}
	planner := newBootDefinitionPlanner(prov, 0, 2048, log)
	if _, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "repair logging",
		Work:   &work.Work{ID: "w-repair"},
		Base:   &work.WorkDefinitionRevision{WorkID: "w-repair", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitJSONLLines(t, raw)
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d:\n%s", len(lines), raw)
	}

	var firstResponse, secondRequest map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &firstResponse); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[2]), &secondRequest); err != nil {
		t.Fatal(err)
	}
	if firstResponse["attempt"] != float64(1) {
		t.Fatalf("first attempt=%v, want 1", firstResponse["attempt"])
	}
	if parseErr, _ := firstResponse["error"].(string); !strings.Contains(parseErr, "multiple JSON values") {
		t.Fatalf("first parse error=%v", firstResponse["error"])
	}
	if secondRequest["attempt"] != float64(2) {
		t.Fatalf("second attempt=%v, want 2", secondRequest["attempt"])
	}
}

func TestWorkLLMInteractionLogger_ChunkErrorLogsPartialRaw(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "chunk-err.jsonl")
	log := newWorkLLMInteractionLogger(logPath)
	if log == nil {
		t.Fatal("logger must be created")
	}
	defer log.Close()

	prov := &definitionPlannerProviderStub{chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "partial output before crash"},
		{Type: provider.ChunkError, Err: fmt.Errorf("model capacity exceeded")},
	}}
	planner := newBootDefinitionPlanner(prov, 0, 2048, log)
	_, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "chunk error test",
		Work:   &work.Work{ID: "w-ce"},
		Base:   &work.WorkDefinitionRevision{WorkID: "w-ce", Revision: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "model capacity exceeded") {
		t.Fatalf("expected chunk error, got: %v", err)
	}

	raw, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	lines := splitJSONLLines(t, raw)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["rawResponse"] != "partial output before crash" {
		t.Fatalf("partial raw not preserved: %v", resp["rawResponse"])
	}
	errStr, _ := resp["error"].(string)
	if !strings.Contains(errStr, "model capacity exceeded") {
		t.Fatalf("error not preserved: %v", errStr)
	}
}

func TestWorkLLMInteractionLogger_StreamErrorIsLogged(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "stream-error.jsonl")
	log := newWorkLLMInteractionLogger(logPath)
	prov := &definitionPlannerProviderStub{err: fmt.Errorf("provider unavailable")}
	planner := newBootDefinitionPlanner(prov, 0, 2048, log)

	_, planErr := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "stream error logging",
		Work:   &work.Work{ID: "w-stream"},
		Base:   &work.WorkDefinitionRevision{WorkID: "w-stream", Revision: 1},
	})
	if planErr == nil || !strings.Contains(planErr.Error(), "provider unavailable") {
		t.Fatalf("plan error=%v", planErr)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitJSONLLines(t, raw)
	if len(lines) != 2 {
		t.Fatalf("expected request and response, got %d", len(lines))
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["rawResponse"] != "" {
		t.Fatalf("rawResponse=%v, want empty string", resp["rawResponse"])
	}
	if streamErr, _ := resp["error"].(string); !strings.Contains(streamErr, "provider unavailable") {
		t.Fatalf("response error=%v", resp["error"])
	}
}

func TestWorkLLMInteractionLogger_PatchPlannerIntegrated(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "patch.jsonl")
	log := newWorkLLMInteractionLogger(logPath)
	if log == nil {
		t.Fatal("logger must be created")
	}
	defer log.Close()

	prov := &patchPlannerProviderStub{sequences: [][]provider.Chunk{
		patchChunks(validPatchPlanJSON),
	}}
	planner := newBootPatchPlanner(prov, 0, 2048, log)
	_, err := planner.PlanPatch(context.Background(), patchPlannerInput())
	if err != nil {
		t.Fatal(err)
	}

	raw, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	lines := splitJSONLLines(t, raw)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var req, resp map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &req); err != nil {
		t.Fatal(err)
	}
	if req["kind"] != "patch" {
		t.Fatalf("kind=%v, want patch", req["kind"])
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["type"] != "response" {
		t.Fatalf("resp type=%v", resp["type"])
	}
}

func TestWorkLLMInteractionLogger_PatchPlannerRepairLogsBothAttempts(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "patch-repair.jsonl")
	log := newWorkLLMInteractionLogger(logPath)
	if log == nil {
		t.Fatal("logger must be created")
	}
	defer log.Close()

	prov := &patchPlannerProviderStub{sequences: [][]provider.Chunk{
		patchChunks("自然语言回答 - 第一次"),
		patchChunks(validPatchPlanJSON),
	}}
	planner := newBootPatchPlanner(prov, 0, 2048, log)
	_, err := planner.PlanPatch(context.Background(), patchPlannerInput())
	if err != nil {
		t.Fatal(err)
	}

	raw, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	lines := splitJSONLLines(t, raw)
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (req+resp × 2 attempts), got %d:\n%s", len(lines), string(raw))
	}

	var req0, resp0 map[string]any
	json.Unmarshal([]byte(lines[0]), &req0)
	json.Unmarshal([]byte(lines[1]), &resp0)
	if req0["attempt"] != float64(1) {
		t.Fatalf("attempt0=%v, want 1", req0["attempt"])
	}
	if resp0["rawResponse"] != "自然语言回答 - 第一次" {
		t.Fatalf("attempt0 raw=%v", resp0["rawResponse"])
	}
	if parseErr, _ := resp0["error"].(string); !strings.Contains(parseErr, "no valid PatchPlan") {
		t.Fatalf("attempt0 parse error=%v", resp0["error"])
	}

	var req1, resp1 map[string]any
	json.Unmarshal([]byte(lines[2]), &req1)
	json.Unmarshal([]byte(lines[3]), &resp1)
	if req1["attempt"] != float64(2) {
		t.Fatalf("attempt1=%v, want 2", req1["attempt"])
	}
}

func TestWorkLLMInteractionLogger_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "concurrent.jsonl")
	log := newWorkLLMInteractionLogger(logPath)
	if log == nil {
		t.Fatal("logger must be created")
	}
	defer log.Close()

	const writers = 10
	const linesPer = 20
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(id int) {
			defer wg.Done()
			kind := "definition"
			if id%2 == 0 {
				kind = "patch"
			}
			for i := 0; i < linesPer; i++ {
				iid := fmt.Sprintf("conc-%d-%d", id, i)
				log.logRequest(iid, kind, fmt.Sprintf("w-%d", id), "prov", i, nil, 0, 0)
				log.logResponse(iid, kind, fmt.Sprintf("w-%d", id), i, "ok", nil)
			}
		}(w)
	}
	wg.Wait()

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitJSONLLines(t, raw)
	expected := writers * linesPer * 2
	if len(lines) != expected {
		t.Fatalf("expected %d lines, got %d", expected, len(lines))
	}

	// Every line must be valid JSON.
	for i, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d not valid JSON: %v\n%s", i, err, line)
		}
	}
}

// splitJSONLLines splits raw JSONL bytes into non-empty lines.
func splitJSONLLines(t *testing.T, raw []byte) []string {
	t.Helper()
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
