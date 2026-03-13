package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// profileHarness creates a standalone MCP server+client with profile support
// enabled, using a crafted temp user data directory. The caller can then call
// tools via the returned session.
type profileHarness struct {
	session *mcp.ClientSession
	cancel  context.CancelFunc
}

func newProfileHarness(t *testing.T, allowedProfiles []string, localState string) *profileHarness {
	t.Helper()

	// Create a temp user data dir with a crafted Local State file.
	userDataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(userDataDir, "Local State"), []byte(localState), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(harness.ctx)

	// Use the existing browser manager — we won't actually launch browsers.
	mgr := browser.NewManager(ctx)

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "chromedp-mcp-profile-test",
		Version: "test",
	}, nil)
	Register(srv, mgr, &Options{
		AllowedProfiles: allowedProfiles,
		UserDataDir:     userDataDir,
	})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() {
		_ = srv.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "profile-test-client",
		Version: "test",
	}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("connect MCP client: %v", err)
	}

	t.Cleanup(func() {
		mgr.CloseAll()
		cancel()
	})

	return &profileHarness{session: session, cancel: cancel}
}

func (ph *profileHarness) callTool(t *testing.T, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := ph.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return result
}

const testLocalState = `{
	"profile": {
		"info_cache": {
			"Default": {"name": "Work"},
			"Profile 2": {"name": "Personal"},
			"Profile 3": {"name": "Testing"}
		}
	}
}`

// ---------------------------------------------------------------------------
// browser_list_profiles
// ---------------------------------------------------------------------------

func TestBrowserListProfiles(t *testing.T) {
	ph := newProfileHarness(t, []string{"Work", "Personal"}, testLocalState)

	result := ph.callTool(t, "browser_list_profiles", nil)
	if result.IsError {
		t.Fatalf("browser_list_profiles error: %s", contentText(result))
	}

	text := contentText(result)
	// Should include Work and Personal but not Testing (not in allowed list).
	if !strings.Contains(text, "Work") {
		t.Errorf("expected 'Work' in output, got: %s", text)
	}
	if !strings.Contains(text, "Personal") {
		t.Errorf("expected 'Personal' in output, got: %s", text)
	}
	if strings.Contains(text, "Testing") {
		t.Errorf("'Testing' should not be in output (not allowed), got: %s", text)
	}
}

func TestBrowserListProfilesFiltersCorrectly(t *testing.T) {
	// Only allow one profile.
	ph := newProfileHarness(t, []string{"Personal"}, testLocalState)

	result := ph.callTool(t, "browser_list_profiles", nil)
	if result.IsError {
		t.Fatalf("browser_list_profiles error: %s", contentText(result))
	}

	text := contentText(result)
	if strings.Contains(text, "Work") {
		t.Errorf("'Work' should not be in output (not allowed), got: %s", text)
	}
	if !strings.Contains(text, "Personal") {
		t.Errorf("expected 'Personal' in output, got: %s", text)
	}
}

// ---------------------------------------------------------------------------
// browser_launch: profile error paths
// ---------------------------------------------------------------------------

func TestBrowserLaunchProfileNotAllowed(t *testing.T) {
	ph := newProfileHarness(t, []string{"Work"}, testLocalState)

	result := ph.callTool(t, "browser_launch", map[string]any{
		"profile": "NotAllowed",
	})
	if !result.IsError {
		t.Fatal("expected error for non-allowed profile")
	}
	text := contentText(result)
	if !strings.Contains(text, "not in the allowed profiles list") {
		t.Errorf("error = %q, want 'not in the allowed profiles list'", text)
	}
}

func TestBrowserLaunchProfileNotFound(t *testing.T) {
	// "Ghost" is in the allowed list but doesn't exist in Local State.
	ph := newProfileHarness(t, []string{"Ghost"}, testLocalState)

	result := ph.callTool(t, "browser_launch", map[string]any{
		"profile": "Ghost",
	})
	if !result.IsError {
		t.Fatal("expected error for profile not found in Local State")
	}
	text := contentText(result)
	if !strings.Contains(text, "not found") {
		t.Errorf("error = %q, want to contain 'not found'", text)
	}
}

func TestBrowserLaunchProfileNoAllowedSet(t *testing.T) {
	// When AllowedProfiles is empty, the profile tools are not registered.
	// browser_launch on the main harness (no AllowedProfiles) should reject
	// a profile parameter.
	result := callToolRaw(t, "browser_launch", map[string]any{
		"profile": "Work",
	})
	if !result.IsError {
		t.Fatal("expected error when profile is set but AllowedProfiles is empty")
	}
	text := contentText(result)
	if !strings.Contains(text, "not in the allowed profiles list") {
		t.Errorf("error = %q, want 'not in the allowed profiles list'", text)
	}
}

// ---------------------------------------------------------------------------
// browser_list_profiles: not registered without AllowedProfiles
// ---------------------------------------------------------------------------

func TestBrowserListProfilesNotRegisteredWithoutAllowedProfiles(t *testing.T) {
	// On the main harness (no AllowedProfiles), browser_list_profiles
	// should not exist.
	ctx, cancel := context.WithTimeout(harness.ctx, 5*time.Second)
	defer cancel()
	_, err := harness.session.CallTool(ctx, &mcp.CallToolParams{
		Name: "browser_list_profiles",
	})
	if err == nil {
		t.Fatal("expected error calling browser_list_profiles when AllowedProfiles is empty")
	}
}

// ---------------------------------------------------------------------------
// browser_list_profiles: bad Local State
// ---------------------------------------------------------------------------

func TestBrowserListProfilesBadLocalState(t *testing.T) {
	ph := newProfileHarness(t, []string{"Work"}, `not valid json`)

	result := ph.callTool(t, "browser_list_profiles", nil)
	if !result.IsError {
		t.Fatal("expected error with invalid Local State JSON")
	}
}
