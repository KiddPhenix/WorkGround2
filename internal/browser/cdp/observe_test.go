package cdp

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/domsnapshot"
	"github.com/chromedp/cdproto/target"
	"github.com/go-json-experiment/json/jsontext"

	"workground2/internal/browser"
)

type stringTable struct {
	values []string
	index  map[string]domsnapshot.StringIndex
}

func newStringTable() *stringTable {
	return &stringTable{index: make(map[string]domsnapshot.StringIndex)}
}

func (s *stringTable) add(value string) domsnapshot.StringIndex {
	if index, ok := s.index[value]; ok {
		return index
	}
	index := domsnapshot.StringIndex(len(s.values))
	s.values = append(s.values, value)
	s.index[value] = index
	return index
}

func (s *stringTable) attrs(values ...string) domsnapshot.ArrayOfStrings {
	result := make(domsnapshot.ArrayOfStrings, len(values))
	for i, value := range values {
		result[i] = int64(s.add(value))
	}
	return result
}

func TestMergeSnapshotUsesLayoutAXAndRedactsValues(t *testing.T) {
	table := newStringTable()
	frame := table.add("frame-main")
	names := []domsnapshot.StringIndex{table.add("INPUT"), table.add("BUTTON"), table.add("#text"), table.add("INPUT"), table.add("BUTTON")}
	secret := "super-secret-password"
	attrs := []domsnapshot.ArrayOfStrings{
		table.attrs("type", "password", "value", secret, "placeholder", "Password"),
		table.attrs("id", "submit"), nil,
		table.attrs("type", "hidden"), table.attrs("disabled", ""),
	}
	visibleStyle := domsnapshot.ArrayOfStrings{int64(table.add("block")), int64(table.add("visible")), int64(table.add("1")), int64(table.add("auto"))}
	document := &domsnapshot.DocumentSnapshot{
		FrameID: frame,
		Nodes: &domsnapshot.NodeTreeSnapshot{
			NodeType:      []int64{1, 1, 3, 1, 1},
			NodeName:      names,
			BackendNodeID: []cdp.BackendNodeID{2, 1, 30, 3, 4},
			Attributes:    attrs,
			InputValue:    &domsnapshot.RareStringData{Index: []int64{0}, Value: []domsnapshot.StringIndex{table.add(secret)}},
			IsClickable:   &domsnapshot.RareBooleanData{Index: []int64{1}},
		},
		Layout: &domsnapshot.LayoutTreeSnapshot{
			NodeIndex: []int64{0, 1, 2, 3, 4},
			Bounds:    []domsnapshot.Rectangle{{0, 10, 100, 20}, {0, 20, 100, 20}, {0, 30, 100, 20}, {0, 40, 10, 10}, {0, 50, 100, 20}},
			Text:      []domsnapshot.StringIndex{table.add(secret), table.add("Submit"), table.add("Public page text"), table.add(""), table.add("Disabled")},
			Styles:    []domsnapshot.ArrayOfStrings{visibleStyle, visibleStyle, visibleStyle, visibleStyle, visibleStyle},
		},
	}
	ax := map[cdp.BackendNodeID]axNodeInfo{
		1: {Role: "button", Name: "Submit"},
		2: {Role: "textbox", Name: "Password"},
	}
	targets, warnings := mapDocumentTargets([]*domsnapshot.DocumentSnapshot{document}, table.values, "target", nil)
	if len(warnings) != 0 || targets["frame-main"] != "target" {
		t.Fatalf("root frame mapping: targets=%v warnings=%v", targets, warnings)
	}
	nodes, text := mergeSnapshot([]*domsnapshot.DocumentSnapshot{document}, table.values, ax, targets)
	filtered := filterAndSort(nodes)
	if len(filtered) != 2 {
		t.Fatalf("interactive nodes = %d, want 2: %+v", len(filtered), filtered)
	}
	if filtered[0].BackendNodeID != 2 || filtered[0].InputType != "password" || filtered[0].Bounds.Y != 10 {
		t.Fatalf("password node lost type/layout: %+v", filtered[0])
	}
	if filtered[1].BackendNodeID != 1 || filtered[1].Role != "button" || filtered[1].Name != "Submit" {
		t.Fatalf("AX merge failed: %+v", filtered[1])
	}
	serialized := fmt.Sprintf("%+v %s", nodes, text)
	if strings.Contains(serialized, secret) {
		t.Fatalf("secret leaked through snapshot: %s", serialized)
	}
	if strings.TrimSpace(text) != "Public page text" {
		t.Fatalf("page text = %q", text)
	}
}

func TestMapDocumentTargetsWarnsForUnverifiedChildFrame(t *testing.T) {
	table := newStringTable()
	documents := []*domsnapshot.DocumentSnapshot{
		{FrameID: table.add("root")},
		{FrameID: table.add("child")},
	}
	targets, warnings := mapDocumentTargets(documents, table.values, "active-target", nil)
	if targets["root"] != "active-target" || targets["child"] != "" {
		t.Fatalf("unverified child must not map to active target: %v", targets)
	}
	if len(warnings) != 1 || warnings[0].Code != "frame_target_unverified" || warnings[0].FrameID != "child" {
		t.Fatalf("child frame must be explicitly incomplete: %+v", warnings)
	}
	child := observedNode{BackendNodeID: 9, FrameID: "child", TargetID: targets["child"], Visible: true, Tag: "button", Bounds: browser.Rect{Width: 10, Height: 10}, Attributes: map[string]string{}}
	if got := filterAndSort([]observedNode{child}); len(got) != 0 {
		t.Fatalf("unverified child node became actionable: %+v", got)
	}
}

func TestMapDocumentTargetsUsesAttachedIframeTarget(t *testing.T) {
	table := newStringTable()
	documents := []*domsnapshot.DocumentSnapshot{{FrameID: table.add("root")}, {FrameID: table.add("child")}}
	infos := []*target.Info{{TargetID: target.ID("child"), Type: "iframe", Attached: true}}
	targets, warnings := mapDocumentTargets(documents, table.values, "active", infos)
	if targets["child"] != "child" || len(warnings) != 0 {
		t.Fatalf("attached iframe mapping=%v warnings=%v", targets, warnings)
	}
}

func TestFilterAndSortDeduplicatesAndUsesDOMOrder(t *testing.T) {
	base := observedNode{TargetID: "target", Visible: true, Tag: "button", Bounds: browser.Rect{Width: 10, Height: 10}, Attributes: map[string]string{}}
	nodes := []observedNode{
		withObserved(base, 1, 20, 20, 2),
		withObserved(base, 2, 10, 20, 1),
		withObserved(base, 1, 0, 0, 0), // duplicate backend ID
		withObserved(base, 3, 10, 20, 0),
	}
	filtered := filterAndSort(nodes)
	got := []cdp.BackendNodeID{filtered[0].BackendNodeID, filtered[1].BackendNodeID, filtered[2].BackendNodeID}
	want := []cdp.BackendNodeID{3, 2, 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order/dedup = %v, want %v", got, want)
	}
}

func withObserved(node observedNode, id cdp.BackendNodeID, x, y float64, order int) observedNode {
	node.BackendNodeID = id
	node.Bounds.X, node.Bounds.Y, node.Order = x, y, order
	return node
}

func TestBuildAXIndexIgnoresAXValue(t *testing.T) {
	secret := jsontext.Value(`"secret-value"`)
	name := jsontext.Value(`"User name"`)
	role := jsontext.Value(`"textbox"`)
	index := buildAXIndex([]*accessibility.Node{{
		BackendDOMNodeID: 7,
		Role:             &accessibility.Value{Value: role},
		Name:             &accessibility.Value{Value: name},
		Value:            &accessibility.Value{Value: secret},
	}})
	if index[7].Name != "User name" || index[7].Role != "textbox" {
		t.Fatalf("unexpected AX index: %+v", index[7])
	}
	if strings.Contains(fmt.Sprint(index), "secret-value") {
		t.Fatal("AX value leaked")
	}
}

func TestTruncateTextPreservesUTF8Runes(t *testing.T) {
	got, truncated := truncateRunes("你好世界", 3)
	if !truncated || got != "你好世" || !utf8.ValidString(got) || utf8.RuneCountInString(got) != 3 {
		t.Fatalf("invalid rune truncation %q truncated=%v", got, truncated)
	}
}
