package tools

import (
	"cmp"
	"context"
	"slices"
	"sync"

	"github.com/chromedp/cdproto/cdp"
	cdpcss "github.com/chromedp/cdproto/css"
	"github.com/chromedp/cdproto/dom"
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

// GetLCPInput is the input for get_lcp.
type GetLCPInput struct {
	TabInput
	Mode  string `json:"mode" jsonschema:"Read mode: 'peek' to keep entries in the buffer, 'drain' to consume them. Required."`
	Limit int    `json:"limit,omitempty" jsonschema:"Max entries to return (default all)"`
}

// GetLCPOutput is the output for get_lcp.
type GetLCPOutput struct {
	Entries []collector.LCPEntry `json:"entries"`
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

var coverageTypeValues = []string{"css", "js", "all"}

func registerPerformanceTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_performance_metrics",
		Description: "Get Chrome runtime performance metrics including JS heap size, DOM node count, layout counts, and timing data.",
		Annotations: readOnlyAnnotations(),
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
		Annotations: mutatingAnnotations(),
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
			// Per W3C Layout Instability / Core Web Vitals CLS spec, shifts
			// within 500ms of recent user input do not contribute to CLS.
			if !s.HadRecentInput {
				cls += s.Value
			}
		}

		return nil, GetLayoutShiftsOutput{Shifts: shifts, CumulativeLS: cls}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_lcp",
		Description: "Get Largest Contentful Paint (LCP) timeline entries captured for a tab. 'mode' must be 'peek' (keep entries) or 'drain' (consume returned entries).",
		InputSchema: modeSchemaFor[GetLCPInput](ModePeek, ModeDrain),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetLCPInput) (*mcp.CallToolResult, GetLCPOutput, error) {
		if err := validateMode(input.Mode, ModePeek, ModeDrain); err != nil {
			return nil, GetLCPOutput{}, err
		}
		if err := validateLimit(input.Limit); err != nil {
			return nil, GetLCPOutput{}, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetLCPOutput{}, err
		}

		var entries []collector.LCPEntry
		if input.Mode == ModePeek {
			entries = t.Performance.PeekLCP(input.Limit)
		} else {
			entries = t.Performance.DrainLCP(input.Limit)
		}
		if entries == nil {
			entries = []collector.LCPEntry{}
		}
		return nil, GetLCPOutput{Entries: entries}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_coverage",
		Description: "Get CSS and/or JavaScript code coverage data showing used vs unused bytes per file.",
		InputSchema: schemaWithEnums[GetCoverageInput](map[string][]string{
			"type": coverageTypeValues,
		}),
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetCoverageInput) (*mcp.CallToolResult, GetCoverageOutput, error) {
		if err := validateEnum("coverage type", input.Type, true, coverageTypeValues); err != nil {
			return nil, GetCoverageOutput{}, err
		}
		coverageType := input.Type
		if coverageType == "" {
			coverageType = "all"
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetCoverageOutput{}, err
		}

		entries := []CoverageEntry{}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		if coverageType == "js" || coverageType == "all" {
			err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				return collectJSCoverage(ctx, tgt, &entries)
			}))
			if err != nil {
				return nil, GetCoverageOutput{}, err
			}
		}
		if coverageType == "css" || coverageType == "all" {
			err = t.WithCSSCoverage(tctx, func(ctx context.Context, tgt *chromedp.Target) error {
				return collectCSSCoverage(ctx, tgt, &entries)
			})
			if err != nil {
				return nil, GetCoverageOutput{}, err
			}
		}

		return nil, GetCoverageOutput{Entries: entries}, nil
	})
}

func collectJSCoverage(ctx context.Context, tgt *chromedp.Target, entries *[]CoverageEntry) error {
	if _, err := cdp.Call(ctx, tgt, profiler.Enable, cdp.Empty{}); err != nil {
		return err
	}
	defer func() {
		cleanCtx, cancel := cleanupContext(ctx)
		defer cancel()
		_, _ = cdp.Call(cleanCtx, tgt, profiler.Disable, cdp.Empty{})
	}()

	if _, err := cdp.Call(ctx, tgt, profiler.StartPreciseCoverage, profiler.StartPreciseCoverageParams{
		CallCount: new(false),
		Detailed:  new(false),
	}); err != nil {
		return err
	}
	defer func() {
		cleanCtx, cancel := cleanupContext(ctx)
		defer cancel()
		_, _ = cdp.Call(cleanCtx, tgt, profiler.StopPreciseCoverage, cdp.Empty{})
	}()

	covRes, err := cdp.Call(ctx, tgt, profiler.TakePreciseCoverage, cdp.Empty{})
	if err != nil {
		return err
	}
	for _, script := range covRes.Result {
		if script == nil || script.URL == "" {
			continue
		}
		usedBytes, totalBytes := computeJSScriptCoverage(script)
		pct := float64(0)
		if totalBytes > 0 {
			pct = float64(usedBytes) / float64(totalBytes) * 100
		}
		*entries = append(*entries, CoverageEntry{
			URL:        script.URL,
			TotalBytes: totalBytes,
			UsedBytes:  usedBytes,
			Percentage: pct,
		})
	}
	return nil
}

// computeJSScriptCoverage computes non-overlapping used bytes and total bytes
// for a V8 ScriptCoverage report. V8 emits nested ranges (e.g. a top-level
// script range [0, N) with count=1 containing nested function ranges with
// count=0 or count>0); inner ranges override outer ranges, so simply summing
// ranges with Count>0 would double-count nested functions and exceed 100%.
func computeJSScriptCoverage(script *profiler.ScriptCoverage) (usedBytes, totalBytes int64) {
	type covRange struct {
		start int64
		end   int64
		used  bool
		order int
	}
	var ranges []covRange
	order := 0
	for _, fn := range script.Functions {
		if fn == nil {
			continue
		}
		for _, r := range fn.Ranges {
			if r == nil || r.StartOffset < 0 || r.EndOffset <= r.StartOffset {
				continue
			}
			if r.EndOffset > totalBytes {
				totalBytes = r.EndOffset
			}
			ranges = append(ranges, covRange{
				start: r.StartOffset,
				end:   r.EndOffset,
				used:  r.Count > 0,
				order: order,
			})
			order++
		}
	}
	if len(ranges) == 0 {
		return 0, 0
	}

	// Sort parent/enclosing ranges before nested child ranges:
	// start ascending, end descending, original V8 emission order ascending.
	slices.SortFunc(ranges, func(a, b covRange) int {
		if c := cmp.Compare(a.start, b.start); c != 0 {
			return c
		}
		if c := cmp.Compare(b.end, a.end); c != 0 {
			return c
		}
		return cmp.Compare(a.order, b.order)
	})

	// Sweep nested ranges with a stack so each byte offset is governed by its
	// innermost enclosing range.
	stack := make([]covRange, 0, 8)
	var cursor int64
	advance := func(to int64) {
		if to <= cursor {
			return
		}
		if len(stack) > 0 && stack[len(stack)-1].used {
			usedBytes += to - cursor
		}
		cursor = to
	}

	for _, r := range ranges {
		for len(stack) > 0 && stack[len(stack)-1].end <= r.start {
			advance(stack[len(stack)-1].end)
			stack = stack[:len(stack)-1]
		}
		advance(r.start)
		stack = append(stack, r)
	}
	for len(stack) > 0 {
		advance(stack[len(stack)-1].end)
		stack = stack[:len(stack)-1]
	}
	return usedBytes, totalBytes
}

func collectCSSCoverage(ctx context.Context, tgt *chromedp.Target, entries *[]CoverageEntry) error {
	// Collect stylesheet metadata (ID → source URL) by
	// listening for styleSheetAdded events from CSS.enable.
	var sheetMu sync.Mutex
	sheetURLs := make(map[string]string)
	lctx, lcancel := context.WithCancel(ctx)
	defer lcancel()
	if err := chromedp.Listen(lctx, func(ev any) {
		if e, ok := ev.(*cdpcss.EventStyleSheetAdded); ok && e.Header != nil {
			sheetMu.Lock()
			sheetURLs[e.Header.StyleSheetID.String()] = e.Header.SourceURL
			sheetMu.Unlock()
		}
	}); err != nil {
		return err
	}

	if _, err := cdp.Call(ctx, tgt, dom.Enable, dom.EnableParams{}); err != nil {
		return err
	}
	if _, err := cdp.Call(ctx, tgt, cdpcss.Enable, cdp.Empty{}); err != nil {
		return err
	}
	defer func() {
		cleanCtx, cancel := cleanupContext(ctx)
		defer cancel()
		_, _ = cdp.Call(cleanCtx, tgt, cdpcss.Disable, cdp.Empty{})
	}()

	if _, err := cdp.Call(ctx, tgt, cdpcss.StartRuleUsageTracking, cdp.Empty{}); err != nil {
		return err
	}
	ruleRes, err := cdp.Call(ctx, tgt, cdpcss.StopRuleUsageTracking, cdp.Empty{})
	lcancel()
	if err != nil {
		return err
	}

	// Aggregate rule byte ranges per stylesheet in deterministic order.
	// Using interval union prevents double-counting overlapping CSS nesting rules.
	bySheet := make(map[string][]*cdpcss.RuleUsage)
	var keys []string
	for _, rule := range ruleRes.RuleUsage {
		if rule == nil || rule.StartOffset < 0 || rule.EndOffset <= rule.StartOffset {
			continue
		}
		key := rule.StyleSheetID.String()
		if _, ok := bySheet[key]; !ok {
			keys = append(keys, key)
		}
		bySheet[key] = append(bySheet[key], rule)
	}
	slices.Sort(keys)

	sheetMu.Lock()
	defer sheetMu.Unlock()
	for _, key := range keys {
		url := sheetURLs[key]
		if url == "" {
			url = key // Fall back to stylesheet ID.
		}
		usedBytes, totalBytes := computeCSSSheetCoverage(bySheet[key])
		pct := float64(0)
		if totalBytes > 0 {
			pct = float64(usedBytes) / float64(totalBytes) * 100
		}
		*entries = append(*entries, CoverageEntry{
			URL:        url,
			TotalBytes: totalBytes,
			UsedBytes:  usedBytes,
			Percentage: pct,
		})
	}
	return nil
}

type byteSpan struct {
	start int64
	end   int64
}

// unionSpanBytes returns the total length of the union of half-open [start, end)
// intervals, avoiding double-counting when CSS rules nest or overlap.
func unionSpanBytes(spans []byteSpan) int64 {
	if len(spans) == 0 {
		return 0
	}
	slices.SortFunc(spans, func(a, b byteSpan) int {
		if c := cmp.Compare(a.start, b.start); c != 0 {
			return c
		}
		return cmp.Compare(a.end, b.end)
	})
	var total int64
	curStart, curEnd := spans[0].start, spans[0].end
	for _, s := range spans[1:] {
		if s.start <= curEnd {
			if s.end > curEnd {
				curEnd = s.end
			}
		} else {
			total += curEnd - curStart
			curStart, curEnd = s.start, s.end
		}
	}
	return total + (curEnd - curStart)
}

// computeCSSSheetCoverage computes non-overlapping used and total bytes across
// a stylesheet's CSS.RuleUsage entries (handling nested/overlapping CSS rules).
func computeCSSSheetCoverage(rules []*cdpcss.RuleUsage) (usedBytes, totalBytes int64) {
	allSpans := make([]byteSpan, 0, len(rules))
	usedSpans := make([]byteSpan, 0, len(rules))
	for _, r := range rules {
		if r == nil || r.StartOffset < 0 || r.EndOffset <= r.StartOffset {
			continue
		}
		sp := byteSpan{start: int64(r.StartOffset), end: int64(r.EndOffset)}
		allSpans = append(allSpans, sp)
		if r.Used {
			usedSpans = append(usedSpans, sp)
		}
	}
	return unionSpanBytes(usedSpans), unionSpanBytes(allSpans)
}
