package tools

import (
	"context"
	"sync"

	"github.com/chromedp/cdproto/cdp"
	cdpcss "github.com/chromedp/cdproto/css"
	"github.com/chromedp/cdproto/performance"
	"github.com/chromedp/cdproto/profiler"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// GetPerformanceMetricsInput is the input for get_performance_metrics.
type GetPerformanceMetricsInput struct {
	TabInput
}

// MetricEntry represents a single performance metric.
type MetricEntry struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// GetPerformanceMetricsOutput is the output for get_performance_metrics.
type GetPerformanceMetricsOutput struct {
	Metrics []MetricEntry `json:"metrics"`
}

// GetLayoutShiftsInput is the input for get_layout_shifts.
type GetLayoutShiftsInput struct {
	TabInput
	Mode string `json:"mode" jsonschema:"Read mode: 'peek' to keep entries in the buffer, 'drain' to consume them. Required."`
}

// GetLayoutShiftsOutput is the output for get_layout_shifts.
type GetLayoutShiftsOutput struct {
	Shifts       []collector.LayoutShiftEntry `json:"shifts"`
	CumulativeLS float64                      `json:"cumulative_ls"`
}

// GetCoverageInput is the input for get_coverage.
type GetCoverageInput struct {
	TabInput
	Type string `json:"type,omitempty" jsonschema:"Coverage type: css js or all (default all)"`
}

// CoverageEntry represents coverage data for a single file.
type CoverageEntry struct {
	URL        string  `json:"url"`
	TotalBytes int64   `json:"total_bytes"`
	UsedBytes  int64   `json:"used_bytes"`
	Percentage float64 `json:"percentage"`
}

// GetCoverageOutput is the output for get_coverage.
type GetCoverageOutput struct {
	Entries []CoverageEntry `json:"entries"`
}

func registerPerformanceTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_performance_metrics",
		Description: "Get Chrome runtime performance metrics including JS heap size, DOM node count, layout counts, and timing data.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetPerformanceMetricsInput) (*mcp.CallToolResult, GetPerformanceMetricsOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetPerformanceMetricsOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		var metrics []*performance.Metric
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			if _, err := cdp.Call(ctx, tgt, performance.Enable, performance.EnableParams{}); err != nil {
				return err
			}
			res, err := cdp.Call(ctx, tgt, performance.GetMetrics, cdp.Empty{})
			if err != nil {
				return err
			}
			metrics = res.Metrics
			return nil
		}))
		if err != nil {
			return nil, GetPerformanceMetricsOutput{}, err
		}

		entries := make([]MetricEntry, 0, len(metrics))
		for _, m := range metrics {
			entries = append(entries, MetricEntry{Name: m.Name, Value: m.Value})
		}
		return nil, GetPerformanceMetricsOutput{Metrics: entries}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_layout_shifts",
		Description: "Get Cumulative Layout Shift (CLS) data. 'mode' must be 'peek' (keep entries) or 'drain' (consume them).",
		InputSchema: modeSchemaFor[GetLayoutShiftsInput](ModePeek, ModeDrain),
		Annotations: &mcp.ToolAnnotations{},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetLayoutShiftsInput) (*mcp.CallToolResult, GetLayoutShiftsOutput, error) {
		if err := validateMode(input.Mode, ModePeek, ModeDrain); err != nil {
			return nil, GetLayoutShiftsOutput{}, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetLayoutShiftsOutput{}, err
		}

		var shifts []collector.LayoutShiftEntry
		if input.Mode == ModePeek {
			shifts = t.Performance.PeekLayoutShifts(0)
		} else {
			shifts = t.Performance.DrainLayoutShifts(0)
		}
		if shifts == nil {
			shifts = []collector.LayoutShiftEntry{}
		}

		var cls float64
		for _, s := range shifts {
			cls += s.Value
		}

		return nil, GetLayoutShiftsOutput{Shifts: shifts, CumulativeLS: cls}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_coverage",
		Description: "Get CSS and/or JavaScript code coverage data showing used vs unused bytes per file.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetCoverageInput) (*mcp.CallToolResult, GetCoverageOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetCoverageOutput{}, err
		}

		coverageType := input.Type
		if coverageType == "" {
			coverageType = "all"
		}

		var entries []CoverageEntry

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			if coverageType == "js" || coverageType == "all" {
				if _, err := cdp.Call(ctx, tgt, profiler.Enable, cdp.Empty{}); err != nil {
					return err
				}
				if _, err := cdp.Call(ctx, tgt, profiler.StartPreciseCoverage, profiler.StartPreciseCoverageParams{
					CallCount: new(false),
					Detailed:  new(false),
				}); err != nil {
					return err
				}
				covRes, err := cdp.Call(ctx, tgt, profiler.TakePreciseCoverage, cdp.Empty{})
				if err != nil {
					return err
				}
				for _, script := range covRes.Result {
					if script.URL == "" {
						continue
					}
					var usedBytes int64
					for _, fn := range script.Functions {
						for _, r := range fn.Ranges {
							if r.Count > 0 {
								usedBytes += int64(r.EndOffset - r.StartOffset)
							}
						}
					}
					// Approximate total from the last range end offset.
					var totalBytes int64
					if len(script.Functions) > 0 {
						lastFn := script.Functions[len(script.Functions)-1]
						if len(lastFn.Ranges) > 0 {
							totalBytes = int64(lastFn.Ranges[len(lastFn.Ranges)-1].EndOffset)
						}
					}
					pct := float64(0)
					if totalBytes > 0 {
						pct = float64(usedBytes) / float64(totalBytes) * 100
					}
					entries = append(entries, CoverageEntry{
						URL:        script.URL,
						TotalBytes: totalBytes,
						UsedBytes:  usedBytes,
						Percentage: pct,
					})
				}
				_, _ = cdp.Call(ctx, tgt, profiler.StopPreciseCoverage, cdp.Empty{})
			}

			if coverageType == "css" || coverageType == "all" {
				// Collect stylesheet metadata (ID → source URL) by
				// listening for styleSheetAdded events from CSS.enable.
				var sheetMu sync.Mutex
				sheetURLs := make(map[string]string)
				lctx, lcancel := context.WithCancel(ctx)
				if err := chromedp.Listen(lctx, func(ev any) {
					if e, ok := ev.(*cdpcss.EventStyleSheetAdded); ok && e.Header != nil {
						sheetMu.Lock()
						sheetURLs[e.Header.StyleSheetID.String()] = e.Header.SourceURL
						sheetMu.Unlock()
					}
				}); err != nil {
					lcancel()
					return err
				}

				if _, err := cdp.Call(ctx, tgt, cdpcss.Enable, cdp.Empty{}); err != nil {
					lcancel()
					return err
				}

				if _, err := cdp.Call(ctx, tgt, cdpcss.StartRuleUsageTracking, cdp.Empty{}); err != nil {
					lcancel()
					_, _ = cdp.Call(ctx, tgt, cdpcss.Disable, cdp.Empty{})
					return err
				}
				ruleRes, err := cdp.Call(ctx, tgt, cdpcss.StopRuleUsageTracking, cdp.Empty{})
				lcancel()
				_, _ = cdp.Call(ctx, tgt, cdpcss.Disable, cdp.Empty{})
				if err != nil {
					return err
				}
				// Aggregate by stylesheet.
				stylesheets := make(map[string]*CoverageEntry)
				sheetMu.Lock()
				for _, rule := range ruleRes.RuleUsage {
					key := rule.StyleSheetID.String()
					ce, ok := stylesheets[key]
					if !ok {
						url := sheetURLs[key]
						if url == "" {
							url = key // Fall back to stylesheet ID.
						}
						ce = &CoverageEntry{URL: url}
						stylesheets[key] = ce
					}
					size := int64(rule.EndOffset - rule.StartOffset)
					ce.TotalBytes += size
					if rule.Used {
						ce.UsedBytes += size
					}
				}
				sheetMu.Unlock()
				for _, ce := range stylesheets {
					if ce.TotalBytes > 0 {
						ce.Percentage = float64(ce.UsedBytes) / float64(ce.TotalBytes) * 100
					}
					entries = append(entries, *ce)
				}
			}
			return nil
		}))
		if err != nil {
			return nil, GetCoverageOutput{}, err
		}

		return nil, GetCoverageOutput{Entries: entries}, nil
	})
}
