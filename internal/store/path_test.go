package store

import (
	"path/filepath"
	"testing"
)

func TestDataHome(t *testing.T) {
	const home = "/home/scott"

	// env builds a lookup func over a fixed set of variables.
	env := func(kv map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			v, ok := kv[k]
			return v, ok
		}
	}

	tests := []struct {
		name        string
		goos        string
		env         map[string]string
		xdgDataHome string
		want        string
	}{
		{
			name:        "linux uses xdg's value",
			goos:        "linux",
			xdgDataHome: filepath.Join(home, ".local", "share"),
			want:        filepath.Join(home, ".local", "share"),
		},
		{
			name:        "linux honours XDG_DATA_HOME via xdg",
			goos:        "linux",
			env:         map[string]string{envDataHome: "/data"},
			xdgDataHome: "/data",
			want:        "/data",
		},
		{
			name:        "windows uses xdg's value",
			goos:        "windows",
			xdgDataHome: `C:\Users\scott\AppData\Local`,
			want:        `C:\Users\scott\AppData\Local`,
		},
		{
			name:        "darwin overrides Application Support",
			goos:        "darwin",
			xdgDataHome: filepath.Join(home, "Library", "Application Support"),
			want:        filepath.Join(home, ".local", "share"),
		},
		{
			name:        "darwin honours an absolute XDG_DATA_HOME",
			goos:        "darwin",
			env:         map[string]string{envDataHome: "/data"},
			xdgDataHome: "/data",
			want:        "/data",
		},
		{
			name:        "darwin ignores a relative XDG_DATA_HOME",
			goos:        "darwin",
			env:         map[string]string{envDataHome: "data"},
			xdgDataHome: filepath.Join(home, "Library", "Application Support"),
			want:        filepath.Join(home, ".local", "share"),
		},
		{
			name:        "darwin ignores an empty XDG_DATA_HOME",
			goos:        "darwin",
			env:         map[string]string{envDataHome: ""},
			xdgDataHome: filepath.Join(home, "Library", "Application Support"),
			want:        filepath.Join(home, ".local", "share"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dataHome(tt.goos, env(tt.env), tt.xdgDataHome, home)
			if got != tt.want {
				t.Errorf("dataHome(%q) = %q, want %q", tt.goos, got, tt.want)
			}
		})
	}
}

// TestDefaultDBPath checks only the shape of the result. Which data home it
// picks is dataHome's job and is covered above; xdg resolves its side at
// package init, so it cannot be steered from here with t.Setenv.
func TestDefaultDBPath(t *testing.T) {
	got, err := DefaultDBPath()
	if err != nil {
		t.Fatalf("DefaultDBPath() returned error: %v", err)
	}

	if !filepath.IsAbs(got) {
		t.Errorf("DefaultDBPath() = %q, want an absolute path", got)
	}
	if want := filepath.Join(appDir, dbFile); lastTwoElements(got) != want {
		t.Errorf("DefaultDBPath() = %q, want a path ending in %q", got, want)
	}
}

func lastTwoElements(path string) string {
	dir, file := filepath.Split(path)
	return filepath.Join(filepath.Base(filepath.Clean(dir)), file)
}
