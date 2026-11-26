package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cdpcss "github.com/chromedp/cdproto/css"
	"github.com/chromedp/chromedp"
)

// --- geolocation ---

func TestEmulateGeolocationOverrideDefaultAccuracyAndReset(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action":  "set_permission",
		"tab":     tabID,
		"name":    "geolocation",
		"setting": "granted",
	})

	callTool[emulateOutput](t, "emulate", map[string]any{
		"action":    "geolocation",
		"tab":       tabID,
		"latitude":  48.8566,
		"longitude": 2.3522,
		"accuracy":  10.0,
	})

	type evalResult struct {
		Result json.RawMessage `json:"result"`
	}
	readPos := func() struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
		Acc float64 `json:"acc"`
	} {
		t.Helper()
		res := callTool[evalResult](t, "evaluate", map[string]any{
			"tab": tabID,
			"expression": `new Promise((resolve, reject) => {
				navigator.geolocation.getCurrentPosition(
					pos => resolve({lat: pos.coords.latitude, lng: pos.coords.longitude, acc: pos.coords.accuracy}),
					err => reject(err.message),
					{timeout: 5000}
				);
			})`,
		})
		var coords struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
			Acc float64 `json:"acc"`
		}
		if err := json.Unmarshal(res.Result, &coords); err != nil {
			t.Fatalf("unmarshal coords: %v, raw: %s", err, res.Result)
		}
		return coords
	}

	coords := readPos()
	if coords.Lat != 48.8566 || coords.Lng != 2.3522 || coords.Acc != 10 {
		t.Fatalf("expected Paris (48.8566, 2.3522, acc=10), got %+v", coords)
	}

	// Default accuracy = 1 when omitted.
	callTool[emulateOutput](t, "emulate", map[string]any{
		"action":    "geolocation",
		"tab":       tabID,
		"latitude":  51.5074,
		"longitude": -0.1278,
	})
	coords = readPos()
	if coords.Lat != 51.5074 || coords.Lng != -0.1278 || coords.Acc != 1 {
		t.Fatalf("expected London with default acc=1, got %+v", coords)
	}

	// Reset by omitting all coordinate fields.
	callTool[emulateOutput](t, "emulate", map[string]any{
		"action": "geolocation",
		"tab":    tabID,
	})
}

// --- timezone & locale ---

func TestEmulateTimezoneAndLocale(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	type strEval struct {
		Result string `json:"result"`
	}

	if errText := callToolExpectErr(t, "emulate", map[string]any{"action": "timezone", "tab": tabID, "timezone_id": "Not/A/Real/Timezone"}); errText == "" {
		t.Fatal("expected error for invalid timezone")
	}
	callTool[emulateOutput](t, "emulate", map[string]any{
		"action":      "timezone",
		"tab":         tabID,
		"timezone_id": "Pacific/Auckland",
	})
	if res := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "Intl.DateTimeFormat().resolvedOptions().timeZone"}); res.Result != "Pacific/Auckland" {
		t.Fatalf("timezone = %q, want Pacific/Auckland", res.Result)
	}
	callTool[emulateOutput](t, "emulate", map[string]any{
		"action":      "timezone",
		"tab":         tabID,
		"timezone_id": "UTC",
	})
	if res := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "Intl.DateTimeFormat().resolvedOptions().timeZone"}); res.Result != "UTC" {
		t.Fatalf("timezone after reset = %q, want UTC", res.Result)
	}

	// Locale override and reset.
	callTool[emulateOutput](t, "emulate", map[string]any{
		"action": "locale",
		"tab":    tabID,
		"locale": "de_DE",
	})
	if res := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "new Intl.NumberFormat().format(1234.5)"}); !strings.Contains(res.Result, ",") {
		t.Fatalf("German number format = %q, want comma", res.Result)
	}
	callTool[emulateOutput](t, "emulate", map[string]any{
		"action": "locale",
		"tab":    tabID,
		"locale": "",
	})
	if res := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "new Intl.NumberFormat().format(1234.5)"}); !strings.Contains(res.Result, ".") {
		t.Fatalf("default number format after reset = %q, want dot", res.Result)
	}
}

// --- user_agent, cpu_throttling, vision_deficiency ---

func TestEmulateUserAgentCPUAndVisionDeficiency(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	type strEval struct {
		Result string `json:"result"`
	}

	callTool[emulateOutput](t, "emulate", map[string]any{
		"action":     "user_agent",
		"tab":        tabID,
		"user_agent": "TestAgent/2.0",
		"platform":   "TestPlatform",
	})
	if ua := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "navigator.userAgent"}); ua.Result != "TestAgent/2.0" {
		t.Fatalf("userAgent = %q, want TestAgent/2.0", ua.Result)
	}
	if plat := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "navigator.platform"}); plat.Result != "TestPlatform" {
		t.Fatalf("platform = %q, want TestPlatform", plat.Result)
	}

	// CPU throttling (enable, disable, clamp < 1).
	callTool[emulateOutput](t, "emulate", map[string]any{"action": "cpu_throttling", "tab": tabID, "rate": 4.0})
	callTool[emulateOutput](t, "emulate", map[string]any{"action": "cpu_throttling", "tab": tabID, "rate": 0.0})
	callTool[emulateOutput](t, "emulate", map[string]any{"action": "cpu_throttling", "tab": tabID, "rate": 1.0})

	// Vision deficiency values + invalid rejection.
	for _, typ := range visionDeficiencyValues {
		callTool[emulateOutput](t, "emulate", map[string]any{"action": "vision_deficiency", "tab": tabID, "type": typ})
	}
	if errText := callToolExpectErr(t, "emulate", map[string]any{"action": "vision_deficiency", "tab": tabID, "type": "invalid_type"}); !strings.Contains(errText, "invalid_type") {
		t.Errorf("invalid vision type err = %q", errText)
	}
}

// --- media ---

func TestEmulateMediaColorSchemeReducedMotionAndPrint(t *testing.T) {
	tabID := navigateToFixture(t, "config.html")
	defer closeTab(t, tabID)

	type strEval struct {
		Result string `json:"result"`
	}

	callTool[emulateOutput](t, "emulate", map[string]any{
		"action": "media",
		"tab":    tabID,
		"features": []map[string]string{
			{"name": "prefers-color-scheme", "value": "dark"},
			{"name": "prefers-reduced-motion", "value": "reduce"},
		},
	})
	if res := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'"}); res.Result != "dark" {
		t.Fatalf("color-scheme = %q, want dark", res.Result)
	}
	if res := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'reduce' : 'no-preference'"}); res.Result != "reduce" {
		t.Fatalf("reduced-motion = %q, want reduce", res.Result)
	}

	callTool[emulateOutput](t, "emulate", map[string]any{"action": "media", "tab": tabID, "media": "print"})
	if res := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "window.matchMedia('print').matches ? 'print' : 'screen'"}); res.Result != "print" {
		t.Fatalf("media = %q, want print", res.Result)
	}

	// Reset media overrides and verify defaults return.
	callTool[emulateOutput](t, "emulate", map[string]any{"action": "media", "tab": tabID})
	if res := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "window.matchMedia('print').matches ? 'print' : 'screen'"}); res.Result != "screen" {
		t.Fatalf("media after reset = %q, want screen", res.Result)
	}
	if res := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'"}); res.Result != "light" {
		t.Fatalf("color-scheme after reset = %q, want light", res.Result)
	}
}

// --- network ---

func TestEmulateNetworkOfflineLatencyAndThroughput(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	type boolEval struct {
		Result bool `json:"result"`
	}
	type strEval struct {
		Result string `json:"result"`
	}

	// Offline mode blocks network.
	callTool[emulateOutput](t, "emulate", map[string]any{"action": "network", "tab": tabID, "offline": true})
	if res := callTool[boolEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "navigator.onLine"}); res.Result {
		t.Fatal("expected navigator.onLine=false when offline")
	}
	if res := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": `fetch('/api/data').then(() => 'ok').catch(e => 'failed: ' + e.message)`}); !strings.Contains(res.Result, "failed") {
		t.Fatalf("expected fetch to fail offline, got %q", res.Result)
	}

	// Back online (all defaults reset).
	callTool[emulateOutput](t, "emulate", map[string]any{"action": "network", "tab": tabID})
	if res := callTool[boolEval](t, "evaluate", map[string]any{"tab": tabID, "expression": "navigator.onLine"}); !res.Result {
		t.Fatal("expected navigator.onLine=true after reset")
	}
	if res := callTool[strEval](t, "evaluate", map[string]any{"tab": tabID, "expression": `fetch('/api/data').then(() => 'ok').catch(e => 'failed')`}); res.Result != "ok" {
		t.Fatalf("expected fetch to succeed online, got %q", res.Result)
	}

	// Latency adds measurable delay; explicit throughput values accepted.
	callTool[emulateOutput](t, "emulate", map[string]any{
		"action":              "network",
		"tab":                 tabID,
		"latency":             400.0,
		"download_throughput": 1000000,
		"upload_throughput":   500000,
	})
	type numEval struct {
		Result float64 `json:"result"`
	}
	latRes := callTool[numEval](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(async () => {
			const start = Date.now();
			await fetch('/api/data');
			return Date.now() - start;
		})()`,
	})
	if latRes.Result < 300 {
		t.Fatalf("expected >= 300ms latency, got %.0fms", latRes.Result)
	}
	callTool[emulateOutput](t, "emulate", map[string]any{"action": "network", "tab": tabID})
}

// --- pseudo_state ---

func TestEmulatePseudoStateSelectorRefShadowIframeAndNavigationReset(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte(`
			#btn { color: rgb(0, 0, 0); background-color: rgb(255, 255, 255); }
			#btn:hover { color: rgb(255, 0, 0); }
			#btn:focus { background-color: rgb(0, 0, 255); }
			#other { color: rgb(0, 0, 0); }
			#other:hover { color: rgb(0, 128, 0); }
		`))
	})
	mux.HandleFunc("/frame.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><style>
			#fbtn { color: rgb(0, 0, 0); }
			#fbtn:hover { color: rgb(128, 0, 128); }
		</style></head><body><button id="fbtn">FrameBtn</button></body></html>`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><link rel="stylesheet" href="/style.css"></head><body>
			<button id="btn">Btn</button>
			<button id="other">Other</button>
			<div id="host"></div>
			<iframe id="child" src="/frame.html"></iframe>
			<script>
				const sr = document.getElementById('host').attachShadow({mode: 'open'});
				sr.innerHTML = '<style>#sbtn { color: rgb(0,0,0); } #sbtn:hover { color: rgb(255,165,0); }</style><button id="sbtn">ShadowBtn</button>';
			</script>
		</body></html>`))
	})
	mux.HandleFunc("/page2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><p id="p2">Page 2</p></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})

	tb, err := harness.mgr.ResolveTab("", tabID)
	if err != nil {
		t.Fatalf("ResolveTab: %v", err)
	}
	var sheetAdded atomic.Int64
	lctx, lcancel := context.WithCancel(tb.Context())
	defer lcancel()
	if err := chromedp.Listen(lctx, func(ev any) {
		if _, ok := ev.(*cdpcss.EventStyleSheetAdded); ok {
			sheetAdded.Add(1)
		}
	}); err != nil {
		t.Fatalf("chromedp.Listen: %v", err)
	}
	injectStyleProbe := func() int64 {
		t.Helper()
		before := sheetAdded.Load()
		callTool[evaluateOutput](t, "evaluate", map[string]any{
			"tab": tabID,
			"expression": `(function() {
				const s = document.createElement('style');
				s.textContent = '.probe-' + Math.random().toString(36).slice(2) + ' { color: pink; }';
				document.head.appendChild(s);
				return getComputedStyle(document.body).color;
			})()`,
		})
		return sheetAdded.Load() - before
	}
	assertCSSDisabled := func(label string) {
		t.Helper()
		if delta := injectStyleProbe(); delta != 0 {
			t.Errorf("%s: CSS domain remained enabled (EventStyleSheetAdded incremented by %d)", label, delta)
		}
	}

	getStyle := func(sel string, frame string) map[string]string {
		t.Helper()
		args := map[string]any{
			"tab":            tabID,
			"selector":       sel,
			"computed_style": []string{"color", "background-color"},
		}
		if frame != "" {
			args["frame"] = frame
		}
		q := callTool[queryOutput](t, "query", args)
		if len(q.Elements) != 1 {
			t.Fatalf("query(%s, frame=%q) returned %d elements", sel, frame, len(q.Elements))
		}
		return q.Elements[0].ComputedStyle
	}

	// 1. Snapshot ref targeting on a fresh tab BEFORE any CSS query / DOM.getDocument.
	snap := callTool[snapshotOutput](t, "snapshot", map[string]any{"tab": tabID})
	otherRef := findSnapshotRef(t, snap.Snapshot, `"Other"`)
	callTool[emulateOutput](t, "emulate", map[string]any{
		"action": "pseudo_state",
		"tab":    tabID,
		"ref":    otherRef,
		"states": []string{"hover"},
	})
	if st := getStyle("#other", ""); st["color"] != "rgb(0, 128, 0)" {
		t.Fatalf("#other via ref forced :hover color = %q, want rgb(0, 128, 0)", st["color"])
	}

	// 2. CSS selector with duplicate states deduplicated.
	forced := callTool[emulateOutput](t, "emulate", map[string]any{
		"action":   "pseudo_state",
		"tab":      tabID,
		"selector": "#btn",
		"states":   []string{"hover", "focus", "hover"},
	})
	if forced.States == nil || len(*forced.States) != 2 || (*forced.States)[0] != "hover" || (*forced.States)[1] != "focus" {
		t.Fatalf("pseudo_state returned %+v, want [hover focus]", forced.States)
	}
	if st := getStyle("#btn", ""); st["color"] != "rgb(255, 0, 0)" || st["background-color"] != "rgb(0, 0, 255)" {
		t.Fatalf("forced #btn styles = %+v", st)
	}
	if delta := injectStyleProbe(); delta == 0 {
		t.Fatal("expected EventStyleSheetAdded while CSS domain is enabled")
	}

	// 3. Open Shadow DOM targeting via '>>>'.
	callTool[emulateOutput](t, "emulate", map[string]any{
		"action":   "pseudo_state",
		"tab":      tabID,
		"selector": "#host >>> #sbtn",
		"states":   []string{"hover"},
	})
	if st := getStyle("#host >>> #sbtn", ""); st["color"] != "rgb(255, 165, 0)" {
		t.Fatalf("shadow #sbtn forced :hover color = %q, want rgb(255, 165, 0)", st["color"])
	}

	// 4. Same-origin iframe targeting via frame + selector.
	frames := callTool[listFramesOutput](t, "list_frames", map[string]any{"tab": tabID})
	var childFrameID string
	for _, f := range frames.Frames {
		if strings.Contains(f.URL, "/frame.html") {
			childFrameID = f.ID
			break
		}
	}
	if childFrameID == "" {
		t.Fatalf("child iframe not found in %+v", frames.Frames)
	}
	callTool[emulateOutput](t, "emulate", map[string]any{
		"action":   "pseudo_state",
		"tab":      tabID,
		"frame":    childFrameID,
		"selector": "#fbtn",
		"states":   []string{"hover"},
	})
	if st := getStyle("#fbtn", childFrameID); st["color"] != "rgb(128, 0, 128)" {
		t.Fatalf("iframe #fbtn forced :hover color = %q, want rgb(128, 0, 128)", st["color"])
	}

	// Clearing #btn must return {"states":[]} and leave #other intact.
	rawCleared := callToolRaw(t, "emulate", map[string]any{
		"action":   "pseudo_state",
		"tab":      tabID,
		"selector": "#btn",
		"states":   []string{},
	})
	if rawCleared.IsError || !strings.Contains(contentText(rawCleared), `"states":[]`) {
		t.Fatalf("clearing #btn returned %s (err=%v), want \"states\":[]", contentText(rawCleared), rawCleared.IsError)
	}
	if st := getStyle("#other", ""); st["color"] != "rgb(0, 128, 0)" {
		t.Fatalf("#other lost forced :hover when #btn cleared: %+v", st)
	}

	// Clear remaining forced elements and confirm CSS domain disables.
	callTool[emulateOutput](t, "emulate", map[string]any{"action": "pseudo_state", "tab": tabID, "ref": otherRef})
	callTool[emulateOutput](t, "emulate", map[string]any{"action": "pseudo_state", "tab": tabID, "selector": "#host >>> #sbtn"})
	callTool[emulateOutput](t, "emulate", map[string]any{"action": "pseudo_state", "tab": tabID, "frame": childFrameID, "selector": "#fbtn"})
	assertCSSDisabled("after clearing all forced elements")

	// Cross-document navigation resets forced states and disables CSS asynchronously.
	callTool[emulateOutput](t, "emulate", map[string]any{"action": "pseudo_state", "tab": tabID, "selector": "#btn", "states": []string{"hover"}})
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/page2"})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if injectStyleProbe() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("CSS domain stayed enabled after cross-document navigation")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if errText := callToolExpectErr(t, "emulate", map[string]any{"action": "pseudo_state", "tab": tabID, "selector": "#p2", "states": []string{"bogus"}}); !strings.Contains(errText, "bogus") {
		t.Errorf("invalid pseudo state err = %q, want bogus", errText)
	}
	if errText := callToolExpectErr(t, "emulate", map[string]any{"action": "pseudo_state", "tab": tabID, "selector": "#missing", "states": []string{"hover"}, "timeout": 300}); errText == "" {
		t.Error("expected error for missing selector")
	}
}

// --- device ---

func TestEmulateDevicePortraitLandscapeAndReset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body>Responsive</body></html>`))
	}))
	defer srv.Close()

	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})

	readEnv := func() struct {
		Width       int     `json:"width"`
		Height      int     `json:"height"`
		DPR         float64 `json:"dpr"`
		UA          string  `json:"ua"`
		TouchPoints int     `json:"touch_points"`
	} {
		t.Helper()
		ev := callTool[evaluateOutput](t, "evaluate", map[string]any{
			"tab": tabID,
			"expression": `({
				width: window.innerWidth,
				height: window.innerHeight,
				dpr: window.devicePixelRatio,
				ua: navigator.userAgent,
				touch_points: navigator.maxTouchPoints,
			})`,
		})
		var got struct {
			Width       int     `json:"width"`
			Height      int     `json:"height"`
			DPR         float64 `json:"dpr"`
			UA          string  `json:"ua"`
			TouchPoints int     `json:"touch_points"`
		}
		if err := json.Unmarshal(ev.Result, &got); err != nil {
			t.Fatalf("unmarshal env: %v (raw=%s)", err, ev.Result)
		}
		return got
	}

	baseline := readEnv()

	out := callTool[emulateOutput](t, "emulate", map[string]any{
		"action": "device",
		"tab":    tabID,
		"device": "iPhone 15 Pro",
	})
	if out.Device != "iPhone 15 Pro" || out.Width != 393 || out.Height != 659 || out.DeviceScaleFactor != 3 || !out.Mobile || !out.Touch || out.Landscape || !strings.Contains(out.UserAgent, "iPhone") {
		t.Fatalf("emulate(device) portrait output = %+v", out)
	}
	env := readEnv()
	if env.Width != 393 || env.Height != 659 || env.DPR != 3 || !strings.Contains(env.UA, "iPhone") || env.TouchPoints == 0 {
		t.Fatalf("portrait page env = %+v, want 393x659 @3x iPhone touch>0", env)
	}

	outLand := callTool[emulateOutput](t, "emulate", map[string]any{
		"action":    "device",
		"tab":       tabID,
		"device":    "iPhone 15 Pro",
		"landscape": true,
	})
	if !outLand.Landscape || outLand.Width != 734 || outLand.Height != 343 {
		t.Fatalf("emulate(device) landscape output = %+v", outLand)
	}
	if envLand := readEnv(); envLand.Width != 734 || envLand.Height != 343 {
		t.Fatalf("landscape page env = %+v, want 734x343", envLand)
	}

	resetOut := callTool[emulateOutput](t, "emulate", map[string]any{
		"action": "device",
		"tab":    tabID,
		"device": "reset",
	})
	if resetOut.Device != "reset" {
		t.Fatalf("reset output = %+v", resetOut)
	}
	envReset := readEnv()
	if envReset.UA != baseline.UA || envReset.TouchPoints != 0 || envReset.Width != baseline.Width || envReset.Height != baseline.Height || envReset.DPR != baseline.DPR {
		t.Fatalf("reset page env = %+v, want baseline %+v", envReset, baseline)
	}

	if errText := callToolExpectErr(t, "emulate", map[string]any{"action": "device", "tab": tabID, "device": "Nokia 3310"}); !strings.Contains(errText, "Nokia 3310") {
		t.Errorf("unknown device err = %q, want Nokia 3310", errText)
	}
	if errText := callToolExpectErr(t, "emulate", map[string]any{"action": "device", "tab": tabID, "device": "reset", "landscape": true}); !strings.Contains(errText, "landscape") {
		t.Errorf("reset+landscape err = %q, want landscape", errText)
	}
}
