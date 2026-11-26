package tools

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// dragAndDropInput is the input for drag_and_drop.
type dragAndDropInput struct {
	selectorInput
	SourceRef       string   `json:"source_ref,omitempty" jsonschema:"Snapshot ref of the element to drag."`
	SourceSelector  string   `json:"source_selector,omitempty" jsonschema:"CSS/shadow ('>>>') selector of the element to drag."`
	SourceFrame     string   `json:"source_frame,omitempty" jsonschema:"Optional same-origin frame ID for source_selector."`
	SourceX         *float64 `json:"source_x,omitempty" jsonschema:"Viewport X coordinate to start dragging from (requires source_y)."`
	SourceY         *float64 `json:"source_y,omitempty" jsonschema:"Viewport Y coordinate to start dragging from (requires source_x)."`
	TargetRef       string   `json:"target_ref,omitempty" jsonschema:"Snapshot ref of the drop target element."`
	TargetSelector  string   `json:"target_selector,omitempty" jsonschema:"CSS/shadow ('>>>') selector of the drop target element."`
	TargetFrame     string   `json:"target_frame,omitempty" jsonschema:"Optional same-origin frame ID for target_selector."`
	TargetX         *float64 `json:"target_x,omitempty" jsonschema:"Viewport X coordinate to drop onto (requires target_y)."`
	TargetY         *float64 `json:"target_y,omitempty" jsonschema:"Viewport Y coordinate to drop onto (requires target_x)."`
	Steps           int      `json:"steps,omitempty" jsonschema:"Intermediate mouse move steps (default 10, max 100)."`
	ObserveWindowMs int      `json:"observe_window_ms,omitempty" jsonschema:"Observe activity for N ms after drop (0=off, max 30000)."`
}

func registerDragTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "drag_and_drop",
		Description: "Drag from a source (ref, CSS/shadow selector, or viewport coordinates) onto a target (ref, CSS/shadow selector, or coordinates). Supports both HTML5 draggable elements (DataTransfer via CDP drag interception) and pointer-based drags.",
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp dragAndDropInput) (*mcp.CallToolResult, actionOutput, error) {
		if err := validatePointerTarget(inp.SourceRef, inp.SourceSelector, inp.SourceFrame, inp.SourceX, inp.SourceY, true); err != nil {
			return nil, actionOutput{}, fmt.Errorf("source: %w", err)
		}
		if err := validatePointerTarget(inp.TargetRef, inp.TargetSelector, inp.TargetFrame, inp.TargetX, inp.TargetY, true); err != nil {
			return nil, actionOutput{}, fmt.Errorf("target: %w", err)
		}
		if err := validateSelectorTimeout(inp.Timeout); err != nil {
			return nil, actionOutput{}, err
		}
		if inp.Steps < 0 || inp.Steps > 100 {
			return nil, actionOutput{}, fmt.Errorf("steps must be between 0 and 100, got %d", inp.Steps)
		}
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, actionOutput{}, err
		}
		steps := inp.Steps
		if steps == 0 {
			steps = 10
		}

		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, actionOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		// Resolve element endpoints BEFORE acquiring WithDragLock or opening
		// the observation window so selector waits neither hold the drag mutex
		// nor pollute observation counts, and release RemoteObjectIDs right
		// away (only BackendNodeID/Label are needed during drag dispatch).
		var (
			srcBID, dstBID     cdp.BackendNodeID
			srcLabel, dstLabel string
		)
		if inp.SourceX == nil || inp.TargetX == nil {
			if err := chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				if inp.SourceX == nil {
					srcEl, err := resolveElementObject(ctx, tgt, t, inp.SourceRef, inp.SourceSelector, inp.SourceFrame, inp.Timeout, true)
					if err != nil {
						return fmt.Errorf("source: %w", err)
					}
					srcBID, srcLabel = srcEl.BackendNodeID, srcEl.Label
					srcEl.Release(ctx, tgt)
				}
				if inp.TargetX == nil {
					dstEl, err := resolveElementObject(ctx, tgt, t, inp.TargetRef, inp.TargetSelector, inp.TargetFrame, inp.Timeout, true)
					if err != nil {
						return fmt.Errorf("target: %w", err)
					}
					dstBID, dstLabel = dstEl.BackendNodeID, dstEl.Label
					dstEl.Release(ctx, tgt)
				}
				return nil
			})); err != nil {
				return nil, actionOutput{}, err
			}
		}

		var obs *observation
		if err := t.WithDragLock(tctx, func(ctx context.Context, tgt *chromedp.Target) error {
			dragCh := make(chan *input.DragData, 1)
			lctx, lcancel := context.WithCancel(ctx)
			defer lcancel()
			if err := chromedp.Listen(lctx, func(ev any) {
				if d, ok := ev.(*input.EventDragIntercepted); ok && d != nil && d.Data != nil {
					select {
					case dragCh <- d.Data:
					default:
					}
				}
			}); err != nil {
				return err
			}
			if _, err := cdp.Call(ctx, tgt, input.SetInterceptDrags, input.SetInterceptDragsParams{
				Enabled: true,
			}); err != nil {
				if t.Context().Err() == nil && ctx.Err() != nil {
					cleanCtx, ccancel := cleanupContext(ctx)
					_, _ = cdp.Call(cleanCtx, tgt, input.SetInterceptDrags, input.SetInterceptDragsParams{
						Enabled: false,
					})
					ccancel()
				}
				return fmt.Errorf("enable drag interception: %w", err)
			}
			defer func() {
				cleanCtx, ccancel := cleanupContext(ctx)
				defer ccancel()
				_, _ = cdp.Call(cleanCtx, tgt, input.SetInterceptDrags, input.SetInterceptDragsParams{
					Enabled: false,
				})
			}()

			// Open observation inside WithDragLock so queued concurrent drags
			// only observe their own mutations/network activity.
			obs = openObservation(ctx, t, inp.ObserveWindowMs)

			var srcX, srcY float64
			if inp.SourceX != nil {
				srcX, srcY = *inp.SourceX, *inp.SourceY
			} else {
				var err error
				srcX, srcY, err = scrollAndCenterBackendNode(ctx, tgt, srcBID)
				if err != nil {
					return fmt.Errorf("source %s: %w", srcLabel, err)
				}
			}

			// Compute a 16px drag-threshold nudge direction while source is in
			// view so Blink's drag hysteresis is crossed BEFORE scrolling target
			// into view (critical when source and target are >1 viewport apart).
			nudgeDX, nudgeDY := 16.0, 0.0
			if srcX > 40 {
				nudgeDX = -16.0
			}
			if inp.TargetX != nil {
				dx, dy := *inp.TargetX-srcX, *inp.TargetY-srcY
				if dist := math.Hypot(dx, dy); dist >= 16 {
					nudgeDX = dx / dist * 16
					nudgeDY = dy / dist * 16
				}
			} else if tx, ty, qErr := contentQuadCenter(ctx, tgt, dstBID); qErr == nil {
				dx, dy := tx-srcX, ty-srcY
				if dist := math.Hypot(dx, dy); dist >= 16 {
					nudgeDX = dx / dist * 16
					nudgeDY = dy / dist * 16
				}
			}

			if _, err := cdp.Call(ctx, tgt, input.DispatchMouseEvent, input.DispatchMouseEventParams{
				Type: chromedp.MouseMoved,
				X:    srcX,
				Y:    srcY,
			}); err != nil {
				return err
			}
			if err := runDialogInterruptible(ctx, t.Dialog, func(dctx context.Context) error {
				_, err := cdp.Call(dctx, tgt, input.DispatchMouseEvent, input.DispatchMouseEventParams{
					Type:       chromedp.MousePressed,
					X:          srcX,
					Y:          srcY,
					Button:     input.MouseButtonLeft,
					Buttons:    1,
					ClickCount: 1,
				})
				return err
			}); err != nil {
				return err
			}
			if t.Dialog != nil && t.Dialog.Open() {
				return nil
			}

			// Ensure neither left mouse button nor an active intercepted HTML5
			// drag is ever left stuck on error/cancel.
			mouseDown := true
			lastX, lastY := srcX, srcY
			var activeDrag *input.DragData
			defer func() {
				if t.Dialog != nil && t.Dialog.Open() {
					// Renderer is paused on a JS modal dialog; dispatching DragCancel
					// or MouseReleased would stall cleanupContext for 2s.
					return
				}
				if activeDrag == nil {
					select {
					case activeDrag = <-dragCh:
					default:
					}
				}
				if activeDrag != nil {
					cleanCtx, ccancel := cleanupContext(ctx)
					defer ccancel()
					_, _ = cdp.Call(cleanCtx, tgt, input.DispatchDragEvent, input.DispatchDragEventParams{
						Type: input.DispatchDragEventTypeDragCancel,
						X:    lastX,
						Y:    lastY,
						Data: activeDrag,
					})
					return
				}
				if !mouseDown {
					return
				}
				cleanCtx, ccancel := cleanupContext(ctx)
				defer ccancel()
				_, _ = cdp.Call(cleanCtx, tgt, input.DispatchMouseEvent, input.DispatchMouseEventParams{
					Type:       chromedp.MouseReleased,
					X:          lastX,
					Y:          lastY,
					Button:     input.MouseButtonLeft,
					ClickCount: 1,
				})
			}()

			for _, frac := range []float64{0.5, 1.0} {
				lastX = srcX + nudgeDX*frac
				lastY = srcY + nudgeDY*frac
				if err := runDialogInterruptible(ctx, t.Dialog, func(dctx context.Context) error {
					_, err := cdp.Call(dctx, tgt, input.DispatchMouseEvent, input.DispatchMouseEventParams{
						Type:    chromedp.MouseMoved,
						X:       lastX,
						Y:       lastY,
						Button:  input.MouseButtonLeft,
						Buttons: 1,
					})
					return err
				}); err != nil {
					return err
				}
				if t.Dialog != nil && t.Dialog.Open() {
					return nil
				}
			}

			var dragData *input.DragData
			select {
			case dragData = <-dragCh:
			default:
				if t.Dialog == nil || !t.Dialog.Open() {
					syncCtx, syncCancel := context.WithTimeout(ctx, 100*time.Millisecond)
					_, _ = chromedp.Evaluate[chromedp.Void](`0`)(syncCtx, tgt)
					syncCancel()
				}
				timer := time.NewTimer(50 * time.Millisecond)
				select {
				case dragData = <-dragCh:
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				}
				timer.Stop()
			}
			if dragData != nil {
				activeDrag = dragData
				mouseDown = false
			}

			var dstX, dstY float64
			if inp.TargetX != nil {
				dstX, dstY = *inp.TargetX, *inp.TargetY
			} else {
				var err error
				dstX, dstY, err = scrollAndCenterBackendNode(ctx, tgt, dstBID)
				if err != nil {
					return fmt.Errorf("target %s: %w", dstLabel, err)
				}
			}

			dispatchHTML5Drop := func(data *input.DragData) error {
				activeDrag = data
				mouseDown = false
				lastX, lastY = dstX, dstY
				if err := runDialogInterruptible(ctx, t.Dialog, func(dctx context.Context) error {
					for _, evType := range []input.DispatchDragEventType{
						input.DispatchDragEventTypeDragEnter,
						input.DispatchDragEventTypeDragOver,
						input.DispatchDragEventTypeDrop,
					} {
						if _, err := cdp.Call(dctx, tgt, input.DispatchDragEvent, input.DispatchDragEventParams{
							Type: evType,
							X:    dstX,
							Y:    dstY,
							Data: data,
						}); err != nil {
							return err
						}
						if t.Dialog != nil && t.Dialog.Open() {
							return nil
						}
					}
					return nil
				}); err != nil {
					return err
				}
				activeDrag = nil
				return nil
			}

			if dragData != nil {
				return dispatchHTML5Drop(dragData)
			}

			startX, startY := lastX, lastY
			for i := 1; i <= steps; i++ {
				select {
				case dragData = <-dragCh:
					return dispatchHTML5Drop(dragData)
				default:
				}
				frac := float64(i) / float64(steps)
				lastX = startX + (dstX-startX)*frac
				lastY = startY + (dstY-startY)*frac
				if err := runDialogInterruptible(ctx, t.Dialog, func(dctx context.Context) error {
					_, err := cdp.Call(dctx, tgt, input.DispatchMouseEvent, input.DispatchMouseEventParams{
						Type:    chromedp.MouseMoved,
						X:       lastX,
						Y:       lastY,
						Button:  input.MouseButtonLeft,
						Buttons: 1,
					})
					return err
				}); err != nil {
					return err
				}
				if t.Dialog != nil && t.Dialog.Open() {
					return nil
				}
			}
			select {
			case dragData = <-dragCh:
				return dispatchHTML5Drop(dragData)
			default:
			}

			if err := runDialogInterruptible(ctx, t.Dialog, func(dctx context.Context) error {
				_, err := cdp.Call(dctx, tgt, input.DispatchMouseEvent, input.DispatchMouseEventParams{
					Type:       chromedp.MouseReleased,
					X:          dstX,
					Y:          dstY,
					Button:     input.MouseButtonLeft,
					ClickCount: 1,
				})
				return err
			}); err != nil {
				return err
			}
			mouseDown = false
			return nil
		}); err != nil {
			return nil, actionOutput{}, err
		}
		return nil, actionOutput{Observed: obs.close(tctx)}, nil
	})
}
