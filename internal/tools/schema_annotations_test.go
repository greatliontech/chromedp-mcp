package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

func listAllRegisteredTools(t *testing.T, opts *Options) []*mcp.Tool {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := browser.NewManager(ctx)
	srv := mcp.NewServer(&mcp.Implementation{Name: "schema-test", Version: "test"}, nil)
	Register(srv, mgr, opts)

	ct, st := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, st) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "schema-client", Version: "test"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	res, err := sess.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	return res.Tools
}

func TestAllToolsHaveExplicitAnnotations(t *testing.T) {
	dir := t.TempDir()
	tools := listAllRegisteredTools(t, &Options{
		AllowedProfiles: []string{"Work"},
		UserDataDir:     dir,
	})

	byName := make(map[string]*mcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
		if tool.Annotations == nil {
			t.Errorf("tool %q has nil Annotations", tool.Name)
			continue
		}
		if tool.Annotations.DestructiveHint == nil {
			t.Errorf("tool %q has nil DestructiveHint (MCP defaults nil to destructive=true)", tool.Name)
		}
	}

	type wantHint struct {
		readOnly    bool
		destructive bool
		idempotent  bool
	}
	checks := map[string]wantHint{
		// Read-only (screenshot/pdf are read-only when DownloadDir is empty)
		"browser_list":            {true, false, true},
		"browser_list_profiles":   {true, false, true},
		"tab_list":                {true, false, true},
		"wait_for":                {true, false, true},
		"screenshot":              {true, false, true},
		"pdf":                     {true, false, true},
		"query":                   {true, false, true},
		"get_html":                {true, false, true},
		"get_text":                {true, false, true},
		"get_accessibility_tree":  {true, false, true},
		"list_frames":             {true, false, true},
		"get_response_body":       {true, false, true},
		"get_request_body":        {true, false, true},
		"list_intercept_rules":    {true, false, true},
		"get_cookies":             {true, false, true},
		"get_storage":             {true, false, true},
		"get_storage_keys":        {true, false, true},
		"get_performance_metrics": {true, false, true},
		"get_coverage":            {true, false, true},
		"get_dialog":              {true, false, true},

		// Peek/drain buffer readers (must NOT claim ReadOnlyHint: true)
		"get_console_logs":     {false, false, false},
		"get_js_errors":        {false, false, false},
		"get_network_requests": {false, false, false},
		"get_websocket_frames": {false, false, false},
		"get_layout_shifts":    {false, false, false},
		"get_lcp":              {false, false, false},
		"get_downloads":        {false, false, false},

		// Non-destructive, non-idempotent mutations
		"browser_launch":  {false, false, false},
		"browser_connect": {false, false, false},
		"tab_new":         {false, false, false},
		"navigate":        {false, false, false},
		"reload":          {false, false, false},
		"go_back":         {false, false, false},
		"go_forward":      {false, false, false},
		"evaluate":           {false, false, false},
		"add_script":         {false, false, false},
		"add_intercept_rule": {false, false, false},
		"click":              {false, false, false},
		"type":            {false, false, false},
		"select_option":   {false, false, false},
		"submit_form":     {false, false, false},
		"scroll":          {false, false, false},
		"hover":           {false, false, false},
		"focus":           {false, false, false},
		"press_key":       {false, false, false},
		"upload_files":    {false, false, false},
		"handle_dialog":   {false, false, false},

		// Idempotent non-destructive writes
		"browser_activate":              {false, false, true},
		"tab_activate":                  {false, false, true},
		"set_viewport":                  {false, false, true},
		"clear_console":                 {false, false, true},
		"clear_buffers":                 {false, false, true},
		"set_cookie":                    {false, false, true},
		"set_storage":                   {false, false, true},
		"set_extra_headers":             {false, false, true},
		"set_permission":                {false, false, true},
		"set_emulated_media":            {false, false, true},
		"set_ignore_certificate_errors": {false, false, true},
		"set_geolocation":               {false, false, true},
		"set_timezone":                  {false, false, true},
		"set_locale":                    {false, false, true},
		"set_user_agent":                {false, false, true},
		"set_cpu_throttling":            {false, false, true},
		"set_vision_deficiency":         {false, false, true},
		"emulate_network":               {false, false, true},
		"block_urls":                    {false, false, true},
		"clear_intercept_rules":         {false, false, true},

		// Destructive
		"browser_close":         {false, true, false},
		"tab_close":             {false, true, false},
		"delete_cookies":        {false, true, false},
		"delete_storage":        {false, true, false},
		"remove_script":         {false, true, false},
		"remove_intercept_rule": {false, true, false},
	}

	// Enforce exact bijection so newly registered tools cannot bypass annotation checks.
	for name := range byName {
		if _, ok := checks[name]; !ok {
			t.Errorf("registered tool %q is missing from annotation expectations map", name)
		}
	}
	for name, want := range checks {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("expected tool %q to be registered", name)
			continue
		}
		a := tool.Annotations
		if a == nil || a.DestructiveHint == nil {
			continue
		}
		if a.ReadOnlyHint != want.readOnly || *a.DestructiveHint != want.destructive || a.IdempotentHint != want.idempotent {
			t.Errorf("tool %q annotations = (readOnly=%v, destructive=%v, idempotent=%v), want (%v, %v, %v)",
				name, a.ReadOnlyHint, *a.DestructiveHint, a.IdempotentHint,
				want.readOnly, want.destructive, want.idempotent)
		}
	}

	// When DownloadDir is configured, screenshot and pdf can write files to disk
	// and must not advertise ReadOnlyHint=true.
	withDownloads := listAllRegisteredTools(t, &Options{DownloadDir: dir})
	for _, tool := range withDownloads {
		if tool.Name == "screenshot" || tool.Name == "pdf" {
			a := tool.Annotations
			if a == nil || a.DestructiveHint == nil {
				t.Fatalf("%s with DownloadDir missing annotations", tool.Name)
			}
			if a.ReadOnlyHint || *a.DestructiveHint || !a.IdempotentHint {
				t.Errorf("%s with DownloadDir annotations = (readOnly=%v, destructive=%v, idempotent=%v), want (false, false, true)",
					tool.Name, a.ReadOnlyHint, *a.DestructiveHint, a.IdempotentHint)
			}
		}
	}
}

func TestAllEnumParametersExposedInInputSchema(t *testing.T) {
	tools := listAllRegisteredTools(t, &Options{})
	byName := make(map[string]*mcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	type enumCheck struct {
		tool    string
		prop    string
		isArray bool
		want    []string
	}
	checks := []enumCheck{
		{"navigate", "wait_until", false, navigateWaitUntilValues},
		{"screenshot", "format", false, screenshotFormatValues},
		{"get_console_logs", "mode", false, []string{ModePeek, ModeDrain}},
		{"get_js_errors", "mode", false, []string{ModePeek, ModeDrain}},
		{"get_network_requests", "mode", false, []string{ModePeek, ModeDrain}},
		{"get_network_requests", "fields", true, sortedKeys(networkEntryFieldSet)},
		{"get_websocket_frames", "mode", false, []string{ModePeek, ModeDrain}},
		{"get_websocket_frames", "direction", false, wsDirectionValues},
		{"click", "button", false, clickButtonValues},
		{"type", "mode", false, []string{TypeModeReplace, TypeModeAppend}},
		{"press_key", "modifiers", true, pressKeyModifierValues},
		{"set_cookie", "same_site", false, cookieSameSiteValues},
		{"get_storage", "type", false, storageTypeValues},
		{"set_storage", "type", false, storageTypeValues},
		{"delete_storage", "type", false, storageTypeValues},
		{"get_storage_keys", "type", false, storageTypeValues},
		{"get_layout_shifts", "mode", false, []string{ModePeek, ModeDrain}},
		{"get_lcp", "mode", false, []string{ModePeek, ModeDrain}},
		{"clear_buffers", "targets", true, clearBufferTargetValues},
		{"get_coverage", "type", false, coverageTypeValues},
		{"get_downloads", "mode", false, []string{ModePeek, ModeDrain}},
		{"set_permission", "setting", false, permissionSettingValues},
		{"set_vision_deficiency", "type", false, visionDeficiencyValues},
		{"add_intercept_rule", "action", false, interceptActionValues},
		{"add_intercept_rule", "error_reason", false, interceptErrorReasonValues},
	}

	for _, tc := range checks {
		tool, ok := byName[tc.tool]
		if !ok {
			t.Fatalf("tool %q not found", tc.tool)
		}
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema for %s: %v", tc.tool, err)
		}
		var schema map[string]any
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatalf("unmarshal schema for %s: %v", tc.tool, err)
		}
		props, _ := schema["properties"].(map[string]any)
		propMap, ok := props[tc.prop].(map[string]any)
		if !ok {
			t.Errorf("%s: missing property %q in schema", tc.tool, tc.prop)
			continue
		}
		var rawEnum []any
		if tc.isArray {
			items, _ := propMap["items"].(map[string]any)
			rawEnum, _ = items["enum"].([]any)
		} else {
			rawEnum, _ = propMap["enum"].([]any)
		}
		got := make([]string, 0, len(rawEnum))
		for _, v := range rawEnum {
			if s, ok := v.(string); ok {
				got = append(got, s)
			}
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s.%s enum = %v, want %v", tc.tool, tc.prop, got, tc.want)
		}
	}
}

func TestSchemaWithEnumsPanicsOnProgrammerMistakes(t *testing.T) {
	assertPanics := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("%s: expected panic", name)
			}
		}()
		fn()
	}

	assertPanics("missing property", func() {
		schemaWithEnums[ClickInput](map[string][]string{"nonexistent": {"a"}})
	})
	assertPanics("empty enum list", func() {
		schemaWithEnums[ClickInput](map[string][]string{"button": {}})
	})
	assertPanics("non-string property", func() {
		schemaWithEnums[ClickInput](map[string][]string{"click_count": {"1"}})
	})
	assertPanics("nullable *string property", func() {
		schemaWithEnums[SelectOptionInput](map[string][]string{"value": {"a"}})
	})
}

func TestBrowserListProfilesReturnsEmptyArrayNotNull(t *testing.T) {
	dir := t.TempDir()
	localState := `{"profile":{"info_cache":{"Default":{"name":"Personal"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte(localState), 0o600); err != nil {
		t.Fatalf("write Local State: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := browser.NewManager(ctx)
	srv := mcp.NewServer(&mcp.Implementation{Name: "prof-test", Version: "test"}, nil)
	Register(srv, mgr, &Options{
		AllowedProfiles: []string{"NonExistentProfile"},
		UserDataDir:     dir,
	})
	ct, st := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "prof-client", Version: "test"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "browser_list_profiles"})
	if err != nil || res.IsError {
		t.Fatalf("browser_list_profiles failed: err=%v text=%s", err, contentText(res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal StructuredContent: %v", err)
	}
	if string(b) != `{"profiles":[]}` {
		t.Errorf("empty browser_list_profiles JSON = %s, want %s", b, `{"profiles":[]}`)
	}
}

func TestGetConsoleLogsFiltersNonStandardCDPLevels(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `console.assert(false, 'assertion-boom'); console.log('plain-log'); true`,
	})
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"mode":  "drain",
		"level": "assert",
	})
	if len(out.Logs) != 1 || out.Logs[0].Level != "assert" || !strings.Contains(out.Logs[0].Text, "assertion-boom") {
		t.Fatalf("expected 1 assert log, got %+v", out.Logs)
	}
}

func TestRuntimeValidationAcrossTools(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Browser validation (both width and height).
	if err := callToolExpectErr(t, "browser_launch", map[string]any{"width": -1}); !strings.Contains(err, "width") {
		t.Errorf("negative browser_launch width err = %q", err)
	}
	if err := callToolExpectErr(t, "browser_launch", map[string]any{"height": -1}); !strings.Contains(err, "height") {
		t.Errorf("negative browser_launch height err = %q", err)
	}
	if err := callToolExpectErr(t, "browser_connect", map[string]any{"url": "   "}); !strings.Contains(err, "url") {
		t.Errorf("whitespace browser_connect url err = %q", err)
	}

	// Console / JS errors / Downloads negative limits.
	if err := callToolExpectErr(t, "get_console_logs", map[string]any{"tab": tabID, "mode": "peek", "limit": -1}); !strings.Contains(err, "limit") {
		t.Errorf("get_console_logs negative limit err = %q", err)
	}
	if err := callToolExpectErr(t, "get_js_errors", map[string]any{"tab": tabID, "mode": "peek", "limit": -1}); !strings.Contains(err, "limit") {
		t.Errorf("get_js_errors negative limit err = %q", err)
	}
	if err := callToolExpectErr(t, "get_downloads", map[string]any{"mode": "peek", "limit": -1}); !strings.Contains(err, "limit") {
		t.Errorf("get_downloads negative limit err = %q", err)
	}

	// Network status bounds (both min, max, inverted) & empty request_id.
	if err := callToolExpectErr(t, "get_network_requests", map[string]any{"tab": tabID, "mode": "peek", "status_min": -1}); !strings.Contains(err, "status_min") {
		t.Errorf("negative status_min err = %q", err)
	}
	if err := callToolExpectErr(t, "get_network_requests", map[string]any{"tab": tabID, "mode": "peek", "status_max": -1}); !strings.Contains(err, "status_max") {
		t.Errorf("negative status_max err = %q", err)
	}
	if err := callToolExpectErr(t, "get_network_requests", map[string]any{"tab": tabID, "mode": "peek", "status_min": 500, "status_max": 200}); !strings.Contains(err, "status_max") {
		t.Errorf("inverted status range err = %q", err)
	}
	for _, toolName := range []string{"get_response_body", "get_request_body"} {
		if err := callToolExpectErr(t, toolName, map[string]any{"tab": tabID, "request_id": ""}); !strings.Contains(err, "request_id") {
			t.Errorf("%s empty request_id err = %q", toolName, err)
		}
	}
	if err := callToolExpectErr(t, "get_websocket_frames", map[string]any{"tab": tabID, "mode": "peek", "request_id": ""}); !strings.Contains(err, "request_id") {
		t.Errorf("get_websocket_frames empty request_id err = %q", err)
	}

	// Storage keys negative limit.
	if err := callToolExpectErr(t, "get_storage_keys", map[string]any{"tab": tabID, "limit": -1}); !strings.Contains(err, "limit") {
		t.Errorf("get_storage_keys negative limit err = %q", err)
	}

	// Config validation.
	if err := callToolExpectErr(t, "set_permission", map[string]any{"tab": tabID, "name": "", "setting": "granted"}); !strings.Contains(err, "name") {
		t.Errorf("set_permission empty name err = %q", err)
	}
	if err := callToolExpectErr(t, "remove_script", map[string]any{"tab": tabID, "identifier": ""}); !strings.Contains(err, "identifier") {
		t.Errorf("remove_script empty identifier err = %q", err)
	}

	// Emulation validation: set_geolocation partial/range/accuracy.
	geoCases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"lat_only", map[string]any{"tab": tabID, "latitude": 10.0}, "latitude and longitude"},
		{"lon_only", map[string]any{"tab": tabID, "longitude": 10.0}, "latitude and longitude"},
		{"accuracy_only", map[string]any{"tab": tabID, "accuracy": 5.0}, "accuracy requires"},
		{"lat_high", map[string]any{"tab": tabID, "latitude": 91.0, "longitude": 0.0}, "latitude"},
		{"lat_low", map[string]any{"tab": tabID, "latitude": -91.0, "longitude": 0.0}, "latitude"},
		{"lon_high", map[string]any{"tab": tabID, "latitude": 0.0, "longitude": 181.0}, "longitude"},
		{"lon_low", map[string]any{"tab": tabID, "latitude": 0.0, "longitude": -181.0}, "longitude"},
		{"neg_accuracy", map[string]any{"tab": tabID, "latitude": 0.0, "longitude": 0.0, "accuracy": -1.0}, "accuracy"},
	}
	for _, tc := range geoCases {
		if err := callToolExpectErr(t, "set_geolocation", tc.args); !strings.Contains(err, tc.want) {
			t.Errorf("set_geolocation(%s) err = %q, want substring %q", tc.name, err, tc.want)
		}
	}

	// Emulation validation: set_cpu_throttling & emulate_network (including fractional (-1, 0)).
	if err := callToolExpectErr(t, "set_cpu_throttling", map[string]any{"tab": tabID, "rate": -1.0}); !strings.Contains(err, "rate") {
		t.Errorf("set_cpu_throttling negative rate err = %q", err)
	}
	if err := callToolExpectErr(t, "emulate_network", map[string]any{"tab": tabID, "latency": -5.0}); !strings.Contains(err, "latency") {
		t.Errorf("emulate_network negative latency err = %q", err)
	}
	for _, bad := range []float64{-2.0, -0.5} {
		if err := callToolExpectErr(t, "emulate_network", map[string]any{"tab": tabID, "download_throughput": bad}); !strings.Contains(err, "download_throughput") {
			t.Errorf("emulate_network download_throughput=%v err = %q", bad, err)
		}
		if err := callToolExpectErr(t, "emulate_network", map[string]any{"tab": tabID, "upload_throughput": bad}); !strings.Contains(err, "upload_throughput") {
			t.Errorf("emulate_network upload_throughput=%v err = %q", bad, err)
		}
	}
	// -1 (explicit disable) must still succeed.
	callTool[struct{}](t, "emulate_network", map[string]any{
		"tab":                 tabID,
		"download_throughput": -1.0,
		"upload_throughput":   -1.0,
	})

	// Negative SelectorInput.Timeout rejected across all 14 SelectorInput tools.
	timeoutTools := []struct {
		name string
		args map[string]any
	}{
		{"screenshot", map[string]any{"tab": tabID, "timeout": -1}},
		{"query", map[string]any{"tab": tabID, "selector": "h1", "timeout": -1}},
		{"get_html", map[string]any{"tab": tabID, "timeout": -1}},
		{"get_text", map[string]any{"tab": tabID, "timeout": -1}},
		{"get_accessibility_tree", map[string]any{"tab": tabID, "timeout": -1}},
		{"evaluate", map[string]any{"tab": tabID, "expression": "1", "timeout": -1}},
		{"click", map[string]any{"tab": tabID, "selector": "h1", "timeout": -1}},
		{"type", map[string]any{"tab": tabID, "selector": "h1", "text": "x", "mode": "replace", "timeout": -1}},
		{"select_option", map[string]any{"tab": tabID, "selector": "select", "value": "a", "timeout": -1}},
		{"submit_form", map[string]any{"tab": tabID, "selector": "form", "timeout": -1}},
		{"scroll", map[string]any{"tab": tabID, "timeout": -1}},
		{"hover", map[string]any{"tab": tabID, "selector": "h1", "timeout": -1}},
		{"focus", map[string]any{"tab": tabID, "selector": "h1", "timeout": -1}},
		{"upload_files", map[string]any{"tab": tabID, "selector": "input", "paths": []string{}, "timeout": -1}},
	}
	for _, tc := range timeoutTools {
		if err := callToolExpectErr(t, tc.name, tc.args); !strings.Contains(err, "timeout") {
			t.Errorf("%s negative timeout err = %q, want mention of timeout", tc.name, err)
		}
	}

	// Empty selector rejected upfront across all 7 selector-required interaction tools.
	emptySelTools := []struct {
		name string
		args map[string]any
	}{
		{"click", map[string]any{"tab": tabID, "selector": ""}},
		{"type", map[string]any{"tab": tabID, "selector": "", "text": "x", "mode": "replace"}},
		{"select_option", map[string]any{"tab": tabID, "selector": "", "value": "a"}},
		{"submit_form", map[string]any{"tab": tabID, "selector": ""}},
		{"hover", map[string]any{"tab": tabID, "selector": ""}},
		{"focus", map[string]any{"tab": tabID, "selector": ""}},
		{"upload_files", map[string]any{"tab": tabID, "selector": "", "paths": []string{}}},
	}
	for _, tc := range emptySelTools {
		if err := callToolExpectErr(t, tc.name, tc.args); !strings.Contains(err, "selector") {
			t.Errorf("%s empty selector err = %q, want mention of selector", tc.name, err)
		}
	}
}

func TestValidationRunsBeforeTabResolution(t *testing.T) {
	// Fresh server with NO launched browser: upfront validation must fail with
	// the parameter error rather than "no browser running".
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := browser.NewManager(ctx)
	srv := mcp.NewServer(&mcp.Implementation{Name: "precheck-test", Version: "test"}, nil)
	Register(srv, mgr, &Options{})
	ct, st := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "precheck-client", Version: "test"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"set_permission", map[string]any{"name": "", "setting": "granted"}, "permission name"},
		{"remove_script", map[string]any{"identifier": ""}, "identifier"},
		{"set_geolocation", map[string]any{"latitude": 10.0}, "latitude and longitude"},
		{"emulate_network", map[string]any{"download_throughput": -0.5}, "download_throughput"},
		{"get_websocket_frames", map[string]any{"mode": "peek", "request_id": ""}, "request_id"},
		{"get_console_logs", map[string]any{"mode": "peek", "limit": -1}, "limit"},
		{"get_lcp", map[string]any{"mode": "peek", "limit": -1}, "limit"},
		{"clear_buffers", map[string]any{"targets": []string{"bogus"}}, "bogus"},
		{"clear_buffers", map[string]any{"targets": []string{}}, "must not be empty"},
		{"add_intercept_rule", map[string]any{"url_pattern": "", "action": "fail"}, "url_pattern"},
		{"add_intercept_rule", map[string]any{"url_pattern": "*", "action": "fulfill", "status": 99}, "status"},
		{"add_intercept_rule", map[string]any{"url_pattern": "*", "action": "modify"}, "requires at least one"},
		{"remove_intercept_rule", map[string]any{"id": ""}, "rule id"},
		{"click", map[string]any{"selector": ""}, "selector"},
		{"browser_activate", map[string]any{"browser": ""}, "browser ID"},
	}
	for _, tc := range cases {
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err != nil {
			t.Fatalf("%s protocol error: %v", tc.name, err)
		}
		if !res.IsError {
			t.Fatalf("%s expected tool error, got success", tc.name)
		}
		got := contentText(res)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s without browser returned %q, want pre-ResolveTab error containing %q", tc.name, got, tc.want)
		}
	}
}

func TestNetworkEntryFieldSetMatchesStructReflection(t *testing.T) {
	rt := reflect.TypeFor[collector.NetworkEntry]()
	want := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		want = append(want, name)
	}
	slices.Sort(want)

	got := sortedKeys(networkEntryFieldSet)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("networkEntryFieldSet = %v, want %v (from collector.NetworkEntry json tags)", got, want)
	}
}
