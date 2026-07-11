// Command workground2-draw-addon is the separately compiled MCP runtime for the
// draw-tool AddOn package. The host loads it from an installed AddOn manifest and
// talks to it over stdio JSON-RPC.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"workground2/pkg/drawaddon"
)

var version = "dev"

const protocolVersion = "2024-11-05"

type request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	log.SetPrefix("workground2-draw-addon: ")
	log.SetFlags(0)
	log.SetOutput(os.Stderr)
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	home := addonHome()
	if home == "" {
		log.Fatal("WORKGROUND2_HOME or WorkGround2_HOME is required")
	}
	if err := serve(os.Stdin, os.Stdout, drawaddon.NewTool(home)); err != nil {
		log.Fatal(err)
	}
}

func addonHome() string {
	for _, key := range []string{"WORKGROUND2_HOME", "WorkGround2_HOME"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func serve(in io.Reader, out io.Writer, tool *drawaddon.DrawImageTool) error {
	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	defer w.Flush()
	for {
		line, err := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			if handleErr := handleLine(line, w, tool); handleErr != nil {
				return handleErr
			}
			if flushErr := w.Flush(); flushErr != nil {
				return flushErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func handleLine(line []byte, w *bufio.Writer, tool *drawaddon.DrawImageTool) error {
	var req request
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(line))), &req); err != nil {
		log.Printf("skipping unparseable line: %v", err)
		return nil
	}
	if req.ID == nil {
		return nil
	}
	resp := response{JSONRPC: "2.0", ID: *req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{"name": "workground2-draw-addon", "version": version},
		}
	case "tools/list":
		resp.Result = map[string]any{"tools": []map[string]any{{
			"name":        "draw_image",
			"description": tool.Description(),
			"inputSchema": json.RawMessage(tool.Schema()),
			"annotations": map[string]any{"readOnlyHint": false, "title": "draw_image"},
		}}}
	case "tools/call":
		resp.Result, resp.Error = callTool(req.Params, tool)
	case "panel/query":
		resp.Result, resp.Error = panelQuery(tool)
	case "panel/action":
		resp.Result, resp.Error = panelAction(req.Params, tool)
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func callTool(params json.RawMessage, tool *drawaddon.DrawImageTool) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	if p.Name != "draw_image" {
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
	}
	if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
		p.Arguments = json.RawMessage(`{}`)
	}
	text, err := tool.Execute(context.Background(), p.Arguments)
	return textResult(text, err != nil), nil
}

func textResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

// ── panel/query and panel/action handlers ──────────────────────────────────

func panelQuery(tool *drawaddon.DrawImageTool) (any, *rpcError) {
	records, err := tool.PanelQuery()
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	return map[string]any{"records": records, "form": map[string]any{}}, nil
}

func panelAction(params json.RawMessage, tool *drawaddon.DrawImageTool) (any, *rpcError) {
	var p struct {
		ActionID string         `json:"actionId"`
		Form     map[string]any `json:"form"`
		RecordID string         `json:"recordId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	result, err := tool.PanelAction(context.Background(), p.ActionID, p.Form, p.RecordID)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	return result, nil
}
