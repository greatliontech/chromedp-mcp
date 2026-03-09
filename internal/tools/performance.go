package tools

import (
	"context"

	cdpcss "github.com/chromedp/cdproto/css"
	"github.com/chromedp/cdproto/performance"
	"github.com/chromedp/cdproto/profiler"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
	"github.com/thegrumpylion/chromedp-mcp/internal/collector"
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
	Peek bool `json:"peek,omitempty" jsonschema:"If true do not clear the buffer (default false)"`
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

		var metrics []*performance.Metric
		err = chromedp.Run(t.Context(), chromedp.ActionFunc(func(ctx context.Context) error {
			if err := performance.Enable().Do(ctx); err != nil {
				return err
			}
			var err error
			metrics, err = performance.GetMetrics().Do(ctx)
			return err
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
		Description: "Get Cumulative Layout Shift (CLS) data. By default drains the buffer.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetLayoutShiftsInput) (*mcp.CallToolResult, GetLayoutShiftsOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetLayoutShiftsOutput{}, err
		}

		var shifts []collector.LayoutShiftEntry
		if input.Peek {
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

		err = chromedp.Run(t.Context(), chromedp.ActionFunc(func(ctx context.Context) error {
			if coverageType == "js" || coverageType == "all" {
				if err := profiler.Enable().Do(ctx); err != nil {
					return err
				}
				if _, err := profiler.StartPreciseCoverage().WithCallCount(false).WithDetailed(false).Do(ctx); err != nil {
					return err
				}
				result, _, err := profiler.TakePreciseCoverage().Do(ctx)
				if err != nil {
					return err
				}
				for _, script := range result {
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
				_ = profiler.StopPreciseCoverage().Do(ctx)
			}

			if coverageType == "css" || coverageType == "all" {
				if err := cdpcss.StartRuleUsageTracking().Do(ctx); err != nil {
					return err
				}
				ruleUsage, err := cdpcss.StopRuleUsageTracking().Do(ctx)
				if err != nil {
					return err
				}
				// Aggregate by stylesheet.
				stylesheets := make(map[string]*CoverageEntry)
				for _, rule := range ruleUsage {
					ce, ok := stylesheets[rule.StyleSheetID.String()]
					if !ok {
						ce = &CoverageEntry{URL: rule.StyleSheetID.String()}
						stylesheets[rule.StyleSheetID.String()] = ce
					}
					size := int64(rule.EndOffset - rule.StartOffset)
					ce.TotalBytes += size
					if rule.Used {
						ce.UsedBytes += size
					}
				}
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
