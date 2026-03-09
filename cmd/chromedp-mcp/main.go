// Command chromedp-mcp runs an MCP server that provides LLMs with browser
// automation tools via Chrome DevTools Protocol.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
	"github.com/thegrumpylion/chromedp-mcp/internal/tools"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	mgr := browser.NewManager(ctx)
	defer mgr.CloseAll()

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "chromedp-mcp",
		Version: "0.1.0",
	}, nil)

	tools.Register(srv, mgr)

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
