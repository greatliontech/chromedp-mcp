// Command chromedp-mcp runs an MCP server that provides LLMs with browser
// automation tools via Chrome DevTools Protocol.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/tools"
)

// version can be overridden at link time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "chromedp-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("chromedp-mcp", flag.ContinueOnError)
	// Suppress flag.FlagSet's automatic error printing so main() prints
	// parse errors exactly once; help (-h) still prints usage to stderr.
	fs.SetOutput(io.Discard)
	downloadDir := fs.String("download-dir", "", "Directory for saving screenshots, PDFs, and downloads")
	allowedProfiles := fs.String("allowed-profiles", "", "Comma-separated list of Chrome profile display names the LLM may use (e.g. \"Work,Personal\")")
	userDataDir := fs.String("user-data-dir", "", "Chrome/Chromium user data directory for profile discovery (auto-detected if omitted)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(stderr)
			fs.Usage()
			return nil
		}
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := &tools.Options{
		DownloadDir:     expandHome(*downloadDir),
		AllowedProfiles: parseAllowedProfiles(*allowedProfiles),
		UserDataDir:     expandHome(*userDataDir),
	}

	return runServer(ctx, &mcp.StdioTransport{}, opts)
}

func runServer(ctx context.Context, transport mcp.Transport, opts *tools.Options) error {
	mgr := browser.NewManager(ctx)
	defer mgr.CloseAll()

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "chromedp-mcp",
		Version: serverVersion(),
	}, nil)

	tools.Register(srv, mgr, opts)

	err := srv.Run(ctx, transport)
	// Signal cancellation terminates srv.Run with context.Canceled; that is a
	// normal shutdown, not an error. Returning nil lets defer mgr.CloseAll()
	// run and exits 0.
	if err != nil && ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// expandHome expands a leading "~" or "~/" (or "~\") to the current user's
// home directory since MCP clients pass CLI arguments without shell expansion.
// Paths like "~alice/dir" or "~backup" are left untouched.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// parseAllowedProfiles splits a comma-separated list of profile names,
// trims whitespace, skips empty entries, and deduplicates while preserving order.
func parseAllowedProfiles(raw string) []string {
	if raw == "" {
		return nil
	}
	var profiles []string
	seen := make(map[string]struct{})
	for _, p := range strings.Split(raw, ",") {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		profiles = append(profiles, t)
	}
	return profiles
}

func serverVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
