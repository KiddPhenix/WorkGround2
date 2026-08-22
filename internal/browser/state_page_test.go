package browser_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"workground2/internal/browser"
)

// manyNodes builds a fake driver page exposing count interactive elements
// (indices 1..count).
func manyNodes(count int) []browser.ObservedNode {
	nodes := make([]browser.ObservedNode, 0, count)
	for i := 1; i <= count; i++ {
		nodes = append(nodes, browser.ObservedNode{
			Ref: browser.NodeRef{
				BackendNodeID: int64(i),
				TargetID:      "tab-1",
				Bounds:        browser.Rect{X: float64(i), Y: float64(i), Width: 100, Height: 30},
			},
			Role: "button", Tag: "button", Name: "btn",
		})
	}
	return nodes
}

// TestStatePaginationPagesSameSnapshot covers the paging contract end to end:
// first page, second page from the same snapshot with original indices,
// idempotent repeats, and the last page / past-the-end pages.
func TestStatePaginationPagesSameSnapshot(t *testing.T) {
	factory := newFakeFactory()
	factory.configure = func(d *fakeDriver) { d.nodes = manyNodes(25) }
	mgr, err := browser.NewManager(context.Background(), browser.Options{
		Factory: factory, IdleTimeout: 10 * time.Minute, MaxElements: 400,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if _, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
		t.Fatal(err)
	}

	// 首包：不带分页参数返回全部元素，且不广告下一页。
	first, err := mgr.State(context.Background(), "owner", browser.StateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Elements) != 25 {
		t.Fatalf("first page has %d elements, want 25", len(first.Elements))
	}
	if first.NextElementIndex != 0 || first.RemainingElements != 0 {
		t.Fatalf("full page must not advertise more elements: next=%d remaining=%d",
			first.NextElementIndex, first.RemainingElements)
	}
	rev := first.Revision

	// 第二页：refresh=false + revision + element_start 从同一快照返回 index >= 11 的元素。
	second, err := mgr.State(context.Background(), "owner", browser.StateRequest{
		Refresh: false, Revision: &rev, ElementStart: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != rev {
		t.Fatalf("second page revision %d != %d — must serve the same snapshot", second.Revision, rev)
	}
	if len(second.Elements) != 15 {
		t.Fatalf("second page has %d elements, want 15", len(second.Elements))
	}
	for i, el := range second.Elements {
		if el.Index != 11+i {
			t.Fatalf("second page element %d index %d, want %d (original indices preserved)", i, el.Index, 11+i)
		}
	}
	if !reflect.DeepEqual(first.Elements[10:], second.Elements) {
		t.Fatal("second page is not the tail of the first page")
	}

	// 重复页：同一快照、同一 element_start 的重复调用结果幂等。
	again, err := mgr.State(context.Background(), "owner", browser.StateRequest{
		Refresh: false, Revision: &rev, ElementStart: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, second) {
		t.Fatalf("duplicate page request is not idempotent:\n%+v\n%+v", second, again)
	}

	// 最后一页：element_start 指向最后一个元素。
	last, err := mgr.State(context.Background(), "owner", browser.StateRequest{
		Refresh: false, Revision: &rev, ElementStart: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Elements) != 1 || last.Elements[0].Index != 25 {
		t.Fatalf("last page = %+v, want just index 25", last.Elements)
	}

	// 越过末尾：空页而非错误。
	past, err := mgr.State(context.Background(), "owner", browser.StateRequest{
		Refresh: false, Revision: &rev, ElementStart: 26,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(past.Elements) != 0 {
		t.Fatalf("page past the end has %d elements, want 0", len(past.Elements))
	}
}

// TestStateStaleRevisionNeverRefreshes proves a revision-pinned request serves
// only the pinned snapshot: a mismatched revision or an invalidated snapshot
// returns stale_state and never triggers a refresh that substitutes fresh
// data — even when the caller asked for one.
func TestStateStaleRevisionNeverRefreshes(t *testing.T) {
	factory := newFakeFactory()
	factory.configure = func(d *fakeDriver) { d.nodes = manyNodes(10) }
	mgr, err := browser.NewManager(context.Background(), browser.Options{
		Factory: factory, IdleTimeout: 10 * time.Minute, MaxElements: 400,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	driver := factory.only(t)

	// 过期 revision：即使 refresh=true 也必须拒绝，而不是刷新后返回新数据。
	stale := opened.Revision - 1
	before := driver.observeCalls.Load()
	_, err = mgr.State(context.Background(), "owner", browser.StateRequest{
		Refresh: true, Revision: &stale,
	})
	if err == nil {
		t.Fatal("expected stale_state for a mismatched revision")
	}
	var be *browser.Error
	if !errors.As(err, &be) || be.Code != browser.ErrStaleState {
		t.Fatalf("expected stale_state, got %v", err)
	}
	if got := driver.observeCalls.Load(); got != before {
		t.Fatalf("stale revision request triggered %d refresh(s), want 0 — refresh must not substitute fresh data",
			got-before)
	}

	// 匹配的 revision：即使 refresh=true 也直接走快照，不触发观察。
	rev := opened.Revision
	if _, err := mgr.State(context.Background(), "owner", browser.StateRequest{
		Refresh: true, Revision: &rev,
	}); err != nil {
		t.Fatalf("matching pinned revision: %v", err)
	}
	if got := driver.observeCalls.Load(); got != before {
		t.Fatalf("pinned matching request triggered %d refresh(s), want 0", got-before)
	}

	// 快照失效后：同一 revision 请求返回 stale_state，同样不刷新。
	driver.invalCh <- browser.Invalidation{Kind: browser.InvalidationDocument, At: time.Now()}
	time.Sleep(20 * time.Millisecond)
	_, err = mgr.State(context.Background(), "owner", browser.StateRequest{
		Refresh: true, Revision: &rev,
	})
	if err == nil {
		t.Fatal("expected stale_state after snapshot invalidation")
	}
	if !errors.As(err, &be) || be.Code != browser.ErrStaleState {
		t.Fatalf("expected stale_state after invalidation, got %v", err)
	}
	if got := driver.observeCalls.Load(); got != before {
		t.Fatalf("invalidated revision request triggered %d refresh(s), want 0", got-before)
	}
}
