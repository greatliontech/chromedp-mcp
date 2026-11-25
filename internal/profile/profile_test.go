package profile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	localState := `{
		"profile": {
			"info_cache": {
				"Default": {"name": "Work"},
				"Profile 2": {"name": "candosa"},
				"Profile 3": {"name": ""}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte(localState), 0o644); err != nil {
		t.Fatal(err)
	}

	profiles, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(profiles) != 3 {
		t.Fatalf("expected 3 profiles, got %d", len(profiles))
	}

	byDir := make(map[string]Profile)
	for _, p := range profiles {
		byDir[p.Dir] = p
	}

	if p := byDir["Default"]; p.Name != "Work" {
		t.Errorf("Default profile name = %q, want %q", p.Name, "Work")
	}
	if p := byDir["Profile 2"]; p.Name != "candosa" {
		t.Errorf("Profile 2 name = %q, want %q", p.Name, "candosa")
	}
	// Empty name falls back to dir name.
	if p := byDir["Profile 3"]; p.Name != "Profile 3" {
		t.Errorf("Profile 3 name = %q, want %q", p.Name, "Profile 3")
	}
}

func TestDiscover_NoLocalState(t *testing.T) {
	dir := t.TempDir()
	_, err := Discover(dir)
	if err == nil {
		t.Fatal("expected error when Local State is missing")
	}
}

func TestDiscover_EmptyDir(t *testing.T) {
	_, err := Discover("")
	if err == nil {
		t.Fatal("expected error when userDataDir is empty")
	}
}

func TestDiscover_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(dir); err == nil {
		t.Fatal("expected error for malformed Local State JSON")
	}
}

func TestDiscover_EmptyInfoCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte(`{"profile":{"info_cache":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	profiles, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(profiles) != 0 {
		t.Errorf("len(profiles) = %d, want 0", len(profiles))
	}
}

func TestDiscover_DeterministicOrderAndDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	localState := `{
		"profile": {
			"info_cache": {
				"Profile 3": {"name": "Shared"},
				"Profile 1": {"name": "Shared"},
				"Default": {"name": "Alpha"},
				"Profile 2": {"name": "Beta"}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte(localState), 0o644); err != nil {
		t.Fatal(err)
	}

	want := []Profile{
		{Dir: "Default", Name: "Alpha"},
		{Dir: "Profile 2", Name: "Beta"},
		{Dir: "Profile 1", Name: "Shared"},
		{Dir: "Profile 3", Name: "Shared"},
	}
	// Run repeatedly to catch Go map iteration randomization.
	for i := 0; i < 20; i++ {
		got, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("len = %d, want %d", len(got), len(want))
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("iteration %d index %d: got %+v, want %+v", i, j, got[j], want[j])
			}
		}
		p, ok := ResolveByName(got, "Shared")
		if !ok || p.Dir != "Profile 1" {
			t.Fatalf("ResolveByName(Shared) = (%+v, %v), want Dir=Profile 1", p, ok)
		}
	}
}

func TestDefaultUserDataDir_XDGConfigHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME lookup is Linux-specific")
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	// Neither google-chrome nor chromium exists yet.
	if got := DefaultUserDataDir(); got != "" {
		t.Errorf("DefaultUserDataDir() = %q, want empty when no Local State exists", got)
	}

	// Create chromium Local State; should be detected.
	chromiumDir := filepath.Join(xdg, "chromium")
	if err := os.MkdirAll(chromiumDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chromiumDir, "Local State"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DefaultUserDataDir(); got != chromiumDir {
		t.Errorf("DefaultUserDataDir() = %q, want %q", got, chromiumDir)
	}

	// google-chrome takes priority over chromium when both exist.
	chromeDir := filepath.Join(xdg, "google-chrome")
	if err := os.MkdirAll(chromeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chromeDir, "Local State"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DefaultUserDataDir(); got != chromeDir {
		t.Errorf("DefaultUserDataDir() = %q, want %q", got, chromeDir)
	}
}

func TestResolveByName(t *testing.T) {
	profiles := []Profile{
		{Dir: "Default", Name: "Work"},
		{Dir: "Profile 2", Name: "candosa"},
	}

	p, ok := ResolveByName(profiles, "Work")
	if !ok {
		t.Fatal("expected to find Work")
	}
	if p.Dir != "Default" {
		t.Errorf("Dir = %q, want %q", p.Dir, "Default")
	}

	p, ok = ResolveByName(profiles, "candosa")
	if !ok {
		t.Fatal("expected to find candosa")
	}
	if p.Dir != "Profile 2" {
		t.Errorf("Dir = %q, want %q", p.Dir, "Profile 2")
	}

	_, ok = ResolveByName(profiles, "nonexistent")
	if ok {
		t.Fatal("expected not to find nonexistent")
	}
}
