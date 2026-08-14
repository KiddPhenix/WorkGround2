package browsertool_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"workground2/internal/browser"
	"workground2/internal/tool"
	browsertool "workground2/internal/tool/browser"
)

// stateBudget mirrors the agent's per-tool output cap (maxToolOutputBytes)
// passed into the limiter at runtime. Keeping it local avoids exporting the
// agent's constant while still asserting the exact acceptance bound.
const stateBudget = 32 * 1024

// stateTool returns the browser_state tool from a fresh tool set.
func stateTool(t *testing.T) tool.Tool {
	t.Helper()
	svc := newFakeService()
	for _, tl := range browsertool.NewTools(svc) {
		if tl.Name() == "browser_state" {
			return tl
		}
	}
	t.Fatal("browser_state tool not found")
	return nil
}

// stateLimiter returns browser_state asserted to implement tool.OutputLimiter.
func stateLimiter(t *testing.T) tool.OutputLimiter {
	t.Helper()
	lm, ok := stateTool(t).(tool.OutputLimiter)
	if !ok {
		t.Fatal("browser_state must implement tool.OutputLimiter")
	}
	return lm
}

func marshalStatePayload(t *testing.T, st browser.PageState) string {
	t.Helper()
	b, err := json.Marshal(browsertool.ToolResponse[browser.PageState]{OK: true, Result: &st})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// bigState builds a state whose encoded payload is far over the budget: lots
// of multibyte page text and many multibyte-labelled elements.
func bigState() browser.PageState {
	st := browser.PageState{
		SessionID:  "s1",
		Revision:   7,
		URL:        "https://example.com/文档",
		Title:      "中文标题",
		ActiveTab:  "tab-1",
		CapturedAt: time.Now().UTC(),
		Tabs: []browser.TabInfo{
			{ID: "tab-1", URL: "https://example.com/文档", Title: "中文标题", Active: true},
			{ID: "tab-2", URL: "https://example.com/other", Title: "第二页"},
		},
		Text: strings.Repeat("页面文本内容段落。", 3000), // 8 runes × 3 bytes × 3000 ≈ 72 KB
	}
	for i := 1; i <= 500; i++ {
		st.Elements = append(st.Elements, browser.Element{
			Index: i,
			Role:  "button",
			Tag:   "button",
			Name:  fmt.Sprintf("按钮%d", i),
			Bounds: browser.Rect{
				X: float64(i), Y: 1, Width: 100, Height: 40,
			},
		})
	}
	return st
}

// TestStateLimitOutputFitsBudget proves the acceptance contract: an oversized
// browser_state result stays within the cap, is still valid JSON that parses
// as ToolResponse[browser.PageState], keeps the envelope/metadata and only
// whole elements with stable indices, reports truncation, and never leaks the
// generic head/tail marker — even with multibyte text and element data.
func TestStateLimitOutputFitsBudget(t *testing.T) {
	lm := stateLimiter(t)
	st := bigState()
	s := marshalStatePayload(t, st)
	if len(s) <= stateBudget {
		t.Fatalf("test payload must exceed the budget: %d bytes", len(s))
	}

	out, ok := lm.LimitOutput(s, stateBudget)
	if !ok {
		t.Fatal("limiter declined an oversized browser_state result")
	}
	if len(out) > stateBudget {
		t.Fatalf("limited output %d bytes exceeds cap %d", len(out), stateBudget)
	}
	if !utf8.ValidString(out) {
		t.Fatal("limited output is not valid UTF-8")
	}
	if strings.Contains(out, "…[truncated") {
		t.Fatal("generic head/tail truncation marker leaked into shape-aware output")
	}

	var resp browsertool.ToolResponse[browser.PageState]
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("limited output does not parse as ToolResponse[browser.PageState]: %v\n%q", err, out)
	}
	if !resp.OK || resp.Result == nil {
		t.Fatalf("envelope must stay ok=true with a result: %+v", resp)
	}
	r := resp.Result
	if !r.Truncated {
		t.Fatal("truncated must be set on the limited state")
	}
	if r.Revision != st.Revision || r.URL != st.URL || r.Title != st.Title ||
		r.SessionID != st.SessionID || r.ActiveTab != st.ActiveTab || len(r.Tabs) != len(st.Tabs) {
		t.Fatalf("metadata must survive: %+v", r)
	}
	if len(r.Elements) >= len(st.Elements) {
		t.Fatalf("no elements elided: kept %d of %d", len(r.Elements), len(st.Elements))
	}
	for i, el := range r.Elements {
		if el.Index != st.Elements[i].Index {
			t.Fatalf("element %d index %d != original %d — indices must stay stable",
				i, el.Index, st.Elements[i].Index)
		}
		if el.Name != st.Elements[i].Name {
			t.Fatalf("element %d mangled: %q != %q", i, el.Name, st.Elements[i].Name)
		}
	}
	// Pagination hints: the next page starts at the first elided element's
	// original index, and the remaining count matches the elided count.
	if r.NextElementIndex != st.Elements[len(r.Elements)].Index {
		t.Fatalf("next_element_index = %d, want %d (first elided element's index)",
			r.NextElementIndex, st.Elements[len(r.Elements)].Index)
	}
	if r.RemainingElements != len(st.Elements)-len(r.Elements) {
		t.Fatalf("remaining_elements = %d, want %d",
			r.RemainingElements, len(st.Elements)-len(r.Elements))
	}
	if !strings.HasPrefix(st.Text, r.Text) {
		t.Fatal("text must be a rune prefix of the original (compacted, never split)")
	}
	if len(r.Warnings) == 0 {
		t.Fatal("expected a structured truncation warning")
	}
	want := r.Warnings[len(r.Warnings)-1]
	if want.Code != "output_truncated" {
		t.Fatalf("warning code = %q, want output_truncated", want.Code)
	}
	if !strings.Contains(want.Message, "elided") {
		t.Fatalf("warning must report the elided count: %q", want.Message)
	}
}

// TestStateLimitOutputCompactsTextWithoutElements proves the text-first step:
// a huge text with no elements fits via rune-prefix compaction alone.
func TestStateLimitOutputCompactsTextWithoutElements(t *testing.T) {
	lm := stateLimiter(t)
	st := browser.PageState{
		SessionID: "s2",
		Revision:  1,
		URL:       "https://example.com",
		Title:     "Title",
		Text:      strings.Repeat("很长很长的一段页面文本内容。", 5000), // ≈ 130 KB
	}
	s := marshalStatePayload(t, st)

	out, ok := lm.LimitOutput(s, stateBudget)
	if !ok {
		t.Fatal("limiter declined a text-only oversized state")
	}
	if len(out) > stateBudget {
		t.Fatalf("limited output %d bytes exceeds cap %d", len(out), stateBudget)
	}
	var resp browsertool.ToolResponse[browser.PageState]
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("output does not parse: %v", err)
	}
	if !resp.Result.Truncated {
		t.Fatal("truncated must be set")
	}
	if !strings.HasPrefix(st.Text, resp.Result.Text) || len(resp.Result.Text) >= len(st.Text) {
		t.Fatalf("text must be a compacted prefix: kept %d of %d bytes",
			len(resp.Result.Text), len(st.Text))
	}
	if len(resp.Result.Warnings) != 1 || !strings.Contains(resp.Result.Warnings[0].Message, "compacted") {
		t.Fatalf("expected a text-compaction warning: %+v", resp.Result.Warnings)
	}
	// No elements were present, so no next page can be advertised.
	if resp.Result.NextElementIndex != 0 || resp.Result.RemainingElements != 0 {
		t.Fatalf("text-only page must not advertise elements: next=%d remaining=%d",
			resp.Result.NextElementIndex, resp.Result.RemainingElements)
	}
}

// TestStateLimitOutputUnderCap passes small payloads through untouched — the
// limiter must never rewrite content that already fits.
func TestStateLimitOutputUnderCap(t *testing.T) {
	lm := stateLimiter(t)
	st := browser.PageState{
		SessionID: "s3",
		Revision:  3,
		URL:       "https://example.com",
		Title:     "T",
		Elements:  []browser.Element{{Index: 1, Tag: "a", Href: "https://example.com"}},
	}
	s := marshalStatePayload(t, st)
	out, ok := lm.LimitOutput(s, stateBudget)
	if !ok || out != s {
		t.Fatalf("under-cap payload must pass through untouched: ok=%v changed=%v", ok, out != s)
	}
}

// TestStateLimitOutputDeclinesForeignPayload keeps the limiter honest: payloads
// that are not the browser_state envelope are declined so the agent's generic
// truncation handles them.
func TestStateLimitOutputDeclinesForeignPayload(t *testing.T) {
	lm := stateLimiter(t)
	if out, ok := lm.LimitOutput(strings.Repeat("plain text ", 10000), stateBudget); ok {
		t.Fatalf("non-envelope payload must be declined, got ok=true out=%q", out)
	}
	bigErr := `{"ok":false,"error":{"code":"config","message":"` + strings.Repeat("boom ", 10000) + `"}}`
	if out, ok := lm.LimitOutput(bigErr, stateBudget); ok {
		t.Fatalf("oversized error envelope must be declined, got ok=true out=%q", out)
	}
}

// TestStateExecutePassesPaginationArgs proves revision and element_start flow
// from the tool arguments into the StateRequest the service receives.
func TestStateExecutePassesPaginationArgs(t *testing.T) {
	svc := newFakeService()
	var got browser.StateRequest
	svc.stateFn = func(ctx context.Context, owner string, req browser.StateRequest) (browser.PageState, error) {
		got = req
		return browser.PageState{
			SessionID: "s", Revision: 9, URL: "https://example.com",
			Elements: []browser.Element{{Index: 5, Tag: "a"}},
		}, nil
	}
	var theTool tool.Tool
	for _, tl := range browsertool.NewTools(svc) {
		if tl.Name() == "browser_state" {
			theTool = tl
		}
	}
	out, err := theTool.Execute(context.Background(), json.RawMessage(`{"refresh":false,"revision":9,"element_start":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Refresh {
		t.Fatal("refresh must be false")
	}
	if got.Revision == nil || *got.Revision != 9 {
		t.Fatalf("revision = %v, want 9", got.Revision)
	}
	if got.ElementStart != 5 {
		t.Fatalf("element_start = %d, want 5", got.ElementStart)
	}
	var resp browsertool.ToolResponse[browser.PageState]
	if err := json.Unmarshal([]byte(out), &resp); err != nil || !resp.OK {
		t.Fatalf("output is not an ok envelope: %v %q", err, out)
	}
}

// TestStateExecuteStaleRevisionError surfaces a stale revision from the
// service as a structured stale_state error envelope.
func TestStateExecuteStaleRevisionError(t *testing.T) {
	svc := newFakeService()
	svc.stateFn = func(ctx context.Context, owner string, req browser.StateRequest) (browser.PageState, error) {
		return browser.PageState{}, browser.NewError(browser.ErrStaleState, "revision 1 does not match current 2", nil)
	}
	var theTool tool.Tool
	for _, tl := range browsertool.NewTools(svc) {
		if tl.Name() == "browser_state" {
			theTool = tl
		}
	}
	out, err := theTool.Execute(context.Background(), json.RawMessage(`{"refresh":false,"revision":1}`))
	if err == nil {
		t.Fatal("expected an error for a stale revision")
	}
	var resp browsertool.ToolResponse[browser.PageState]
	if uerr := json.Unmarshal([]byte(out), &resp); uerr != nil {
		t.Fatalf("error output is not JSON: %v", uerr)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != browser.ErrStaleState {
		t.Fatalf("expected a stale_state error envelope, got %+v", resp)
	}
}

// TestStateExecuteRejectsInvalidElementStart keeps invalid pagination
// arguments out of the service.
func TestStateExecuteRejectsInvalidElementStart(t *testing.T) {
	svc := newFakeService()
	var calls int
	svc.stateFn = func(ctx context.Context, owner string, req browser.StateRequest) (browser.PageState, error) {
		calls++
		return browser.PageState{}, nil
	}
	var theTool tool.Tool
	for _, tl := range browsertool.NewTools(svc) {
		if tl.Name() == "browser_state" {
			theTool = tl
		}
	}
	for _, raw := range []string{`{"element_start":0}`, `{"element_start":-1}`} {
		out, err := theTool.Execute(context.Background(), json.RawMessage(raw))
		if err == nil {
			t.Fatalf("expected an error for %s", raw)
		}
		if calls != 0 {
			t.Fatal("service must not be called with invalid element_start")
		}
		var resp browsertool.ToolResponse[browser.PageState]
		if uerr := json.Unmarshal([]byte(out), &resp); uerr != nil {
			t.Fatalf("error output is not JSON: %v", uerr)
		}
		if resp.OK || resp.Error == nil || resp.Error.Code != browser.ErrInvalidArguments {
			t.Fatalf("expected invalid_arguments error envelope, got %+v", resp)
		}
	}
}

// TestStatePaginationNextPageEndToEnd walks a large element list the way the
// model would: first page trimmed by the OutputLimiter reports
// next_element_index; each follow-up request pages the same snapshot from that
// index, keeps original element indices, and the walk ends exactly when all
// elements have been seen.
func TestStatePaginationNextPageEndToEnd(t *testing.T) {
	const total = 300
	all := make([]browser.Element, 0, total)
	for i := 1; i <= total; i++ {
		all = append(all, browser.Element{
			Index: i,
			Role:  "link",
			Tag:   "a",
			Name:  fmt.Sprintf("链接项 %d", i),
			Href:  fmt.Sprintf("https://example.com/item/%d", i),
			Bounds: browser.Rect{
				X: float64(i), Y: 1, Width: 200, Height: 40,
			},
		})
	}
	svc := newFakeService()
	svc.stateFn = func(ctx context.Context, owner string, req browser.StateRequest) (browser.PageState, error) {
		st := browser.PageState{
			SessionID: "s", Revision: 42,
			URL: "https://example.com", Title: "List",
			Text: strings.Repeat("列表页面内容段落。", 2000),
		}
		for _, el := range all {
			if req.ElementStart <= 0 || el.Index >= req.ElementStart {
				st.Elements = append(st.Elements, el)
			}
		}
		return st, nil
	}
	var theTool tool.Tool
	for _, tl := range browsertool.NewTools(svc) {
		if tl.Name() == "browser_state" {
			theTool = tl
		}
	}
	lm, ok := theTool.(tool.OutputLimiter)
	if !ok {
		t.Fatal("browser_state must implement tool.OutputLimiter")
	}
	ctx := context.Background()

	// 首包：完整输出被限制器裁剪，并给出下一页起点。
	raw, err := theTool.Execute(ctx, json.RawMessage(`{"refresh":true}`))
	if err != nil {
		t.Fatal(err)
	}
	page1, ok := lm.LimitOutput(raw, stateBudget)
	if !ok {
		t.Fatal("limiter declined the first page")
	}
	var r1 browsertool.ToolResponse[browser.PageState]
	if err := json.Unmarshal([]byte(page1), &r1); err != nil {
		t.Fatal(err)
	}
	if len(r1.Result.Elements) == 0 || len(r1.Result.Elements) >= total {
		t.Fatalf("first page kept %d of %d elements, want a trimmed non-empty page",
			len(r1.Result.Elements), total)
	}
	if r1.Result.NextElementIndex == 0 {
		t.Fatal("first page must advertise a next_element_index")
	}
	if r1.Result.RemainingElements != total-len(r1.Result.Elements) {
		t.Fatalf("first page remaining = %d, want %d",
			r1.Result.RemainingElements, total-len(r1.Result.Elements))
	}
	for i, el := range r1.Result.Elements {
		if el.Index != all[i].Index {
			t.Fatalf("first page element %d index %d != %d", i, el.Index, all[i].Index)
		}
	}

	// 第二页及之后：refresh=false + revision + element_start 从同一快照续页。
	seen := len(r1.Result.Elements)
	start := r1.Result.NextElementIndex
	for start > 0 {
		rawN, err := theTool.Execute(ctx, json.RawMessage(fmt.Sprintf(
			`{"refresh":false,"revision":42,"element_start":%d}`, start)))
		if err != nil {
			t.Fatal(err)
		}
		pageN, ok := lm.LimitOutput(rawN, stateBudget)
		if !ok {
			t.Fatal("limiter declined a later page")
		}
		var rN browsertool.ToolResponse[browser.PageState]
		if err := json.Unmarshal([]byte(pageN), &rN); err != nil {
			t.Fatal(err)
		}
		if rN.Result.Revision != 42 {
			t.Fatalf("later page revision %d, want 42 (same snapshot)", rN.Result.Revision)
		}
		if len(rN.Result.Elements) == 0 {
			t.Fatalf("page starting at %d returned no elements while %d remain", start, total-seen)
		}
		for i, el := range rN.Result.Elements {
			if el.Index != all[seen+i].Index {
				t.Fatalf("page element %d index %d != %d (original index)", i, el.Index, all[seen+i].Index)
			}
		}
		seen += len(rN.Result.Elements)
		start = rN.Result.NextElementIndex
	}
	if seen != total {
		t.Fatalf("walked %d of %d elements", seen, total)
	}
}
