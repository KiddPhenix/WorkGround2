package cdp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/domsnapshot"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/go-json-experiment/json/jsontext"

	"workground2/internal/browser"
)

var interactiveTags = map[string]bool{
	"a": true, "button": true, "input": true, "textarea": true,
	"select": true, "option": true, "details": true, "summary": true,
}

var interactiveRoles = map[string]bool{
	"button": true, "link": true, "textbox": true, "searchbox": true,
	"combobox": true, "listbox": true, "menuitem": true, "menuitemcheckbox": true,
	"menuitemradio": true, "option": true, "radio": true, "checkbox": true,
	"switch": true, "tab": true, "slider": true, "spinbutton": true,
	"textfield": true,
}

type axNodeInfo struct {
	Role     string
	Name     string
	Disabled bool
}

// observedNode is deliberately value-free. In particular, neither the DOM
// "value" attribute nor DOMSnapshot inputValue/textValue nor AX.Value is read.
type observedNode struct {
	TargetID      string
	BackendNodeID cdp.BackendNodeID
	FrameID       string
	Tag           string
	InputType     string
	Attributes    map[string]string
	Bounds        browser.Rect
	Role          string
	Name          string
	Placeholder   string
	Href          string
	Editable      bool
	Disabled      bool
	Checked       *bool
	Clickable     bool
	Visible       bool
	Order         int
}

func observe(ctx context.Context, activeTarget string, opts browser.ObserveOptions) (browser.Observation, error) {
	if opts.MaxTextChars <= 0 {
		opts.MaxTextChars = 20000
	}
	if opts.MaxElements <= 0 {
		opts.MaxElements = 400
	}

	var url, title string
	var documents []*domsnapshot.DocumentSnapshot
	var stringsTable []string
	var axNodes []*accessibility.Node
	var targetInfos []*target.Info
	err := chromedp.Run(ctx,
		chromedp.Location(&url),
		chromedp.Title(&title),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			documents, stringsTable, err = domsnapshot.CaptureSnapshot(
				[]string{"display", "visibility", "opacity", "pointer-events"},
			).WithIncludeDOMRects(true).WithIncludePaintOrder(true).Do(ctx)
			return err
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			targetInfos, err = target.GetTargets().Do(ctx)
			return err
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			axNodes, err = accessibility.GetFullAXTree().Do(ctx)
			return err
		}),
	)
	if err != nil {
		return browser.Observation{}, fmt.Errorf("capture DOMSnapshot/AX: %w", err)
	}

	axByBackend := buildAXIndex(axNodes)
	targets, warnings := mapDocumentTargets(documents, stringsTable, activeTarget, targetInfos)
	nodes, text := mergeSnapshot(documents, stringsTable, axByBackend, targets)
	nodes = filterAndSort(nodes)

	truncated := false
	if len(nodes) > opts.MaxElements {
		nodes = nodes[:opts.MaxElements]
		truncated = true
	}
	text = strings.Join(strings.Fields(text), " ")
	var textWasTruncated bool
	text, textWasTruncated = truncateRunes(text, opts.MaxTextChars)
	if textWasTruncated {
		truncated = true
	}

	observed := make([]browser.ObservedNode, 0, len(nodes))
	for _, n := range nodes {
		observed = append(observed, browser.ObservedNode{
			Ref: browser.NodeRef{
				TargetID:      n.TargetID,
				FrameID:       n.FrameID,
				BackendNodeID: int64(n.BackendNodeID),
				Bounds:        n.Bounds,
			},
			Role: n.Role, Tag: n.Tag, InputType: n.InputType, Name: n.Name,
			Placeholder: n.Placeholder, Href: n.Href, Disabled: n.Disabled,
			Checked: n.Checked, Editable: n.Editable,
		})
	}

	tabs, tabErr := listTabsInternal(ctx, activeTarget)
	if tabErr != nil {
		tabs = []browser.TabInfo{{ID: activeTarget, URL: url, Title: title, Active: true}}
	}
	return browser.Observation{
		URL: url, Title: title, ActiveTab: activeTarget, Tabs: tabs, Text: text,
		Nodes: observed, Warnings: warnings, Fingerprint: observationFingerprint(url, title, observed),
		Truncated: truncated,
	}, nil
}

func truncateRunes(value string, limit int) (string, bool) {
	if limit < 0 {
		limit = 0
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]), true
}

func buildAXIndex(nodes []*accessibility.Node) map[cdp.BackendNodeID]axNodeInfo {
	result := make(map[cdp.BackendNodeID]axNodeInfo)
	for _, node := range nodes {
		if node == nil || node.Ignored || node.BackendDOMNodeID == 0 {
			continue
		}
		info := axNodeInfo{}
		if node.Role != nil {
			info.Role = strings.ToLower(valueToString(node.Role.Value))
		}
		if node.Name != nil {
			info.Name = valueToString(node.Name.Value)
		}
		for _, prop := range node.Properties {
			if prop.Name == accessibility.PropertyNameDisabled {
				info.Disabled = valueToBool(prop.Value)
			}
		}
		result[node.BackendDOMNodeID] = info
	}
	return result
}

func mergeSnapshot(documents []*domsnapshot.DocumentSnapshot, table []string, ax map[cdp.BackendNodeID]axNodeInfo, targets map[string]string) ([]observedNode, string) {
	var result []observedNode
	var pageText strings.Builder
	order := 0
	for _, document := range documents {
		if document == nil || document.Nodes == nil || document.Layout == nil {
			continue
		}
		frameID := tableString(table, int64(document.FrameID))
		targetID := targets[frameID]
		layoutByNode := make(map[int64]int, len(document.Layout.NodeIndex))
		for layoutIndex, nodeIndex := range document.Layout.NodeIndex {
			layoutByNode[nodeIndex] = layoutIndex
			// Layout text for form controls may contain the current value. Only
			// ordinary DOM text nodes are allowed into the model-visible text.
			if nodeIndex >= 0 && int(nodeIndex) < len(document.Nodes.NodeType) &&
				document.Nodes.NodeType[nodeIndex] == 3 && layoutIndex < len(document.Layout.Text) {
				pageText.WriteString(tableString(table, int64(document.Layout.Text[layoutIndex])))
				pageText.WriteByte(' ')
			}
		}

		count := len(document.Nodes.BackendNodeID)
		for nodeIndex := 0; nodeIndex < count; nodeIndex++ {
			layoutIndex, hasLayout := layoutByNode[int64(nodeIndex)]
			if !hasLayout || layoutIndex >= len(document.Layout.Bounds) {
				continue
			}
			attrs := snapshotAttributes(document.Nodes, nodeIndex, table)
			tag := strings.ToLower(snapshotStringAt(document.Nodes.NodeName, nodeIndex, table))
			bounds := snapshotRect(document.Layout.Bounds[layoutIndex])
			styles := snapshotStyles(document.Layout, layoutIndex, table)
			n := observedNode{
				BackendNodeID: document.Nodes.BackendNodeID[nodeIndex], FrameID: frameID, TargetID: targetID,
				Tag: tag, Attributes: attrs, Bounds: bounds, Order: order,
				InputType: strings.ToLower(attrs["type"]), Placeholder: attrs["placeholder"],
				Href: attrs["href"], Role: strings.ToLower(attrs["role"]),
				Visible: isVisible(bounds, styles),
			}
			order++
			if n.Tag == "input" && n.InputType == "" {
				n.InputType = "text"
			}
			_, n.Disabled = attrs["disabled"]
			if _, ok := attrs["checked"]; ok {
				checked := true
				n.Checked = &checked
			}
			n.Clickable = rareBool(document.Nodes.IsClickable, int64(nodeIndex))
			contentEditable, hasCE := attrs["contenteditable"]
			isCE := hasCE && (contentEditable == "" || strings.EqualFold(contentEditable, "true"))
			n.Editable = n.Tag == "textarea" || n.Tag == "select" || isCE ||
				(n.Tag == "input" && editableInputType(n.InputType))
			if info, ok := ax[n.BackendNodeID]; ok {
				if n.Role == "" {
					n.Role = info.Role
				}
				n.Name = info.Name
				n.Disabled = n.Disabled || info.Disabled
			}
			result = append(result, n)
		}
	}
	return result, pageText.String()
}

// mapDocumentTargets keeps the frame identity explicit. DOMSnapshot returns
// child documents but does not identify an out-of-process iframe target. V1
// binds them to the active target and emits a warning instead of silently
// pretending that cross-target coverage is complete.
func mapDocumentTargets(documents []*domsnapshot.DocumentSnapshot, table []string, activeTarget string, infos []*target.Info) (map[string]string, []browser.StateWarning) {
	targets := make(map[string]string, len(documents))
	attachedFrames := make(map[string]string)
	for _, info := range infos {
		if info == nil || info.Type != "iframe" {
			continue
		}
		// Chromium identifies OOPIF targets by their frame target ID.
		attachedFrames[string(info.TargetID)] = string(info.TargetID)
	}
	var warnings []browser.StateWarning
	for index, document := range documents {
		if document == nil {
			continue
		}
		frameID := tableString(table, int64(document.FrameID))
		if index == 0 {
			targets[frameID] = activeTarget
		} else if targetID := attachedFrames[frameID]; targetID != "" {
			targets[frameID] = targetID
		} else {
			// An OOPIF has its own CDP target. Until that target is attached and
			// verified, never route its backend node IDs through the main target.
			targets[frameID] = ""
			warnings = append(warnings, browser.StateWarning{
				Code: "frame_target_unverified", FrameID: frameID,
				Message: "child frame is visible in DOMSnapshot but its out-of-process target mapping is not verified",
			})
		}
	}
	return targets, warnings
}

func filterAndSort(nodes []observedNode) []observedNode {
	seen := make(map[cdp.BackendNodeID]bool)
	filtered := make([]observedNode, 0, len(nodes))
	for _, node := range nodes {
		if node.BackendNodeID == 0 || seen[node.BackendNodeID] || !isInteractable(node) {
			continue
		}
		seen[node.BackendNodeID] = true
		filtered = append(filtered, node)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Bounds.Y != filtered[j].Bounds.Y {
			return filtered[i].Bounds.Y < filtered[j].Bounds.Y
		}
		if filtered[i].Bounds.X != filtered[j].Bounds.X {
			return filtered[i].Bounds.X < filtered[j].Bounds.X
		}
		return filtered[i].Order < filtered[j].Order
	})
	return filtered
}

func isInteractable(node observedNode) bool {
	if node.TargetID == "" || !node.Visible || node.Disabled || node.Bounds.Width <= 0 || node.Bounds.Height <= 0 {
		return false
	}
	if node.Tag == "input" && node.InputType == "hidden" {
		return false
	}
	if interactiveTags[node.Tag] || interactiveRoles[node.Role] || node.Clickable || node.Editable {
		return true
	}
	_, hasTabIndex := node.Attributes["tabindex"]
	return hasTabIndex
}

func editableInputType(inputType string) bool {
	switch inputType {
	case "hidden", "checkbox", "radio", "file", "image", "submit", "reset", "button":
		return false
	default:
		return true
	}
}

func isVisible(bounds browser.Rect, styles map[string]string) bool {
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return false
	}
	if strings.EqualFold(styles["display"], "none") || strings.EqualFold(styles["visibility"], "hidden") || styles["opacity"] == "0" {
		return false
	}
	return true
}

func snapshotAttributes(nodes *domsnapshot.NodeTreeSnapshot, index int, table []string) map[string]string {
	result := make(map[string]string)
	if index >= len(nodes.Attributes) {
		return result
	}
	indices := nodes.Attributes[index]
	for i := 0; i+1 < len(indices); i += 2 {
		name := strings.ToLower(tableString(table, indices[i]))
		// Values must never enter Observation, logs, fingerprints, or tests.
		if name == "value" {
			continue
		}
		result[name] = tableString(table, indices[i+1])
	}
	return result
}

func snapshotStyles(layout *domsnapshot.LayoutTreeSnapshot, index int, table []string) map[string]string {
	result := make(map[string]string)
	if index >= len(layout.Styles) {
		return result
	}
	names := []string{"display", "visibility", "opacity", "pointer-events"}
	for i, valueIndex := range layout.Styles[index] {
		if i < len(names) {
			result[names[i]] = tableString(table, valueIndex)
		}
	}
	return result
}

func snapshotRect(rect domsnapshot.Rectangle) browser.Rect {
	if len(rect) < 4 {
		return browser.Rect{}
	}
	return browser.Rect{X: rect[0], Y: rect[1], Width: rect[2], Height: rect[3]}
}

func snapshotStringAt(values []domsnapshot.StringIndex, index int, table []string) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return tableString(table, int64(values[index]))
}

func tableString(table []string, index int64) string {
	if index < 0 || int(index) >= len(table) {
		return ""
	}
	return table[index]
}

func rareBool(data *domsnapshot.RareBooleanData, index int64) bool {
	if data == nil {
		return false
	}
	for _, candidate := range data.Index {
		if candidate == index {
			return true
		}
	}
	return false
}

func observationFingerprint(url, title string, nodes []browser.ObservedNode) string {
	h := sha256.New()
	fmt.Fprint(h, url, "\x00", title)
	for _, node := range nodes {
		fmt.Fprintf(h, "\x00%d\x00%s\x00%s", node.Ref.BackendNodeID, node.Tag, node.Role)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func listTabsInternal(ctx context.Context, activeTarget string) ([]browser.TabInfo, error) {
	var tabs []browser.TabInfo
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		targets, err := target.GetTargets().Do(ctx)
		if err != nil {
			return err
		}
		for _, info := range targets {
			if info.Type == "page" {
				tabs = append(tabs, browser.TabInfo{ID: string(info.TargetID), URL: info.URL, Title: info.Title, Active: string(info.TargetID) == activeTarget})
			}
		}
		return nil
	}))
	return tabs, err
}

func valueToString(value jsontext.Value) string {
	if len(value) == 0 {
		return ""
	}
	s := value.String()
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return s
}

func valueToBool(value *accessibility.Value) bool {
	return value != nil && value.Value != nil && value.Value.String() == "true"
}
