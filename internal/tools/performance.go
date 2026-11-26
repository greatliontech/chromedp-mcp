package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// GetPerformanceInput is the input for get_performance.
type GetPerformanceInput struct {
	TabInput
	Mode  string `json:"mode,omitempty" jsonschema:"Read mode for buffered layout_shifts and lcp entries: 'peek' (default, keep entries) or 'drain' (consume them)."`
	Limit int    `json:"limit,omitempty" jsonschema:"Max lcp entries to return (default all)."`
}

// MetricEntry represents a single Chrome runtime performance metric.
type MetricEntry struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// GetPerformanceOutput is the output for get_performance.
type GetPerformanceOutput struct {
	Metrics      []MetricEntry                `json:"metrics"`
	LayoutShifts []collector.LayoutShiftEntry `json:"layout_shifts"`
	CLS          float64                      `json:"cls"`
	LCP          []collector.LCPEntry         `json:"lcp"`
}

var performanceModeValues = []string{ModePeek, ModeDrain}

func registerPerformanceTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_performance",
		Description: "Get Chrome runtime performance metrics (JS heap, DOM nodes, layout counts, timings), layout shifts with W3C session-window CLS, and Largest Contentful Paint (LCP) entries. 'mode' controls buffered layout_shifts and lcp: 'peek' (default, keep) or 'drain' (consume).",
		InputSchema: schemaWithEnums[GetPerformanceInput](map[string][]string{
			"mode": performanceModeValues,
		}),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetPerformanceInput) (*mcp.CallToolResult, GetPerformanceOutput, error) {
		if err := validateEnum("mode", input.Mode, true, performanceModeValues); err != nil {
			return nil, GetPerformanceOutput{}, err
		}
		if err := validateLimit(input.Limit); err != nil {
			return nil, GetPerformanceOutput{}, err
		}
		mode := input.Mode
		if mode == "" {
			mode = ModePeek
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetPerformanceOutput{}, err
		}
		metrics, err := t.GetPerformanceMetrics(ctx)
		if err != nil {
			return nil, GetPerformanceOutput{}, err
		}

		entries := make([]MetricEntry, 0, len(metrics))
		for _, m := range metrics {
			entries = append(entries, MetricEntry{Name: m.Name, Value: m.Value})
		}

		var (
			shifts []collector.LayoutShiftEntry
			lcp    []collector.LCPEntry
		)
		if mode == ModePeek {
			shifts = t.Performance.PeekLayoutShifts(0)
			lcp = t.Performance.PeekLCP(input.Limit)
		} else {
			shifts = t.Performance.DrainLayoutShifts(0)
			lcp = t.Performance.DrainLCP(input.Limit)
		}
		if shifts == nil {
			shifts = []collector.LayoutShiftEntry{}
		}
		if lcp == nil {
			lcp = []collector.LCPEntry{}
		}

		return nil, GetPerformanceOutput{
			Metrics:      entries,
			LayoutShifts: shifts,
			CLS:          collector.ComputeCLS(shifts),
			LCP:          lcp,
		}, nil
	})
}
