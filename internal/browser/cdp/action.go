package cdp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"workground2/internal/browser"
)

type clickBackend interface {
	resolve(context.Context, cdp.BackendNodeID) (cdp.BackendNodeID, error)
	scrollIntoView(context.Context, cdp.BackendNodeID) error
	box(context.Context, cdp.BackendNodeID) (*dom.BoxModel, error)
	hitTest(context.Context, float64, float64) (cdp.BackendNodeID, error)
	contains(context.Context, cdp.BackendNodeID, cdp.BackendNodeID) (bool, error)
	mouse(context.Context, input.MouseType, float64, float64) error
	domClick(context.Context, cdp.BackendNodeID) (bool, error)
}

type protocolClickBackend struct{}

func (protocolClickBackend) resolve(ctx context.Context, backend cdp.BackendNodeID) (cdp.BackendNodeID, error) {
	node, err := dom.DescribeNode().WithBackendNodeID(backend).WithDepth(0).Do(ctx)
	if err != nil {
		return 0, err
	}
	if node == nil {
		return 0, fmt.Errorf("node %d not found", backend)
	}
	return backend, nil
}

func (protocolClickBackend) scrollIntoView(ctx context.Context, node cdp.BackendNodeID) error {
	return dom.ScrollIntoViewIfNeeded().WithBackendNodeID(node).Do(ctx)
}

func (protocolClickBackend) box(ctx context.Context, node cdp.BackendNodeID) (*dom.BoxModel, error) {
	return dom.GetBoxModel().WithBackendNodeID(node).Do(ctx)
}

func (protocolClickBackend) hitTest(ctx context.Context, x, y float64) (cdp.BackendNodeID, error) {
	backend, _, _, err := dom.GetNodeForLocation(int64(x), int64(y)).Do(ctx)
	return backend, err
}

func (protocolClickBackend) contains(ctx context.Context, targetNode, hitNode cdp.BackendNodeID) (bool, error) {
	targetObject, err := dom.ResolveNode().WithBackendNodeID(targetNode).Do(ctx)
	if err != nil || targetObject == nil || targetObject.ObjectID == "" {
		return false, fmt.Errorf("resolve target for hit relation: %w", err)
	}
	defer func() { _ = runtime.ReleaseObject(targetObject.ObjectID).Do(ctx) }()
	hitObject, err := dom.ResolveNode().WithBackendNodeID(hitNode).Do(ctx)
	if err != nil || hitObject == nil || hitObject.ObjectID == "" {
		return false, fmt.Errorf("resolve hit node: %w", err)
	}
	defer func() { _ = runtime.ReleaseObject(hitObject.ObjectID).Do(ctx) }()
	result, exception, err := runtime.CallFunctionOn(`function (node) { return this === node || this.contains(node); }`).
		WithObjectID(targetObject.ObjectID).
		WithArguments([]*runtime.CallArgument{{ObjectID: hitObject.ObjectID}}).
		WithReturnByValue(true).Do(ctx)
	if err != nil {
		return false, err
	}
	if exception != nil {
		return false, fmt.Errorf("hit relation exception: %s", exception.Text)
	}
	return result != nil && result.Value.String() == "true", nil
}

func (protocolClickBackend) mouse(ctx context.Context, kind input.MouseType, x, y float64) error {
	event := input.DispatchMouseEvent(kind, x, y)
	if kind == input.MousePressed || kind == input.MouseReleased {
		event = event.WithButton(input.Left).WithClickCount(1)
	}
	return event.Do(ctx)
}

func (protocolClickBackend) domClick(ctx context.Context, node cdp.BackendNodeID) (bool, error) {
	object, err := dom.ResolveNode().WithBackendNodeID(node).Do(ctx)
	if err != nil || object == nil || object.ObjectID == "" {
		return false, fmt.Errorf("resolve click fallback object: %w", err)
	}
	defer func() { _ = runtime.ReleaseObject(object.ObjectID).Do(ctx) }()
	_, exception, err := runtime.CallFunctionOn(`function () {
		if (!(this instanceof HTMLElement)) throw new Error("target is not HTMLElement");
		this.click();
		return true;
	}`).WithObjectID(object.ObjectID).WithUserGesture(true).WithReturnByValue(true).Do(ctx)
	if err != nil {
		return true, err
	}
	if exception != nil {
		return true, fmt.Errorf("DOM click exception: %s", exception.Text)
	}
	return true, nil
}

// clickNodeWithMethod returns "mouse" or "dom_fallback" for tests and logs.
// browser.Driver.Click cannot currently expose that value to ActionResult.
func clickNodeWithMethod(ctx context.Context, ref browser.NodeRef) (string, error) {
	var method string
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		method, err = clickUsing(ctx, protocolClickBackend{}, ref)
		return err
	}))
	return method, err
}

func clickUsing(ctx context.Context, backend clickBackend, ref browser.NodeRef) (string, error) {
	nodeID, err := backend.resolve(ctx, cdp.BackendNodeID(ref.BackendNodeID))
	if err != nil {
		return "", fmt.Errorf("resolve node: %w", err)
	}
	if err := backend.scrollIntoView(ctx, nodeID); err != nil {
		return fallbackClick(ctx, backend, nodeID, false, fmt.Errorf("scroll into view: %w", err))
	}
	box, err := backend.box(ctx, nodeID)
	if err != nil || box == nil || len(box.Content) < 8 {
		return fallbackClick(ctx, backend, nodeID, false, fmt.Errorf("box model unavailable: %w", err))
	}
	x := (box.Content[0] + box.Content[2] + box.Content[4] + box.Content[6]) / 4
	y := (box.Content[1] + box.Content[3] + box.Content[5] + box.Content[7]) / 4
	hit, err := backend.hitTest(ctx, x, y)
	hitMatches := hit == cdp.BackendNodeID(ref.BackendNodeID)
	if err == nil && !hitMatches {
		hitMatches, err = backend.contains(ctx, cdp.BackendNodeID(ref.BackendNodeID), hit)
	}
	if err != nil || !hitMatches {
		return fallbackClick(ctx, backend, nodeID, false, fmt.Errorf("target not hit at click point: got %d: %w", hit, err))
	}
	if err := backend.mouse(ctx, input.MouseMoved, x, y); err != nil {
		return fallbackClick(ctx, backend, nodeID, false, fmt.Errorf("%s: %w", input.MouseMoved, err))
	}
	if err := backend.mouse(ctx, input.MousePressed, x, y); err != nil {
		return "mouse", &browser.DispatchError{Dispatched: true, Cause: fmt.Errorf("%s: %w", input.MousePressed, err)}
	}
	if err := backend.mouse(ctx, input.MouseReleased, x, y); err != nil {
		// A successful MousePressed may already have triggered page behavior.
		// Falling back here can double-click and is therefore forbidden.
		return "mouse", &browser.DispatchError{Dispatched: true, Cause: fmt.Errorf("%s: %w", input.MouseReleased, err)}
	}
	return "mouse", nil
}

func fallbackClick(ctx context.Context, backend clickBackend, nodeID cdp.BackendNodeID, previouslyDispatched bool, primary error) (string, error) {
	dispatched, err := backend.domClick(ctx, nodeID)
	if err != nil {
		joined := errors.Join(primary, fmt.Errorf("DOM fallback: %w", err))
		if previouslyDispatched || dispatched {
			return "dom_fallback", &browser.DispatchError{Dispatched: true, Cause: joined}
		}
		return "dom_fallback", joined
	}
	return "dom_fallback", nil
}

func typeText(ctx context.Context, ref browser.NodeRef, value browser.TypeInput) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		node, err := dom.DescribeNode().WithBackendNodeID(cdp.BackendNodeID(ref.BackendNodeID)).WithDepth(0).Do(ctx)
		if err != nil {
			return fmt.Errorf("describe input node: %w", err)
		}
		// This check intentionally precedes focus and every Input.* dispatch.
		if err := validateTypeTarget(node); err != nil {
			return err
		}
		if err := dom.Focus().WithBackendNodeID(cdp.BackendNodeID(ref.BackendNodeID)).Do(ctx); err != nil {
			return fmt.Errorf("focus input: %w", err)
		}
		if value.Clear {
			if err := dispatchClear(ctx); err != nil {
				return err
			}
		}
		if value.Text != "" {
			if err := input.InsertText(value.Text).Do(ctx); err != nil {
				return &browser.DispatchError{Dispatched: true, Cause: fmt.Errorf("insert text: %w", err)}
			}
		}
		if value.PressEnter {
			if err := dispatchEnter(ctx); err != nil {
				return err
			}
		}
		return nil
	}))
}

func validateTypeTarget(node *cdp.Node) error {
	if node == nil {
		return fmt.Errorf("input node not found")
	}
	attrs := parseDOMAttributes(node.Attributes)
	inputType := strings.ToLower(attrs["type"])
	if strings.EqualFold(node.NodeName, "input") && (inputType == "password" || inputType == "file") {
		return browser.NewError(browser.ErrSensitiveInputBlocked, "password and file inputs require a dedicated secure tool", nil)
	}
	return nil
}

func parseDOMAttributes(attributes []string) map[string]string {
	result := make(map[string]string, len(attributes)/2)
	for i := 0; i+1 < len(attributes); i += 2 {
		result[strings.ToLower(attributes[i])] = attributes[i+1]
	}
	return result
}

func dispatchClear(ctx context.Context) error {
	for _, event := range []*input.DispatchKeyEventParams{
		input.DispatchKeyEvent(input.KeyDown).WithWindowsVirtualKeyCode(65).WithNativeVirtualKeyCode(65).WithModifiers(input.ModifierCtrl),
		input.DispatchKeyEvent(input.KeyUp).WithWindowsVirtualKeyCode(65).WithNativeVirtualKeyCode(65).WithModifiers(input.ModifierCtrl),
		input.DispatchKeyEvent(input.KeyRawDown).WithWindowsVirtualKeyCode(8).WithNativeVirtualKeyCode(8),
		input.DispatchKeyEvent(input.KeyUp).WithWindowsVirtualKeyCode(8).WithNativeVirtualKeyCode(8),
	} {
		if err := event.Do(ctx); err != nil {
			return &browser.DispatchError{Dispatched: true, Cause: fmt.Errorf("clear input: %w", err)}
		}
	}
	return nil
}

func dispatchEnter(ctx context.Context) error {
	for _, event := range []*input.DispatchKeyEventParams{
		input.DispatchKeyEvent(input.KeyRawDown).WithWindowsVirtualKeyCode(13).WithNativeVirtualKeyCode(13),
		input.DispatchKeyEvent(input.KeyUp).WithWindowsVirtualKeyCode(13).WithNativeVirtualKeyCode(13),
	} {
		if err := event.Do(ctx); err != nil {
			return &browser.DispatchError{Dispatched: true, Cause: fmt.Errorf("press enter: %w", err)}
		}
	}
	return nil
}

func scrollPage(ctx context.Context, value browser.ScrollInput) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		if value.Node != nil {
			node, err := dom.DescribeNode().WithBackendNodeID(cdp.BackendNodeID(value.Node.BackendNodeID)).WithDepth(0).Do(ctx)
			if err != nil || node == nil {
				return fmt.Errorf("resolve scroll target: %w", err)
			}
			if err := dom.ScrollIntoViewIfNeeded().WithBackendNodeID(cdp.BackendNodeID(value.Node.BackendNodeID)).Do(ctx); err != nil {
				return fmt.Errorf("scroll into view: %w", err)
			}
		}
		if err := input.DispatchMouseEvent(input.MouseWheel, 0, 0).WithDeltaY(float64(value.DeltaY)).Do(ctx); err != nil {
			return &browser.DispatchError{Dispatched: true, Cause: fmt.Errorf("wheel event: %w", err)}
		}
		return nil
	}))
}
