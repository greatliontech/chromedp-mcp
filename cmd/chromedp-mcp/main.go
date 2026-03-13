// Command chromedp-mcp runs an MCP server that provides LLMs with browser
// automation tools via Chrome DevTools Protocol.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/tools"
)

func main() {
	downloadDir := flag.String("download-dir", "", "Directory for saving screenshots, PDFs, and downloads")
	allowedProfiles := flag.String("allowed-profiles", "", "Comma-separated list of Chrome profile display names the LLM may use (e.g. \"Work,Personal\")")
	flag.Parse()

	// Expand ~ to the user's home directory since MCP clients pass
	// args directly without shell expansion.
	if strings.HasPrefix(*downloadDir, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			*downloadDir = filepath.Join(home, (*downloadDir)[1:])
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	mgr := browser.NewManager(ctx)
	defer mgr.CloseAll()

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "chromedp-mcp",
		Version: "0.1.0",
	}, nil)

	var profiles []string
	if *allowedProfiles != "" {
		for _, p := range strings.Split(*allowedProfiles, ",") {
			if t := strings.TrimSpace(p); t != "" {
				profiles = append(profiles, t)
			}
		}
		profiles = slices.Compact(profiles)
	}

	tools.Register(srv, mgr, &tools.Options{
		DownloadDir:     *downloadDir,
		AllowedProfiles: profiles,
	})

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
