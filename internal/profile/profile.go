// Package profile discovers Chrome/Chromium user profiles from the
// browser's user data directory.
package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Profile holds the mapping between a Chrome profile's directory name
// (e.g. "Default", "Profile 2") and the user-chosen display name
// (e.g. "Work", "Personal").
type Profile struct {
	// Dir is the profile directory name relative to the user data dir.
	Dir string `json:"dir"`
	// Name is the user-visible display name.
	Name string `json:"name"`
}

// localState is the subset of Chrome's "Local State" JSON we care about.
type localState struct {
	Profile struct {
		InfoCache map[string]profileInfo `json:"info_cache"`
	} `json:"profile"`
}

type profileInfo struct {
	Name string `json:"name"`
}

// Discover reads the "Local State" file in the given user data directory
// and returns all profiles found in it.
func Discover(userDataDir string) ([]Profile, error) {
	data, err := os.ReadFile(filepath.Join(userDataDir, "Local State"))
	if err != nil {
		return nil, fmt.Errorf("read Local State: %w", err)
	}

	var state localState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse Local State: %w", err)
	}

	profiles := make([]Profile, 0, len(state.Profile.InfoCache))
	for dir, info := range state.Profile.InfoCache {
		name := info.Name
		if name == "" {
			name = dir
		}
		profiles = append(profiles, Profile{
			Dir:  dir,
			Name: name,
		})
	}
	return profiles, nil
}

// ResolveByName finds a profile by its display name from a list of profiles.
func ResolveByName(profiles []Profile, name string) (Profile, bool) {
	for _, p := range profiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// DefaultUserDataDir returns the platform-specific default user data
// directory for Chrome/Chromium. Returns an empty string if the
// directory does not exist.
func DefaultUserDataDir() string {
	candidates := defaultUserDataDirs()
	for _, dir := range candidates {
		if info, err := os.Stat(filepath.Join(dir, "Local State")); err == nil && !info.IsDir() {
			return dir
		}
	}
	return ""
}

// defaultUserDataDirs returns candidate user data directories in
// priority order for the current platform.
func defaultUserDataDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	switch runtime.GOOS {
	case "linux":
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if configDir == "" {
			configDir = filepath.Join(home, ".config")
		}
		return []string{
			filepath.Join(configDir, "google-chrome"),
			filepath.Join(configDir, "chromium"),
		}
	case "darwin":
		appSupport := filepath.Join(home, "Library", "Application Support")
		return []string{
			filepath.Join(appSupport, "Google", "Chrome"),
			filepath.Join(appSupport, "Chromium"),
		}
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		return []string{
			filepath.Join(localAppData, "Google", "Chrome", "User Data"),
			filepath.Join(localAppData, "Chromium", "User Data"),
		}
	default:
		return nil
	}
}
