package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
)

// testHarness holds the shared test infrastructure.
type testHarness struct {
	ctx         context.Context
	cancel      context.CancelFunc
	httpSrv     *httptest.Server
	mgr         *browser.Manager
	session     *mcp.ClientSession
	downloadDir string
}

// Global harness, initialized in TestMain.
var harness *testHarness

func TestMain(m *testing.M) {
	ctx, cancel := context.WithCancel(context.Background())

	// Start test HTTP server serving fixtures.
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir("testdata")))
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","value":42}`)
	})
	mux.HandleFunc("/api/not-found", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "submitted")
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/page2.html", http.StatusFound)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><h1>Slow Page</h1></body></html>`)
	})
	mux.HandleFunc("/api/unicode", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, `{"message":"héllo wörld 🌍","emoji":"🚀✨"}`)
	})
	// Serve downloadable files.
	mux.HandleFunc("/download/test-file.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="test-file.txt"`)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello from download test")
	})
	mux.HandleFunc("/download/data.csv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="data.csv"`)
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprint(w, "name,value\nalpha,1\nbeta,2\n")
	})

	// Serve a tiny PNG for image tests.
	mux.HandleFunc("/image.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		// 1x1 red pixel PNG.
		png := []byte{
			0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
			0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
			0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00,
			0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
			0x00, 0x00, 0x03, 0x00, 0x01, 0x36, 0x28, 0x19, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
		}
		w.Write(png)
	})
	httpSrv := httptest.NewServer(mux)

	// Create a temp download directory for tests.
	downloadDir, err := os.MkdirTemp("", "chromedp-mcp-test-downloads-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp download dir: %v\n", err)
		cancel()
		httpSrv.Close()
		os.Exit(1)
	}

	// Create browser manager.
	mgr := browser.NewManager(ctx)

	// Create MCP server and register tools.
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "chromedp-mcp-test",
		Version: "test",
	}, nil)
	Register(srv, mgr, &Options{DownloadDir: downloadDir})

	// Connect an in-memory MCP client to the server.
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() {
		_ = srv.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "test-client",
		Version: "test",
	}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect MCP client: %v\n", err)
		cancel()
		httpSrv.Close()
		os.Exit(1)
	}

	// Pre-launch a browser so tests don't each pay the startup cost.
	_, err = mgr.Launch(browser.LaunchOptions{
		Headless:    true,
		Width:       1280,
		Height:      720,
		DownloadDir: downloadDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to launch browser: %v\n", err)
		cancel()
		httpSrv.Close()
		os.Exit(1)
	}

	harness = &testHarness{
		ctx:         ctx,
		cancel:      cancel,
		httpSrv:     httpSrv,
		mgr:         mgr,
		session:     session,
		downloadDir: downloadDir,
	}

	code := m.Run()

	mgr.CloseAll()
	httpSrv.Close()
	cancel()
	os.RemoveAll(downloadDir)
	os.Exit(code)
}

// callTool calls an MCP tool and unmarshals the structured output into dst.
// It fails the test if the tool returns an error.
func callTool[T any](t *testing.T, name string, args any) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(harness.ctx, 30*time.Second)
	defer cancel()

	result, err := harness.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s) returned error: %s", name, contentText(result))
	}

	var out T
	// The structured output is in StructuredContent.
	if result.StructuredContent != nil {
		b, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatalf("marshal StructuredContent: %v", err)
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal StructuredContent into %T: %v\nraw: %s", out, err, b)
		}
		return out
	}
	// Fall back to parsing from text content.
	text := contentText(result)
	if text != "" {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("unmarshal text content into %T: %v\nraw: %s", out, err, text)
		}
	}
	return out
}

// callToolRaw calls an MCP tool and returns the raw result.
func callToolRaw(t *testing.T, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(harness.ctx, 30*time.Second)
	defer cancel()

	result, err := harness.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return result
}

// callToolExpectErr calls an MCP tool and expects it to return an error.
// Returns the error text. Fails the test if the tool succeeds.
func callToolExpectErr(t *testing.T, name string, args any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(harness.ctx, 10*time.Second)
	defer cancel()

	result, err := harness.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		// Protocol-level error — that's also an error, return it.
		return err.Error()
	}
	if !result.IsError {
		t.Fatalf("CallTool(%s) expected error, got success: %s", name, contentText(result))
	}
	return contentText(result)
}

// fixtureURL returns the test server URL for a fixture file.
func fixtureURL(path string) string {
	return harness.httpSrv.URL + "/" + path
}

// contentText extracts the first text content from a CallToolResult.
func contentText(r *mcp.CallToolResult) string {
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// navigateToFixture is a helper that navigates a fresh tab to a fixture page.
// It returns the tab ID. The tab should be closed by the caller.
func navigateToFixture(t *testing.T, fixture string) string {
	t.Helper()
	out := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL(fixture),
	})
	if out.TabID == "" {
		t.Fatal("tab_new returned empty tab ID")
	}
	// Give the page time to execute inline scripts.
	time.Sleep(500 * time.Millisecond)
	return out.TabID
}

// closeTab is a helper that closes a tab.
func closeTab(t *testing.T, tabID string) {
	t.Helper()
	callTool[struct{}](t, "tab_close", map[string]any{"tab": tabID})
}
