package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

// shadowPiercingSeparator splits a selector into chained CSS segments that
// cross open Shadow DOM boundaries (e.g. "my-card >>> button.save").
const shadowPiercingSeparator = ">>>"

// parseShadowSelector splits selector on unquoted ">>>" combinators (respecting
// single/double-quoted CSS attribute values and backslash escapes). Plain
// selectors return ([]string{selector}, false, nil). Empty segments around
// ">>>" fail with a descriptive validation error.
func parseShadowSelector(selector string) ([]string, bool, error) {
	var (
		parts    []string
		quote    byte
		escaped  bool
		segStart int
		foundSep bool
	)
	for i := 0; i < len(selector); i++ {
		c := selector[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if strings.HasPrefix(selector[i:], shadowPiercingSeparator) {
			foundSep = true
			seg := strings.TrimSpace(selector[segStart:i])
			if seg == "" {
				return nil, true, fmt.Errorf("invalid shadow selector %q: empty segment around %q", selector, shadowPiercingSeparator)
			}
			parts = append(parts, seg)
			i += len(shadowPiercingSeparator) - 1
			segStart = i + 1
		}
	}
	if !foundSep {
		return []string{selector}, false, nil
	}
	last := strings.TrimSpace(selector[segStart:])
	if last == "" {
		return nil, true, fmt.Errorf("invalid shadow selector %q: empty segment around %q", selector, shadowPiercingSeparator)
	}
	parts = append(parts, last)
	return parts, true, nil
}

// queryAllElementsJS returns a JS expression evaluating to an Array of all
// matching Elements in the current document, crossing open shadow roots when
// selector uses the ">>>" combinator.
func queryAllElementsJS(selector string) (string, error) {
	parts, isShadow, err := parseShadowSelector(selector)
	if err != nil {
		return "", err
	}
	if !isShadow {
		return fmt.Sprintf(`Array.from(document.querySelectorAll(%s))`, jsQuote(selector)), nil
	}
	return queryAllElementsFromPartsJS(parts)
}

func queryAllElementsFromPartsJS(parts []string) (string, error) {
	segsJSON, err := json.Marshal(parts)
	if err != nil {
		return "", err
	}
	// Validate every segment's CSS syntax up front so invalid CSS after an
	// unmatched or closed shadow host fails fast instead of spinning until timeout.
	return fmt.Sprintf(`(function(segs) {
		const check = document.createDocumentFragment();
		for (let i = 0; i < segs.length; i++) {
			check.querySelector(segs[i]);
		}
		let roots = [document];
		for (let i = 0; i < segs.length; i++) {
			const matches = [];
			for (let r = 0; r < roots.length; r++) {
				const list = roots[r].querySelectorAll(segs[i]);
				for (let j = 0; j < list.length; j++) matches.push(list[j]);
			}
			if (i === segs.length - 1) return matches;
			const next = [];
			for (let j = 0; j < matches.length; j++) {
				if (matches[j].shadowRoot) next.push(matches[j].shadowRoot);
			}
			if (next.length === 0) return [];
			roots = next;
		}
		return [];
	})(%s)`, string(segsJSON)), nil
}

// queryOneElementJS returns a JS expression evaluating to the first matching
// Element (or null) in the current document, supporting ">>>" shadow piercing.
func queryOneElementJS(selector string) (string, error) {
	parts, isShadow, err := parseShadowSelector(selector)
	if err != nil {
		return "", err
	}
	if !isShadow {
		return fmt.Sprintf(`document.querySelector(%s)`, jsQuote(selector)), nil
	}
	allExpr, err := queryAllElementsFromPartsJS(parts)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`((%s)[0] || null)`, allExpr), nil
}

// waitForSelector waits for selector to match an element in the main frame
// (frameID == "") or inside frameID's execution context, supporting ">>>"
// Shadow DOM piercing. When frameID is non-empty, returns the live
// UniqueContextID (re-resolving across same-origin subframe navigations).
func waitForSelector(sctx, tctx context.Context, t *tab.Tab, uniqueCtxID, frameID, selector string) (string, error) {
	_, isShadow, err := parseShadowSelector(selector)
	if err != nil {
		return "", err
	}
	if frameID == "" && !isShadow {
		if err := chromedp.Do(sctx, chromedp.WaitReady(chromedp.CSS(selector))); err != nil {
			return "", selectorError(tctx, selector, err)
		}
		return "", nil
	}

	findExpr, err := queryOneElementJS(selector)
	if err != nil {
		return "", err
	}
	checkJS := fmt.Sprintf(`(%s) !== null`, findExpr)
	curCtxID := uniqueCtxID
	deadCtxIDs := make(map[string]bool)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if frameID == "" || curCtxID != "" {
			var evalOpts []chromedp.EvaluateOption
			if curCtxID != "" {
				evalOpts = append(evalOpts, evalInUniqueContext(curCtxID))
			}
			found, err := chromedp.Run(sctx, chromedp.Evaluate[bool](checkJS, evalOpts...))
			if err == nil {
				if found {
					return curCtxID, nil
				}
			} else {
				if tctx.Err() != nil {
					return "", err
				}
				if errors.Is(sctx.Err(), context.DeadlineExceeded) {
					if frameID != "" {
						return "", fmt.Errorf("selector %q not found in frame %q: %w", selector, frameID, sctx.Err())
					}
					return "", fmt.Errorf("element %q not found in the DOM (timed out waiting for it to appear)", selector)
				}
				var exc *chromedp.ExceptionError
				if errors.As(err, &exc) {
					if frameID != "" {
						return "", fmt.Errorf("selector %q in frame %q: %w", selector, frameID, err)
					}
					return "", fmt.Errorf("selector %q: %w", selector, err)
				}
				if isStaleContextError(err) {
					if frameID != "" {
						deadCtxIDs[curCtxID] = true
						ec, reachable, known, swapped := t.ExecContexts.ContextForFrame(frameID)
						if !known {
							return "", fmt.Errorf("frame %q detached while waiting for selector %q", frameID, selector)
						}
						if swapped {
							return "", fmt.Errorf("frame %q became out-of-process (cross-origin) while waiting for selector %q", frameID, selector)
						}
						if reachable && !deadCtxIDs[ec.UniqueID] {
							curCtxID = ec.UniqueID
							continue
						}
						curCtxID = ""
					}
					// For main-frame shadow selectors, retry until the incoming
					// document finishes navigating or sctx expires.
				} else {
					return "", err
				}
			}
		} else {
			ec, reachable, known, swapped := t.ExecContexts.ContextForFrame(frameID)
			if !known {
				return "", fmt.Errorf("frame %q detached while waiting for selector %q", frameID, selector)
			}
			if swapped {
				return "", fmt.Errorf("frame %q became out-of-process (cross-origin) while waiting for selector %q", frameID, selector)
			}
			if reachable && !deadCtxIDs[ec.UniqueID] {
				curCtxID = ec.UniqueID
				continue
			}
		}

		select {
		case <-tctx.Done():
			return "", tctx.Err()
		case <-sctx.Done():
			if frameID != "" {
				if curCtxID == "" {
					if _, reachable, known, _ := t.ExecContexts.ContextForFrame(frameID); known && !reachable {
						return "", fmt.Errorf("frame %q has no reachable execution context while waiting for selector %q: %w", frameID, selector, sctx.Err())
					}
				}
				return "", fmt.Errorf("selector %q not found in frame %q: %w", selector, frameID, sctx.Err())
			}
			return "", fmt.Errorf("element %q not found in the DOM (timed out waiting for it to appear)", selector)
		case <-ticker.C:
		}
	}
}

// resolvedElement holds a live CDP RemoteObjectID (and optional BackendNodeID)
// for an element resolved from either a snapshot ref or a CSS/shadow selector.
// Callers must always call Release once done.
type resolvedElement struct {
	ObjectID      runtime.RemoteObjectID
	BackendNodeID cdp.BackendNodeID
	FrameID       cdp.FrameID
	Label         string
}

func (e *resolvedElement) Release(ctx context.Context, tgt *chromedp.Target) {
	if e == nil || e.ObjectID == "" {
		return
	}
	oid := e.ObjectID
	e.ObjectID = ""
	releaseRemoteObject(ctx, tgt, oid)
}

func releaseRemoteObject(ctx context.Context, tgt *chromedp.Target, oid runtime.RemoteObjectID) {
	if oid == "" {
		return
	}
	cleanCtx, cancel := cleanupContext(ctx)
	defer cancel()
	_, _ = cdp.Call(cleanCtx, tgt, runtime.ReleaseObject, runtime.ReleaseObjectParams{
		ObjectID: oid,
	})
}

// validateElementTarget checks mutual exclusion between ref, selector, and frame.
func validateElementTarget(ref, selector, frameID string, requireOne bool) error {
	if ref != "" && selector != "" {
		return fmt.Errorf("ref and selector are mutually exclusive")
	}
	if ref != "" && frameID != "" {
		return fmt.Errorf("ref and frame are mutually exclusive (refs already bind to their frame)")
	}
	if requireOne && ref == "" && selector == "" {
		return fmt.Errorf("either ref or selector is required")
	}
	if selector != "" {
		if _, _, err := parseShadowSelector(selector); err != nil {
			return err
		}
	}
	return nil
}

// resolveElementObject resolves either a snapshot ref (via BackendNodeID) or a
// CSS/shadow selector (in the main frame or a same-origin iframe) into a live
// CDP RemoteObjectID (plus BackendNodeID when needBackendID is true). Always
// call el.Release when finished.
func resolveElementObject(
	tctx context.Context,
	tgt *chromedp.Target,
	t *tab.Tab,
	ref, selector, frameID string,
	timeoutMs int,
	needBackendID bool,
) (*resolvedElement, error) {
	if err := validateElementTarget(ref, selector, frameID, true); err != nil {
		return nil, err
	}

	if ref != "" {
		snapRef, err := t.ResolveRef(ref)
		if err != nil {
			return nil, err
		}
		res, err := cdp.Call(tctx, tgt, dom.ResolveNode, dom.ResolveNodeParams{
			BackendNodeID: snapRef.BackendNodeID,
		})
		if err != nil {
			if tctx.Err() != nil {
				return nil, tctx.Err()
			}
			return nil, fmt.Errorf("ref %q is no longer attached to the DOM — call snapshot again", ref)
		}
		if res.Object == nil || res.Object.ObjectID == "" {
			return nil, fmt.Errorf("ref %q could not be resolved to a DOM node — call snapshot again", ref)
		}
		oid := res.Object.ObjectID
		connected, err := callOnElement[bool](tctx, tgt, oid, `function() { return Boolean(this && this.isConnected); }`, false)
		if err != nil || !connected {
			releaseRemoteObject(tctx, tgt, oid)
			if err != nil && tctx.Err() != nil {
				return nil, tctx.Err()
			}
			return nil, fmt.Errorf("ref %q is no longer attached to the DOM — call snapshot again", ref)
		}
		return &resolvedElement{
			ObjectID:      oid,
			BackendNodeID: snapRef.BackendNodeID,
			FrameID:       snapRef.FrameID,
			Label:         fmt.Sprintf("ref %q", ref),
		}, nil
	}

	uniqueCtxID, err := resolveFrameContext(t, frameID, true)
	if err != nil {
		return nil, err
	}
	sctx, scancel := selectorContext(tctx, timeoutMs)
	uniqueCtxID, err = waitForSelector(sctx, tctx, t, uniqueCtxID, frameID, selector)
	scancel()
	if err != nil {
		return nil, err
	}

	findExpr, err := queryOneElementJS(selector)
	if err != nil {
		return nil, err
	}
	evalParams := runtime.EvaluateParams{
		Expression:    findExpr,
		ReturnByValue: new(false),
	}
	if uniqueCtxID != "" {
		evalParams.UniqueContextID = uniqueCtxID
	}
	evalRes, err := cdp.Call(tctx, tgt, runtime.Evaluate, evalParams)
	if err != nil {
		return nil, err
	}
	if evalRes.ExceptionDetails != nil {
		if evalRes.Result != nil && evalRes.Result.ObjectID != "" {
			releaseRemoteObject(tctx, tgt, evalRes.Result.ObjectID)
		}
		return nil, fmt.Errorf("selector %q: %s", selector, formatExceptionDetails(evalRes.ExceptionDetails))
	}
	if evalRes.Result == nil || evalRes.Result.ObjectID == "" {
		return nil, fmt.Errorf("selector %q matched no elements", selector)
	}
	oid := evalRes.Result.ObjectID

	var bid cdp.BackendNodeID
	if needBackendID {
		desc, err := cdp.Call(tctx, tgt, dom.DescribeNode, dom.DescribeNodeParams{
			ObjectID: oid,
		})
		if err != nil {
			if tctx.Err() != nil {
				releaseRemoteObject(tctx, tgt, oid)
				return nil, tctx.Err()
			}
		} else if desc.Node != nil {
			bid = desc.Node.BackendNodeID
		}
	}

	return &resolvedElement{
		ObjectID:      oid,
		BackendNodeID: bid,
		FrameID:       cdp.FrameID(frameID),
		Label:         fmt.Sprintf("selector %q", selector),
	}, nil
}

// formatExceptionDetails extracts the most descriptive error message from a
// CDP Runtime.ExceptionDetails payload, covering Error objects (Description),
// thrown primitives (Value), and fallback Text.
func formatExceptionDetails(ex *runtime.ExceptionDetails) string {
	if ex == nil {
		return "unknown JavaScript exception"
	}
	if ex.Exception != nil {
		if ex.Exception.Description != "" {
			return ex.Exception.Description
		}
		if len(ex.Exception.Value) > 0 {
			if ex.Exception.Value[0] == '"' {
				var s string
				if err := json.Unmarshal(ex.Exception.Value, &s); err == nil && s != "" {
					return s
				}
			} else {
				return string(ex.Exception.Value)
			}
		}
	}
	if ex.Text != "" {
		return ex.Text
	}
	return "unknown JavaScript exception"
}

// callOnElement runs fnDecl against oid via Runtime.callFunctionOn with
// ReturnByValue=true and unmarshals the JSON result into T.
func callOnElement[T any](
	ctx context.Context,
	tgt *chromedp.Target,
	oid runtime.RemoteObjectID,
	fnDecl string,
	awaitPromise bool,
	args ...*runtime.CallArgument,
) (T, error) {
	var zero T
	params := runtime.CallFunctionOnParams{
		ObjectID:            oid,
		FunctionDeclaration: fnDecl,
		Arguments:           args,
		ReturnByValue:       new(true),
		AwaitPromise:        new(awaitPromise),
	}
	res, err := cdp.Call(ctx, tgt, runtime.CallFunctionOn, params)
	if err != nil {
		return zero, err
	}
	if res.ExceptionDetails != nil {
		return zero, fmt.Errorf("%s", formatExceptionDetails(res.ExceptionDetails))
	}
	if res.Result == nil || len(res.Result.Value) == 0 {
		return zero, nil
	}
	var out T
	if err := json.Unmarshal(res.Result.Value, &out); err != nil {
		return zero, err
	}
	return out, nil
}
