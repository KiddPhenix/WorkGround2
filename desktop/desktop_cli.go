package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"workground2/internal/config"
)

func desktopRunEmbeddedCLI(args []string, version string) int {
	if len(args) == 0 {
		return 2
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "desktop":
		return desktopEmbeddedCommand(args[1:])
	case "help", "--help", "-h":
		desktopEmbeddedUsage()
		return 0
	case "version", "--version", "-v":
		fmt.Println("WorkGround2", version)
		return 0
	default:
		return 2
	}
}

func desktopEmbeddedCommand(args []string) int {
	if len(args) == 0 {
		desktopEmbeddedDesktopUsage()
		return 2
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]
	switch sub {
	case "help", "--help", "-h":
		desktopEmbeddedDesktopUsage()
		return 0
	case "workspaces", "ws":
		return desktopEmbeddedWorkspaces(rest)
	case "new":
		return desktopEmbeddedNew(rest)
	case "submit":
		return desktopEmbeddedSubmit(rest)
	case "answer":
		return desktopEmbeddedAnswer(rest)
	case "approve":
		return desktopEmbeddedApprove(rest)
	case "status":
		return desktopEmbeddedStatus(rest)
	case "open":
		return desktopEmbeddedOpen(rest)
	case "focus":
		return desktopEmbeddedFocus(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown desktop subcommand: %s\n", args[0])
		return 2
	}
}

func desktopEmbeddedUsage() {
	fmt.Fprintln(os.Stderr, "Usage: WorkGround2 <command>")
	fmt.Fprintln(os.Stderr, "  desktop <subcommand>  control a running Desktop instance")
	fmt.Fprintln(os.Stderr, "  version               print version")
}

func desktopEmbeddedDesktopUsage() {
	fmt.Fprintln(os.Stderr, "Usage: WorkGround2 desktop <subcommand>")
	fmt.Fprintln(os.Stderr, "  workspaces            list Desktop workspaces")
	fmt.Fprintln(os.Stderr, "  new [prompt]          create a new session")
	fmt.Fprintln(os.Stderr, "  submit <prompt>       submit to an explicit session ID")
	fmt.Fprintln(os.Stderr, "  answer               answer a structured ask")
	fmt.Fprintln(os.Stderr, "  approve              allow or deny a pending approval")
	fmt.Fprintln(os.Stderr, "  status                show one session's status")
	fmt.Fprintln(os.Stderr, "  open <path>           open a session file")
	fmt.Fprintln(os.Stderr, "  focus                 bring Desktop window to front")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Options:")
	fmt.Fprintln(os.Stderr, "  --workspace <dir>     target a specific workspace")
	fmt.Fprintln(os.Stderr, "  --session <id>        target a specific SessionID")
	fmt.Fprintln(os.Stderr, "  --session-name NAME   display name for a newly created session")
	fmt.Fprintln(os.Stderr, "  --no-wait             do not wait for reply")
	fmt.Fprintln(os.Stderr, "  --yolo                run with tool approval mode yolo")
	fmt.Fprintln(os.Stderr, "  --tool-approval MODE  ask, auto, or yolo")
	fmt.Fprintln(os.Stderr, "  --id ID               pending interaction ID (answer/approve)")
	fmt.Fprintln(os.Stderr, "  --answer QID=LABEL    selected ask option; repeat for multi-select")
}

func desktopEmbeddedHTTPClient() (*http.Client, string, error) {
	portPath := filepath.Join(config.MemoryUserDir(), remoteAPIPortFile)
	raw, err := os.ReadFile(portPath)
	if err != nil {
		return nil, "", fmt.Errorf("cannot read port file (%s): Desktop may not be running", portPath)
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, "", fmt.Errorf("invalid port file: %w", err)
	}
	return &http.Client{Timeout: remoteWorkspaceReadyTimeout + remoteWorkspaceReadyWriteMargin}, fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

func desktopEmbeddedPostJSON(endpoint string, body any) (map[string]any, error) {
	client, baseURL, err := desktopEmbeddedHTTPClient()
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(body)
	resp, err := client.Post(baseURL+endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func desktopEmbeddedPostEmpty(endpoint string) (map[string]any, error) {
	return desktopEmbeddedPostJSON(endpoint, nil)
}

func desktopEmbeddedGetJSON(endpoint string) (map[string]any, error) {
	client, baseURL, err := desktopEmbeddedHTTPClient()
	if err != nil {
		return nil, err
	}
	resp, err := client.Get(baseURL + endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func desktopEmbeddedStringField(values map[string]any, key string) string {
	if value, ok := values[key].(string); ok {
		return value
	}
	return ""
}

func desktopEmbeddedBoolField(values map[string]any, key string) (bool, bool) {
	if value, ok := values[key].(bool); ok {
		return value, true
	}
	return false, false
}

func desktopEmbeddedToolApprovalMode(yolo bool, explicit string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(explicit))
	if yolo {
		if mode != "" && mode != "yolo" {
			return "", fmt.Errorf("--yolo conflicts with --tool-approval %s", explicit)
		}
		return "yolo", nil
	}
	switch mode {
	case "":
		return "", nil
	case "ask", "auto", "yolo":
		return mode, nil
	default:
		return "", fmt.Errorf("--tool-approval must be ask, auto, or yolo")
	}
}

func desktopEmbeddedSessionOptions(sessionName, toolApprovalMode string) map[string]any {
	body := map[string]any{}
	if strings.TrimSpace(sessionName) != "" {
		body["sessionName"] = strings.TrimSpace(sessionName)
	}
	if toolApprovalMode != "" {
		body["toolApprovalMode"] = toolApprovalMode
	}
	if len(body) == 0 {
		return nil
	}
	return body
}

func desktopEmbeddedSubmitBody(prompt, sessionID, toolApprovalMode string) map[string]string {
	body := map[string]string{"prompt": prompt}
	if strings.TrimSpace(sessionID) != "" {
		body["sessionId"] = strings.TrimSpace(sessionID)
	}
	if toolApprovalMode != "" {
		body["toolApprovalMode"] = toolApprovalMode
	}
	return body
}

type desktopEmbeddedAnswerValues []string

func (v *desktopEmbeddedAnswerValues) String() string { return strings.Join(*v, ",") }

func (v *desktopEmbeddedAnswerValues) Set(value string) error {
	*v = append(*v, value)
	return nil
}

type desktopEmbeddedQuestionAnswer struct {
	QuestionID string   `json:"questionId"`
	Selected   []string `json:"selected"`
}

func desktopEmbeddedParseAnswers(values []string) ([]desktopEmbeddedQuestionAnswer, error) {
	answers := make([]desktopEmbeddedQuestionAnswer, 0, len(values))
	index := map[string]int{}
	for _, value := range values {
		questionID, label, ok := strings.Cut(value, "=")
		questionID = strings.TrimSpace(questionID)
		label = strings.TrimSpace(label)
		if !ok || questionID == "" || label == "" {
			return nil, fmt.Errorf("--answer must be QUESTION_ID=OPTION_LABEL")
		}
		if i, exists := index[questionID]; exists {
			answers[i].Selected = append(answers[i].Selected, label)
			continue
		}
		index[questionID] = len(answers)
		answers = append(answers, desktopEmbeddedQuestionAnswer{QuestionID: questionID, Selected: []string{label}})
	}
	if len(answers) == 0 {
		return nil, fmt.Errorf("at least one --answer is required")
	}
	return answers, nil
}

func desktopEmbeddedPrintSessionReport(action string, result map[string]any) {
	if sessionID := desktopEmbeddedStringField(result, "sessionId"); sessionID != "" {
		fmt.Printf("SessionID: %s\n", sessionID)
	}
	if path := desktopEmbeddedStringField(result, "path"); path != "" {
		fmt.Printf("%s session: %s\n", action, path)
	} else {
		fmt.Printf("%s session\n", action)
	}
	fields := []string{}
	if mode := desktopEmbeddedStringField(result, "toolApprovalMode"); mode != "" {
		fields = append(fields, "toolApproval="+mode)
	}
	if runtimeMode := desktopEmbeddedStringField(result, "mode"); runtimeMode != "" {
		fields = append(fields, "mode="+runtimeMode)
	}
	if running, ok := desktopEmbeddedBoolField(result, "running"); ok {
		fields = append(fields, fmt.Sprintf("running=%t", running))
	}
	if pending, ok := desktopEmbeddedBoolField(result, "pendingPrompt"); ok {
		fields = append(fields, fmt.Sprintf("pendingPrompt=%t", pending))
	}
	if len(fields) > 0 {
		fmt.Println("Status: " + strings.Join(fields, " "))
	}
}

func desktopEmbeddedPrintStatus(result map[string]any, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		return enc.Encode(result)
	}
	desktopEmbeddedPrintSessionReport("Active", result)
	return nil
}

func desktopEmbeddedWorkspaces(args []string) int {
	fs := flag.NewFlagSet("desktop workspaces", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	client, baseURL, err := desktopEmbeddedHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop workspaces: %v\n", err)
		return 1
	}
	resp, err := client.Get(baseURL + "/api/v1/workspaces")
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop workspaces: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	var workspaces []struct {
		Path    string `json:"path"`
		Name    string `json:"name"`
		Current bool   `json:"current"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&workspaces); err != nil {
		fmt.Fprintf(os.Stderr, "desktop workspaces: %v\n", err)
		return 1
	}
	if len(workspaces) == 0 {
		fmt.Println("(no workspaces)")
		return 0
	}
	for _, workspace := range workspaces {
		marker := " "
		if workspace.Current {
			marker = "*"
		}
		name := workspace.Name
		if name == "" {
			name = filepath.Base(workspace.Path)
		}
		fmt.Printf("%s  %-28s %s\n", marker, name, workspace.Path)
	}
	return 0
}

func desktopEmbeddedNew(args []string) int {
	fs := flag.NewFlagSet("desktop new", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "target workspace directory")
	sessionName := fs.String("session-name", "", "display name for the new session")
	_ = fs.Bool("no-wait", false, "do not wait for reply")
	yolo := fs.Bool("yolo", false, "run with tool approval mode yolo")
	toolApproval := fs.String("tool-approval", "", "tool approval mode: ask, auto, or yolo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	toolApprovalMode, err := desktopEmbeddedToolApprovalMode(*yolo, *toolApproval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop new: %v\n", err)
		return 2
	}
	body := desktopEmbeddedSessionOptions(*sessionName, toolApprovalMode)
	if strings.TrimSpace(*workspace) != "" {
		body["workspace"] = strings.TrimSpace(*workspace)
	}
	result, err := desktopEmbeddedPostJSON("/api/v1/session/new", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop new: %v\n", err)
		return 1
	}
	sessionID := desktopEmbeddedStringField(result, "sessionId")
	if sessionID == "" {
		fmt.Fprintln(os.Stderr, "desktop new: response missing sessionId")
		return 1
	}
	desktopEmbeddedPrintSessionReport("Created", result)
	if fs.NArg() == 0 {
		return 0
	}
	prompt := strings.Join(fs.Args(), " ")
	result, err = desktopEmbeddedPostJSON("/api/v1/session/submit", desktopEmbeddedSubmitBody(prompt, sessionID, toolApprovalMode))
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop new: submit: %v\n", err)
		return 1
	}
	desktopEmbeddedPrintSessionReport("Submitted to", result)
	return 0
}

func desktopEmbeddedSubmit(args []string) int {
	fs := flag.NewFlagSet("desktop submit", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "target workspace directory")
	session := fs.String("session", "", "target SessionID")
	_ = fs.Bool("no-wait", false, "do not wait for reply")
	yolo := fs.Bool("yolo", false, "run with tool approval mode yolo")
	toolApproval := fs.String("tool-approval", "", "tool approval mode: ask, auto, or yolo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: WorkGround2 desktop submit [--workspace <dir> | --session <id>] <prompt>")
		return 2
	}
	toolApprovalMode, err := desktopEmbeddedToolApprovalMode(*yolo, *toolApproval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop submit: %v\n", err)
		return 2
	}
	sessionID := strings.TrimSpace(*session)
	if sessionID == "" && strings.TrimSpace(*workspace) != "" {
		body := desktopEmbeddedSessionOptions("", toolApprovalMode)
		body["workspace"] = strings.TrimSpace(*workspace)
		created, err := desktopEmbeddedPostJSON("/api/v1/session/new", body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "desktop submit: create background session: %v\n", err)
			return 1
		}
		sessionID = desktopEmbeddedStringField(created, "sessionId")
	}
	if sessionID == "" {
		fmt.Fprintln(os.Stderr, "desktop submit: --session or --workspace is required")
		return 2
	}
	result, err := desktopEmbeddedPostJSON("/api/v1/session/submit", desktopEmbeddedSubmitBody(strings.Join(fs.Args(), " "), sessionID, toolApprovalMode))
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop submit: %v\n", err)
		return 1
	}
	desktopEmbeddedPrintSessionReport("Submitted to", result)
	return 0
}

func desktopEmbeddedAnswer(args []string) int {
	fs := flag.NewFlagSet("desktop answer", flag.ContinueOnError)
	sessionID := fs.String("session", "", "target SessionID")
	id := fs.String("id", "", "pending ask ID")
	var values desktopEmbeddedAnswerValues
	fs.Var(&values, "answer", "selected option as QUESTION_ID=OPTION_LABEL; repeat for multi-select")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*sessionID) == "" || strings.TrimSpace(*id) == "" {
		fmt.Fprintln(os.Stderr, "desktop answer: --session and --id are required")
		return 2
	}
	answers, err := desktopEmbeddedParseAnswers(values)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop answer: %v\n", err)
		return 2
	}
	if _, err := desktopEmbeddedPostJSON("/api/v1/session/answer", map[string]any{
		"sessionId": strings.TrimSpace(*sessionID),
		"id":        strings.TrimSpace(*id),
		"answers":   answers,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "desktop answer: %v\n", err)
		return 1
	}
	fmt.Printf("Answered interaction %s\n", strings.TrimSpace(*id))
	return 0
}

func desktopEmbeddedApprove(args []string) int {
	fs := flag.NewFlagSet("desktop approve", flag.ContinueOnError)
	sessionID := fs.String("session", "", "target SessionID")
	id := fs.String("id", "", "pending approval ID")
	allow := fs.Bool("allow", false, "allow the pending approval")
	deny := fs.Bool("deny", false, "deny the pending approval")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*sessionID) == "" || strings.TrimSpace(*id) == "" || *allow == *deny {
		fmt.Fprintln(os.Stderr, "desktop approve: --session, --id and exactly one of --allow/--deny are required")
		return 2
	}
	if _, err := desktopEmbeddedPostJSON("/api/v1/session/approve", map[string]any{
		"sessionId": strings.TrimSpace(*sessionID),
		"id":        strings.TrimSpace(*id),
		"allow":     *allow,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "desktop approve: %v\n", err)
		return 1
	}
	fmt.Printf("Resolved approval %s allow=%t\n", strings.TrimSpace(*id), *allow)
	return 0
}

func desktopEmbeddedStatus(args []string) int {
	fs := flag.NewFlagSet("desktop status", flag.ContinueOnError)
	sessionID := fs.String("session", "", "target SessionID")
	asJSON := fs.Bool("json", false, "print raw status JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*sessionID) == "" {
		fmt.Fprintln(os.Stderr, "desktop status: --session is required")
		return 2
	}
	endpoint := "/api/v1/session/status?sessionId=" + url.QueryEscape(strings.TrimSpace(*sessionID))
	result, err := desktopEmbeddedGetJSON(endpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop status: %v\n", err)
		return 1
	}
	if err := desktopEmbeddedPrintStatus(result, *asJSON); err != nil {
		fmt.Fprintf(os.Stderr, "desktop status: %v\n", err)
		return 1
	}
	return 0
}

func desktopEmbeddedOpen(args []string) int {
	fs := flag.NewFlagSet("desktop open", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: WorkGround2 desktop open <path>")
		return 2
	}
	if _, err := desktopEmbeddedPostJSON("/api/v1/session/open", map[string]string{"path": fs.Arg(0)}); err != nil {
		fmt.Fprintf(os.Stderr, "desktop open: %v\n", err)
		return 1
	}
	fmt.Printf("Opened session: %s\n", filepath.Base(fs.Arg(0)))
	return 0
}

func desktopEmbeddedFocus(args []string) int {
	fs := flag.NewFlagSet("desktop focus", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if _, err := desktopEmbeddedPostEmpty("/api/v1/window/focus"); err != nil {
		fmt.Fprintf(os.Stderr, "desktop focus: %v\n", err)
		return 1
	}
	fmt.Println("Desktop window focused")
	return 0
}
