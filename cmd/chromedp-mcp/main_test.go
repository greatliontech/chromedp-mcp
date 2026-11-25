package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/tools"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "absolute path", in: "/tmp/downloads", want: "/tmp/downloads"},
		{name: "relative path", in: "downloads/sub", want: "downloads/sub"},
		{name: "bare tilde", in: "~", want: home},
		{name: "tilde slash", in: "~/Downloads", want: filepath.Join(home, "Downloads")},
		{name: "tilde nested", in: "~/a/b/c", want: filepath.Join(home, "a", "b", "c")},
		{name: "other user untouched", in: "~alice/Downloads", want: "~alice/Downloads"},
		{name: "tilde prefix filename untouched", in: "~backup", want: "~backup"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandHome(tc.in); got != tc.want {
				t.Errorf("expandHome(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseAllowedProfiles(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty", raw: "", want: nil},
		{name: "only commas and spaces", raw: " , , ", want: nil},
		{name: "single", raw: "Work", want: []string{"Work"}},
		{name: "trims whitespace", raw: " Work , Personal ", want: []string{"Work", "Personal"}},
		{name: "dedups adjacent", raw: "Work,Work,Personal", want: []string{"Work", "Personal"}},
		{name: "dedups non-adjacent preserving order", raw: "Work,Personal,Work,Test,Personal", want: []string{"Work", "Personal", "Test"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAllowedProfiles(tc.raw)
			if !slices.Equal(got, tc.want) {
				t.Errorf("parseAllowedProfiles(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestServerVersion(t *testing.T) {
	orig := version
	t.Cleanup(func() { version = orig })

	version = "1.2.3"
	if got := serverVersion(); got != "1.2.3" {
		t.Errorf("serverVersion() with ldflags override = %q, want %q", got, "1.2.3")
	}

	version = "dev"
	if got := serverVersion(); got == "" || got == "0.1.0" {
		t.Errorf("serverVersion() = %q, want non-empty non-hardcoded version", got)
	}
}

func TestRunFlags(t *testing.T) {
	var stderr bytes.Buffer
	if err := run([]string{"-h"}, &stderr); err != nil {
		t.Errorf("run(-h) returned error: %v", err)
	}
	if err := run([]string{"--unknown-flag"}, &stderr); err == nil {
		t.Error("run(--unknown-flag) should return error")
	}
}

func TestRunServerCleanShutdownOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	errCh := make(chan error, 1)
	go func() {
		errCh <- runServer(ctx, serverTransport, &tools.Options{})
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("client.Connect: %v", err)
	}
	defer session.Close()

	// Cancel server context (simulating SIGINT/SIGTERM) — runServer must return
	// nil so main exits 0 after running deferred browser cleanup.
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runServer returned %v on context cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runServer to exit on context cancel")
	}
}
