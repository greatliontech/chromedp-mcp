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
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/tools"
)

func main() {
	downloadDir := flag.String("download-dir", "", "Directory for saving screenshots, PDFs, and downloads")
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

	tools.Register(srv, mgr, &tools.Options{
		DownloadDir: *downloadDir,
	})

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
