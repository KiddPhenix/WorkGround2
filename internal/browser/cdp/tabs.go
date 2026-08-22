package cdp

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"workground2/internal/browser"
)

// listTabs returns the current browser tabs.
func listTabs(ctx context.Context) ([]browser.TabInfo, error) {
	return listTabsInternal(ctx, "")
}

// newTab creates a new tab and navigates to url.
func newTab(ctx context.Context, url string) (string, error) {
	var targetID string
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		tID, err := target.CreateTarget(url).Do(ctx)
		if err != nil {
			return fmt.Errorf("create target: %w", err)
		}
		targetID = string(tID)
		return nil
	}))
	return targetID, err
}

// activateTab switches to the specified tab.
func activateTab(ctx context.Context, targetID string) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return target.ActivateTarget(target.ID(targetID)).Do(ctx)
	}))
}

// closeTab closes the target attached to ctx through Page.close. Unlike
// Target.closeTarget, Page.close runs beforeunload hooks, allowing the shared
// dialog policy to preserve the page or explicitly authorize leaving.
func closeTab(ctx context.Context) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		// Get targets to check count.
		targets, err := target.GetTargets().Do(ctx)
		if err != nil {
			return fmt.Errorf("get targets: %w", err)
		}
		pageCount := 0
		for _, t := range targets {
			if t.Type == "page" {
				pageCount++
			}
		}
		if pageCount <= 1 {
			return fmt.Errorf("cannot close last tab")
		}
		return page.Close().Do(ctx)
	}))
}
