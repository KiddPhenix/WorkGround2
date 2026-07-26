package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDefinitionPlanLeaseArbitratesAcrossProcesses_FileWorkStore(t *testing.T) {
	if os.Getenv("WORK_DEFINITION_PLAN_LEASE_HELPER") == "1" {
		runDefinitionPlanLeaseHelper(t)
		return
	}
	root := t.TempDir()
	marker := filepath.Join(root, "lease-held")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDefinitionPlanLeaseArbitratesAcrossProcesses_FileWorkStore$")
	cmd.Env = append(os.Environ(),
		"WORK_DEFINITION_PLAN_LEASE_HELPER=1",
		"WORK_DEFINITION_PLAN_LEASE_ROOT="+root,
		"WORK_DEFINITION_PLAN_LEASE_MARKER="+marker,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, marker, 5*time.Second)

	store, err := NewFileWorkStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if release, lockErr := store.AcquireDefinitionPlanLease(ctx, "work-cross-process", "request-one"); release != nil {
		_ = release()
		t.Fatal("second process acquired a held definition plan lease")
	} else if !errors.Is(lockErr, context.DeadlineExceeded) {
		t.Fatalf("lock error=%v, want context deadline", lockErr)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("force-terminated lease helper exited successfully")
	}
	release, err := store.AcquireDefinitionPlanLease(context.Background(), "work-cross-process", "request-one")
	if err != nil {
		t.Fatalf("acquire after crashed/exited owner: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func runDefinitionPlanLeaseHelper(t *testing.T) {
	root := os.Getenv("WORK_DEFINITION_PLAN_LEASE_ROOT")
	store, err := NewFileWorkStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	release, err := store.AcquireDefinitionPlanLease(
		context.Background(),
		"work-cross-process",
		"request-one",
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = release
	if err := os.WriteFile(os.Getenv("WORK_DEFINITION_PLAN_LEASE_MARKER"), []byte("held"), 0o644); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

type definitionPlanProcessResult struct {
	Committed bool  `json:"committed"`
	Duplicate bool  `json:"duplicate"`
	Revision  int64 `json:"revision"`
	Candidate int64 `json:"candidate"`
}

func TestDefinitionPlannerTwoIndependentProcessesCallModelOnce_FileWorkStore(t *testing.T) {
	h, request := crossProcessPlanFixture(t, "two-process-model-once")
	countPath := filepath.Join(t.TempDir(), "model-calls")
	resultDir := t.TempDir()
	first := definitionPlannerProcess(t, h.store.workDir, request, countPath, filepath.Join(resultDir, "first.json"), "normal")
	second := definitionPlannerProcess(t, h.store.workDir, request, countPath, filepath.Join(resultDir, "second.json"), "normal")
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		_ = first.Process.Kill()
		t.Fatal(err)
	}
	if err := first.Wait(); err != nil {
		t.Fatalf("first process: %v", err)
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("second process: %v", err)
	}
	if calls := definitionPlannerCallCount(t, countPath); calls != 1 {
		t.Fatalf("cross-process model calls=%d, want 1", calls)
	}
	left := readDefinitionPlanProcessResult(t, filepath.Join(resultDir, "first.json"))
	right := readDefinitionPlanProcessResult(t, filepath.Join(resultDir, "second.json"))
	if !left.Committed || !right.Committed || left.Candidate == 0 || left.Candidate != right.Candidate ||
		left.Duplicate == right.Duplicate {
		t.Fatalf("first=%+v second=%+v", left, right)
	}
}

func TestDefinitionPlannerCrashBeforeReceiptIsTakenOver_FileWorkStore(t *testing.T) {
	h, request := crossProcessPlanFixture(t, "crash-before-receipt")
	countPath := filepath.Join(t.TempDir(), "model-calls")
	crashed := definitionPlannerProcess(t, h.store.workDir, request, countPath, "", "crash_before_receipt")
	if err := crashed.Run(); err == nil {
		t.Fatal("planner owner did not crash before receipt")
	}
	if calls := definitionPlannerCallCount(t, countPath); calls != 1 {
		t.Fatalf("crashed owner model calls=%d, want 1", calls)
	}
	if receipt, err := h.store.LoadV2Receipt(h.work, request.RequestID); err == nil || receipt != nil {
		t.Fatalf("receipt exists before takeover: receipt=%+v err=%v", receipt, err)
	}
	resultPath := filepath.Join(t.TempDir(), "takeover.json")
	takeover := definitionPlannerProcess(t, h.store.workDir, request, countPath, resultPath, "normal")
	if err := takeover.Run(); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if calls := definitionPlannerCallCount(t, countPath); calls != 2 {
		t.Fatalf("model calls after takeover=%d, want 2", calls)
	}
	result := readDefinitionPlanProcessResult(t, resultPath)
	if !result.Committed || result.Duplicate || result.Candidate == 0 {
		t.Fatalf("takeover result=%+v", result)
	}
}

func TestDefinitionPlannerCrashAfterReceiptReplaysWithoutModel_FileWorkStore(t *testing.T) {
	h, request := crossProcessPlanFixture(t, "crash-after-receipt")
	countPath := filepath.Join(t.TempDir(), "model-calls")
	crashed := definitionPlannerProcess(t, h.store.workDir, request, countPath, "", "crash_after_receipt")
	if err := crashed.Run(); err == nil {
		t.Fatal("planner owner did not crash before ACK")
	}
	if calls := definitionPlannerCallCount(t, countPath); calls != 1 {
		t.Fatalf("owner model calls=%d, want 1", calls)
	}
	receipt, err := h.store.LoadV2Receipt(h.work, request.RequestID)
	if err != nil || receipt == nil {
		t.Fatalf("durable receipt missing after owner crash: receipt=%+v err=%v", receipt, err)
	}
	resultPath := filepath.Join(t.TempDir(), "replay.json")
	replay := definitionPlannerProcess(t, h.store.workDir, request, countPath, resultPath, "normal")
	if err := replay.Run(); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if calls := definitionPlannerCallCount(t, countPath); calls != 1 {
		t.Fatalf("replay called model; calls=%d", calls)
	}
	result := readDefinitionPlanProcessResult(t, resultPath)
	if !result.Committed || !result.Duplicate || result.Candidate != receipt.ResultRevision {
		t.Fatalf("replay result=%+v receipt=%+v", result, receipt)
	}
}

func TestDefinitionPlannerCrossProcessHelper(t *testing.T) {
	if os.Getenv("WORK_DEFINITION_PLAN_PROCESS_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	root := os.Getenv("WORK_DEFINITION_PLAN_PROCESS_ROOT")
	store, err := NewFileWorkStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	baseRevision, err := strconv.ParseInt(os.Getenv("WORK_DEFINITION_PLAN_PROCESS_BASE"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	expectedRevision, err := strconv.ParseInt(os.Getenv("WORK_DEFINITION_PLAN_PROCESS_EXPECTED"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	mode := os.Getenv("WORK_DEFINITION_PLAN_PROCESS_MODE")
	service := NewService(store, nil, nil)
	service.SetV2DefinitionPlanner(definitionPlannerFunc(func(_ context.Context, input DefinitionPlanInput) (*DefinitionPlan, error) {
		countFile := os.Getenv("WORK_DEFINITION_PLAN_PROCESS_COUNT")
		file, openErr := os.OpenFile(countFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if openErr != nil {
			return nil, openErr
		}
		_, writeErr := fmt.Fprintf(file, "%d\n", os.Getpid())
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return nil, errors.Join(writeErr, closeErr)
		}
		if mode == "crash_before_receipt" {
			os.Exit(31)
		}
		return &DefinitionPlan{
			Goal:          "cross-process planned",
			Nodes:         append([]NodeDef(nil), input.Base.Nodes...),
			ArtifactSlots: append([]ArtifactSlotDef(nil), input.Base.ArtifactSlots...),
			InputSpecs:    append([]InputSpec(nil), input.Base.InputSpecs...),
		}, nil
	}))
	result, callErr := service.CreateCandidateRevisionWithResult(context.Background(), CreateCandidateRevisionInput{
		WorkID: os.Getenv("WORK_DEFINITION_PLAN_PROCESS_WORK"), Intent: "cross-process structure change",
		BaseDefinitionRevision: baseRevision, ExpectedRevision: expectedRevision,
		RequestID: os.Getenv("WORK_DEFINITION_PLAN_PROCESS_REQUEST"),
	})
	if mode == "crash_after_receipt" && result != nil && result.Committed {
		os.Exit(32)
	}
	if callErr != nil {
		t.Fatal(callErr)
	}
	if result == nil || result.Candidate == nil {
		t.Fatalf("result=%+v", result)
	}
	body, _ := json.Marshal(definitionPlanProcessResult{
		Committed: result.Committed, Duplicate: result.Duplicate,
		Revision: result.Revision, Candidate: result.Candidate.Revision,
	})
	if err := os.WriteFile(os.Getenv("WORK_DEFINITION_PLAN_PROCESS_RESULT"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func crossProcessPlanFixture(t *testing.T, requestID string) (*coordinatorHarness, CreateCandidateRevisionInput) {
	t.Helper()
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "base", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	return h, CreateCandidateRevisionInput{
		WorkID: h.work, Intent: "cross-process structure change",
		BaseDefinitionRevision: h.def.Revision, ExpectedRevision: state.Revision,
		RequestID: requestID,
	}
}

func definitionPlannerProcess(
	t *testing.T,
	root string,
	request CreateCandidateRevisionInput,
	countPath, resultPath, mode string,
) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDefinitionPlannerCrossProcessHelper$")
	cmd.Env = append(os.Environ(),
		"WORK_DEFINITION_PLAN_PROCESS_HELPER=1",
		"WORK_DEFINITION_PLAN_PROCESS_ROOT="+root,
		"WORK_DEFINITION_PLAN_PROCESS_WORK="+request.WorkID,
		"WORK_DEFINITION_PLAN_PROCESS_BASE="+strconv.FormatInt(request.BaseDefinitionRevision, 10),
		"WORK_DEFINITION_PLAN_PROCESS_EXPECTED="+strconv.FormatInt(request.ExpectedRevision, 10),
		"WORK_DEFINITION_PLAN_PROCESS_REQUEST="+request.RequestID,
		"WORK_DEFINITION_PLAN_PROCESS_COUNT="+countPath,
		"WORK_DEFINITION_PLAN_PROCESS_RESULT="+resultPath,
		"WORK_DEFINITION_PLAN_PROCESS_MODE="+mode,
	)
	return cmd
}

func definitionPlannerCallCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(data)))
}

func readDefinitionPlanProcessResult(t *testing.T, path string) definitionPlanProcessResult {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result definitionPlanProcessResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
