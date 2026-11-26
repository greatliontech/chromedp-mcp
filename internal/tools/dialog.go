package tools

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

const (
	dialogActionGet     = "get"
	dialogActionAccept  = "accept"
	dialogActionDismiss = "dismiss"
)

var dialogActionValues = []string{dialogActionGet, dialogActionAccept, dialogActionDismiss}

// DialogInput is the input for dialog.
type DialogInput struct {
	TabInput
	Action          string  `json:"action" jsonschema:"Dialog action: 'get' (inspect without blocking), 'accept', or 'dismiss'."`
	Text            *string `json:"text,omitempty" jsonschema:"For action='accept' on a prompt dialog: text to submit (omit to keep default; empty string submits empty)."`
	ObserveWindowMs int     `json:"observe_window_ms,omitempty" jsonschema:"Observe activity for N ms after handling the dialog (0=off, max 30000)."`
}

// DialogOutput is the output for dialog.
type DialogOutput struct {
	Open     bool                   `json:"open"`
	Dialog   *collector.DialogState `json:"dialog,omitempty"`
	Observed *ObservedActivity      `json:"observed,omitempty"`
}

func registerDialogTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "dialog",
		Description: "Inspect ('get') or handle ('accept'/'dismiss') a JavaScript modal dialog (alert, confirm, prompt, beforeunload) without blocking on the renderer.",
		InputSchema: schemaWithEnums[DialogInput](map[string][]string{
			"action": dialogActionValues,
		}),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp DialogInput) (*mcp.CallToolResult, DialogOutput, error) {
		if err := validateEnum("action", inp.Action, false, dialogActionValues); err != nil {
			return nil, DialogOutput{}, err
		}
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, DialogOutput{}, err
		}
		if inp.Action != dialogActionAccept && inp.Text != nil {
			return nil, DialogOutput{}, fmt.Errorf("text is only valid when action is 'accept'")
		}
		if inp.Action == dialogActionGet && inp.ObserveWindowMs > 0 {
			return nil, DialogOutput{}, fmt.Errorf("observe_window_ms is not valid when action is 'get'")
		}
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, DialogOutput{}, err
		}

		if inp.Action == dialogActionGet {
			cur := t.Dialog.Current()
			if cur == nil {
				return nil, DialogOutput{Open: false}, nil
			}
			return nil, DialogOutput{Open: true, Dialog: cur}, nil
		}

		accept := inp.Action == dialogActionAccept
		cur, seq := t.Dialog.Snapshot()
		var promptText string
		if accept {
			if inp.Text != nil {
				promptText = *inp.Text
			} else if cur != nil && cur.Type == "prompt" {
				promptText = cur.DefaultPrompt
			}
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		obs := openObservation(tctx, t, inp.ObserveWindowMs)
		if _, err := chromedp.Call(tctx, page.HandleJavaScriptDialog, page.HandleJavaScriptDialogParams{
			Accept:     accept,
			PromptText: promptText,
		}); err != nil {
			return nil, DialogOutput{}, err
		}
		// Clear the tracked dialog generation synchronously so immediate
		// follow-up calls never race the async Page.javascriptDialogClosed event,
		// while preserving any chained next dialog whose HandleOpening already advanced seq.
		t.Dialog.ClearIfSeq(seq)
		// Close the observation window BEFORE reading Current() so any chained
		// dialog opened during observe_window_ms is reflected in the response.
		observed := obs.close(tctx)
		next := t.Dialog.Current()
		return nil, DialogOutput{
			Open:     next != nil,
			Dialog:   next,
			Observed: observed,
		}, nil
	})
}
