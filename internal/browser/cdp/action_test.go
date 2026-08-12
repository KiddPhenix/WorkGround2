package cdp

import (
	"context"
	"errors"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"

	"workground2/internal/browser"
)

type fakeClickBackend struct {
	boxModel    *dom.BoxModel
	boxErr      error
	hit         cdp.BackendNodeID
	hitErr      error
	mouseErrAt  input.MouseType
	fallbackErr error
	mouseCalls  []input.MouseType
	fallbacks   int
	containsHit bool
}

func (f *fakeClickBackend) resolve(context.Context, cdp.BackendNodeID) (cdp.BackendNodeID, error) {
	return 9, nil
}
func (f *fakeClickBackend) scrollIntoView(context.Context, cdp.BackendNodeID) error { return nil }
func (f *fakeClickBackend) box(context.Context, cdp.BackendNodeID) (*dom.BoxModel, error) {
	return f.boxModel, f.boxErr
}
func (f *fakeClickBackend) hitTest(context.Context, float64, float64) (cdp.BackendNodeID, error) {
	return f.hit, f.hitErr
}
func (f *fakeClickBackend) contains(context.Context, cdp.BackendNodeID, cdp.BackendNodeID) (bool, error) {
	return f.containsHit, nil
}
func (f *fakeClickBackend) mouse(_ context.Context, kind input.MouseType, _, _ float64) error {
	f.mouseCalls = append(f.mouseCalls, kind)
	if f.mouseErrAt == kind {
		return errors.New("mouse failed")
	}
	return nil
}
func (f *fakeClickBackend) domClick(context.Context, cdp.BackendNodeID) (bool, error) {
	f.fallbacks++
	return true, f.fallbackErr
}

func goodBox() *dom.BoxModel {
	return &dom.BoxModel{Content: dom.Quad{0, 0, 20, 0, 20, 20, 0, 20}}
}

func TestClickUsesHitTestedMousePath(t *testing.T) {
	backend := &fakeClickBackend{boxModel: goodBox(), hit: 42}
	method, err := clickUsing(context.Background(), backend, browser.NodeRef{BackendNodeID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if method != "mouse" || backend.fallbacks != 0 || len(backend.mouseCalls) != 3 {
		t.Fatalf("method=%s fallback=%d mouse=%v", method, backend.fallbacks, backend.mouseCalls)
	}
}

func TestClickFallsBackExactlyOnceWhenBoxUnavailable(t *testing.T) {
	backend := &fakeClickBackend{boxErr: errors.New("no layout")}
	method, err := clickUsing(context.Background(), backend, browser.NodeRef{BackendNodeID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if method != "dom_fallback" || backend.fallbacks != 1 || len(backend.mouseCalls) != 0 {
		t.Fatalf("method=%s fallback=%d mouse=%v", method, backend.fallbacks, backend.mouseCalls)
	}
}

func TestClickFallsBackExactlyOnceOnHitMismatchOrMouseFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hit        cdp.BackendNodeID
		mouseError input.MouseType
	}{
		{name: "occluded", hit: 77},
		{name: "mouse move failure", hit: 42, mouseError: input.MouseMoved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &fakeClickBackend{boxModel: goodBox(), hit: tc.hit, mouseErrAt: tc.mouseError}
			method, err := clickUsing(context.Background(), backend, browser.NodeRef{BackendNodeID: 42})
			if err != nil {
				t.Fatal(err)
			}
			if method != "dom_fallback" || backend.fallbacks != 1 {
				t.Fatalf("method=%s fallback=%d", method, backend.fallbacks)
			}
		})
	}
}

func TestClickDoesNotFallbackAfterMousePress(t *testing.T) {
	for _, failure := range []input.MouseType{input.MousePressed, input.MouseReleased} {
		backend := &fakeClickBackend{boxModel: goodBox(), hit: 42, mouseErrAt: failure}
		method, err := clickUsing(context.Background(), backend, browser.NodeRef{BackendNodeID: 42})
		var dispatched *browser.DispatchError
		if method != "mouse" || !errors.As(err, &dispatched) || !dispatched.Dispatched || backend.fallbacks != 0 {
			t.Fatalf("failure=%s method=%s fallback=%d err=%v", failure, method, backend.fallbacks, err)
		}
	}
}

func TestClickAcceptsDescendantHit(t *testing.T) {
	backend := &fakeClickBackend{boxModel: goodBox(), hit: 77, containsHit: true}
	method, err := clickUsing(context.Background(), backend, browser.NodeRef{BackendNodeID: 42})
	if err != nil || method != "mouse" || backend.fallbacks != 0 {
		t.Fatalf("descendant hit method=%s fallback=%d err=%v", method, backend.fallbacks, err)
	}
}

func TestClickReturnsBothPrimaryAndFallbackError(t *testing.T) {
	backend := &fakeClickBackend{boxErr: errors.New("no box"), fallbackErr: errors.New("not HTMLElement")}
	method, err := clickUsing(context.Background(), backend, browser.NodeRef{BackendNodeID: 42})
	if err == nil || method != "dom_fallback" || backend.fallbacks != 1 {
		t.Fatalf("method=%s error=%v fallback=%d", method, err, backend.fallbacks)
	}
	var dispatched *browser.DispatchError
	if !errors.As(err, &dispatched) || !dispatched.Dispatched {
		t.Fatalf("fallback action error must be outcome-unknown: %v", err)
	}
}

func TestValidateTypeTargetBlocksSensitiveInputsBeforeDispatch(t *testing.T) {
	for _, inputType := range []string{"password", "file", "PASSWORD"} {
		node := &cdp.Node{NodeName: "INPUT", Attributes: []string{"type", inputType, "value", "must-not-be-read"}}
		err := validateTypeTarget(node)
		var browserErr *browser.Error
		if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrSensitiveInputBlocked {
			t.Fatalf("type %q error = %v", inputType, err)
		}
	}
	if err := validateTypeTarget(&cdp.Node{NodeName: "INPUT", Attributes: []string{"type", "text"}}); err != nil {
		t.Fatalf("text input rejected: %v", err)
	}
}
