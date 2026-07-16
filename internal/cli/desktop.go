package cli

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
	"time"

	"workground2/internal/agent"
	"workground2/internal/config"
	"workground2/internal/provider"
)

const (
	desktopPortFile   = "desktop-port"
	pollInterval      = 500 * time.Millisecond
	pollTimeout       = 5 * time.Minute
	pollMaxQuietTicks = 6
)

func desktopCommand(args []string) int {
	if len(args) == 0 {
		desktopUsage()
		return 2
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "help", "--help", "-h":
		desktopUsage()
		return 0
	case "open":
		return desktopOpen(rest)
	case "new":
		return desktopNew(rest)
	case "submit":
		return desktopSubmit(rest)
	case "answer":
		return desktopAnswer(rest)
	case "approve":
		return desktopApprove(rest)
	case "status":
		return desktopStatus(rest)
	case "workspaces", "ws":
		return desktopWorkspaces(rest)
	case "focus":
		return desktopFocus(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown desktop subcommand: %s\n", sub)
		return 2
	}
}

func desktopUsage() {
	fmt.Fprintln(os.Stderr, "Usage: workground2 desktop <subcommand>")
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

func desktopPort() (int, error) {
	portPath := filepath.Join(config.MemoryUserDir(), desktopPortFile)
	b, err := os.ReadFile(portPath)
	if err != nil {
		return 0, fmt.Errorf("cannot read port file (%s): Desktop may not be running", portPath)
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("invalid port file: %w", err)
	}
	return port, nil
}

func desktopClient() (*http.Client, string, error) {
	port, err := desktopPort()
	if err != nil {
		return nil, "", err
	}
	return &http.Client{Timeout: 5 * time.Second}, fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

func desktopPostJSON(endpoint string, body any) (map[string]interface{}, error) {
	client, baseURL, err := desktopClient()
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(body)
	resp, err := client.Post(baseURL+endpoint, "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var m map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func desktopPostEmpty(endpoint string) (map[string]interface{}, error) {
	return desktopPostJSON(endpoint, nil)
}

func desktopGetJSON(endpoint string) (map[string]interface{}, error) {
	client, baseURL, err := desktopClient()
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
	var m map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func stringField(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func boolField(m map[string]interface{}, key string) (bool, bool) {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b, true
		}
	}
	return false, false
}

func desktopToolApprovalMode(yolo bool, explicit string) (string, error) {
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

func desktopSessionOptions(sessionName, toolApprovalMode string) map[string]string {
	body := map[string]string{}
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

func desktopSubmitBody(prompt, sessionID, toolApprovalMode string) map[string]string {
	body := map[string]string{"prompt": prompt}
	if strings.TrimSpace(sessionID) != "" {
		body["sessionId"] = strings.TrimSpace(sessionID)
	}
	if toolApprovalMode != "" {
		body["toolApprovalMode"] = toolApprovalMode
	}
	return body
}

type desktopAnswerValues []string

func (v *desktopAnswerValues) String() string { return strings.Join(*v, ",") }

func (v *desktopAnswerValues) Set(value string) error {
	*v = append(*v, value)
	return nil
}

type desktopQuestionAnswer struct {
	QuestionID string   `json:"questionId"`
	Selected   []string `json:"selected"`
}

func parseDesktopAnswers(values []string) ([]desktopQuestionAnswer, error) {
	answers := make([]desktopQuestionAnswer, 0, len(values))
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
		answers = append(answers, desktopQuestionAnswer{QuestionID: questionID, Selected: []string{label}})
	}
	if len(answers) == 0 {
		return nil, fmt.Errorf("at least one --answer is required")
	}
	return answers, nil
}

func printDesktopSessionReport(action string, result map[string]interface{}) {
	if sessionID := stringField(result, "sessionId"); sessionID != "" {
		fmt.Printf("SessionID: %s\n", sessionID)
	}
	if path := stringField(result, "path"); path != "" {
		fmt.Printf("%s session: %s\n", action, path)
	} else {
		fmt.Printf("%s session\n", action)
	}
	fields := []string{}
	if mode := stringField(result, "toolApprovalMode"); mode != "" {
		fields = append(fields, "toolApproval="+mode)
	}
	if runtimeMode := stringField(result, "mode"); runtimeMode != "" {
		fields = append(fields, "mode="+runtimeMode)
	}
	if running, ok := boolField(result, "running"); ok {
		fields = append(fields, fmt.Sprintf("running=%t", running))
	}
	if pending, ok := boolField(result, "pendingPrompt"); ok {
		fields = append(fields, fmt.Sprintf("pendingPrompt=%t", pending))
	}
	if len(fields) > 0 {
		fmt.Println("Status: " + strings.Join(fields, " "))
	}
}

func printDesktopStatus(result map[string]interface{}, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		return enc.Encode(result)
	}
	printDesktopSessionReport("Active", result)
	return nil
}

// switchWorkspace tells Desktop to switch to a given workspace directory.
func switchWorkspace(dir string) error {
	_, err := desktopPostJSON("/api/v1/workspace/switch", map[string]string{"dir": dir})
	return err
}

// pollSession polls a session file until activity settles or an approval is
// needed, printing new messages as they arrive. If the Desktop pauses for
// approval, it prompts the user and sends the answer back.
func pollSession(sessionID, sessionPath string) error {
	client, baseURL, err := desktopClient()
	if err != nil {
		return err
	}

	deadline := time.Now().Add(pollTimeout)
	prevMsgCount := 0
	quietTicks := 0
	approvalHandled := false

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out (waited %v)", pollTimeout)
		}

		// 1. Check session status — are we waiting for approval?
		status, err := checkSessionStatus(client, baseURL, sessionID)
		if err == nil && status.PendingPrompt && !approvalHandled {
			if status.PendingInteraction.Kind == "ask" {
				encoded, _ := json.MarshalIndent(status.PendingInteraction, "", "  ")
				fmt.Printf("\nDesktop is waiting for a structured answer:\n%s\n", encoded)
				fmt.Printf("Use `workground2 desktop answer --session %s --id <id> --answer 'q1=<option>'` and then poll status again.\n", sessionID)
				return nil
			}
			approvalHandled = true
			fmt.Println("\n⏸  Desktop 等待审批…")
			showLastToolCall(sessionPath, prevMsgCount)

			fmt.Print("\n批准? [y=允许 / n=拒绝 / c=取消] ")
			var answer string
			fmt.Scanln(&answer)
			answer = strings.ToLower(strings.TrimSpace(answer))

			switch answer {
			case "y", "yes", "allow", "批准":
				if err := approvePending(client, baseURL, sessionID, true); err != nil {
					return fmt.Errorf("send approval: %w", err)
				}
				fmt.Println("✅ 已批准，继续执行…")
			case "c", "cancel":
				if err := approvePending(client, baseURL, sessionID, false); err != nil {
					return fmt.Errorf("send cancel: %w", err)
				}
				fmt.Println("❌ 已拒绝，继续执行…")
			default:
				if err := approvePending(client, baseURL, sessionID, false); err != nil {
					return fmt.Errorf("send deny: %w", err)
				}
				fmt.Println("❌ 已拒绝，继续执行…")
			}
			approvalHandled = false
			quietTicks = 0
			time.Sleep(pollInterval)
			continue
		}

		// 2. Load session content.
		session, err := agent.LoadSession(sessionPath)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}

		var msgs []provider.Message
		for _, m := range session.Messages {
			if m.Role != provider.RoleSystem {
				msgs = append(msgs, m)
			}
		}

		if len(msgs) > prevMsgCount {
			for i := prevMsgCount; i < len(msgs); i++ {
				m := msgs[i]
				if i == prevMsgCount && i > 0 {
					fmt.Println()
				}
				roleLabel := strings.ToUpper(string(m.Role))
				// Skip printing tool call JSON noise.
				if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 {
					for _, tc := range m.ToolCalls {
						fmt.Printf("[TOOL] %s(%s)\n", tc.Name, tc.Arguments)
					}
					if m.Content != "" {
						fmt.Printf("[ASSISTANT] %s\n", m.Content)
					}
				} else {
					fmt.Printf("[%s] %s\n", roleLabel, m.Content)
				}
			}
			prevMsgCount = len(msgs)
			quietTicks = 0

			last := msgs[len(msgs)-1]
			if last.Role == provider.RoleAssistant {
				quietTicks++
			}
			approvalHandled = false // new messages arrived, reset approval tracking
		} else {
			quietTicks++
		}

		if quietTicks >= pollMaxQuietTicks {
			return nil
		}

		time.Sleep(pollInterval)
	}
}

type sessionStatus struct {
	PendingPrompt      bool                      `json:"pendingPrompt"`
	PendingInteraction desktopPendingInteraction `json:"pendingInteraction"`
	Running            bool                      `json:"running"`
	Mode               string                    `json:"mode"`
	Path               string                    `json:"path"`
	ToolApprovalMode   string                    `json:"toolApprovalMode"`
}

type desktopPendingInteraction struct {
	Kind      string                   `json:"kind"`
	ID        string                   `json:"id"`
	Tool      string                   `json:"tool,omitempty"`
	Subject   string                   `json:"subject,omitempty"`
	Reason    string                   `json:"reason,omitempty"`
	Questions []desktopPendingQuestion `json:"questions,omitempty"`
}

type desktopPendingQuestion struct {
	ID          string                 `json:"id"`
	Header      string                 `json:"header"`
	Question    string                 `json:"question"`
	Options     []desktopPendingOption `json:"options"`
	MultiSelect bool                   `json:"multiSelect"`
}

type desktopPendingOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

func checkSessionStatus(client *http.Client, baseURL, sessionID string) (*sessionStatus, error) {
	endpoint := baseURL + "/api/v1/session/status?sessionId=" + url.QueryEscape(strings.TrimSpace(sessionID))
	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var st sessionStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

func approvePending(client *http.Client, baseURL, sessionID string, allow bool) error {
	b, _ := json.Marshal(map[string]any{"sessionId": strings.TrimSpace(sessionID), "allow": allow})
	resp, err := client.Post(baseURL+"/api/v1/session/approve", "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("approve: server returned %d", resp.StatusCode)
	}
	return nil
}

func showLastToolCall(sessionPath string, prevMsgCount int) {
	session, err := agent.LoadSession(sessionPath)
	if err != nil {
		return
	}
	var msgs []provider.Message
	for _, m := range session.Messages {
		if m.Role != provider.RoleSystem {
			msgs = append(msgs, m)
		}
	}
	// Find the last assistant message with tool calls.
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				fmt.Printf("  🔧 %s(%s)\n", tc.Name, tc.Arguments)
			}
			return
		}
	}
}

// ---------------------------------------------------------------------------
// workspaces
// ---------------------------------------------------------------------------

func desktopWorkspaces(args []string) int {
	fs := flag.NewFlagSet("desktop workspaces", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	client, baseURL, err := desktopClient()
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

	for _, ws := range workspaces {
		marker := " "
		if ws.Current {
			marker = "*"
		}
		name := ws.Name
		if name == "" {
			name = filepath.Base(ws.Path)
		}
		fmt.Printf(" %s  %-30s  %s\n", marker, name, ws.Path)
	}
	return 0
}

// ---------------------------------------------------------------------------
// open
// ---------------------------------------------------------------------------

func desktopOpen(args []string) int {
	fs := flag.NewFlagSet("desktop open", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: workground2 desktop open <path>")
		return 2
	}

	if _, err := desktopPostJSON("/api/v1/session/open", map[string]string{"path": fs.Arg(0)}); err != nil {
		fmt.Fprintf(os.Stderr, "desktop open: %v\n", err)
		return 1
	}
	fmt.Printf("Opened session: %s\n", filepath.Base(fs.Arg(0)))
	return 0
}

// ---------------------------------------------------------------------------
// new
// ---------------------------------------------------------------------------

func desktopNew(args []string) int {
	fs := flag.NewFlagSet("desktop new", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "target workspace directory")
	sessionName := fs.String("session-name", "", "display name for the new session")
	noWait := fs.Bool("no-wait", false, "do not wait for reply")
	yolo := fs.Bool("yolo", false, "run with tool approval mode yolo")
	toolApproval := fs.String("tool-approval", "", "tool approval mode: ask, auto, or yolo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	toolApprovalMode, err := desktopToolApprovalMode(*yolo, *toolApproval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop new: %v\n", err)
		return 2
	}

	body := desktopSessionOptions(*sessionName, toolApprovalMode)
	if body == nil {
		body = map[string]string{}
	}
	if strings.TrimSpace(*workspace) != "" {
		body["workspace"] = strings.TrimSpace(*workspace)
	}
	result, err := desktopPostJSON("/api/v1/session/new", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop new: %v\n", err)
		return 1
	}
	sessionID := stringField(result, "sessionId")
	if sessionID == "" {
		fmt.Fprintln(os.Stderr, "desktop new: response missing sessionId")
		return 1
	}
	printDesktopSessionReport("Created", result)

	if fs.NArg() > 0 {
		prompt := strings.Join(fs.Args(), " ")
		result, err = desktopPostJSON("/api/v1/session/submit", desktopSubmitBody(prompt, sessionID, toolApprovalMode))
		if err != nil {
			fmt.Fprintf(os.Stderr, "desktop new: submit: %v\n", err)
			return 1
		}
		printDesktopSessionReport("Submitted to", result)

		if !*noWait {
			sessionPath := stringField(result, "path")
			if sessionPath != "" {
				_ = pollSession(sessionID, sessionPath)
			}
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// submit
// ---------------------------------------------------------------------------

func desktopSubmit(args []string) int {
	fs := flag.NewFlagSet("desktop submit", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "target workspace directory")
	session := fs.String("session", "", "target SessionID")
	noWait := fs.Bool("no-wait", false, "do not wait for reply")
	yolo := fs.Bool("yolo", false, "run with tool approval mode yolo")
	toolApproval := fs.String("tool-approval", "", "tool approval mode: ask, auto, or yolo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: workground2 desktop submit [--workspace <dir> | --session <id>] <prompt>")
		return 2
	}
	toolApprovalMode, err := desktopToolApprovalMode(*yolo, *toolApproval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop submit: %v\n", err)
		return 2
	}

	sessionID := strings.TrimSpace(*session)
	if sessionID == "" && strings.TrimSpace(*workspace) != "" {
		body := desktopSessionOptions("", toolApprovalMode)
		if body == nil {
			body = map[string]string{}
		}
		body["workspace"] = strings.TrimSpace(*workspace)
		created, err := desktopPostJSON("/api/v1/session/new", body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "desktop submit: create background session: %v\n", err)
			return 1
		}
		sessionID = stringField(created, "sessionId")
	}
	if sessionID == "" {
		fmt.Fprintln(os.Stderr, "desktop submit: --session or --workspace is required")
		return 2
	}

	prompt := strings.Join(fs.Args(), " ")
	result, err := desktopPostJSON("/api/v1/session/submit", desktopSubmitBody(prompt, sessionID, toolApprovalMode))
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop submit: %v\n", err)
		return 1
	}
	printDesktopSessionReport("Submitted to", result)

	if !*noWait {
		sessionPath := stringField(result, "path")
		if sessionPath != "" {
			_ = pollSession(sessionID, sessionPath)
		}
	}
	return 0
}

func desktopAnswer(args []string) int {
	fs := flag.NewFlagSet("desktop answer", flag.ContinueOnError)
	sessionID := fs.String("session", "", "target SessionID")
	id := fs.String("id", "", "pending ask ID")
	var values desktopAnswerValues
	fs.Var(&values, "answer", "selected option as QUESTION_ID=OPTION_LABEL; repeat for multi-select")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*sessionID) == "" || strings.TrimSpace(*id) == "" {
		fmt.Fprintln(os.Stderr, "desktop answer: --session and --id are required")
		return 2
	}
	answers, err := parseDesktopAnswers(values)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop answer: %v\n", err)
		return 2
	}
	if _, err := desktopPostJSON("/api/v1/session/answer", map[string]any{
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

func desktopApprove(args []string) int {
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
	if _, err := desktopPostJSON("/api/v1/session/approve", map[string]any{
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

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func desktopStatus(args []string) int {
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
	result, err := desktopGetJSON(endpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktop status: %v\n", err)
		return 1
	}
	if err := printDesktopStatus(result, *asJSON); err != nil {
		fmt.Fprintf(os.Stderr, "desktop status: %v\n", err)
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// focus
// ---------------------------------------------------------------------------

func desktopFocus(args []string) int {
	fs := flag.NewFlagSet("desktop focus", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if _, err := desktopPostEmpty("/api/v1/window/focus"); err != nil {
		fmt.Fprintf(os.Stderr, "desktop focus: %v\n", err)
		return 1
	}
	fmt.Println("Desktop window focused")
	return 0
}
