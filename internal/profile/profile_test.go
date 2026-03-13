package profile

import (
	"os"
	"path/filepath"
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
