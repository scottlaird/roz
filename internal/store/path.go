package store

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/adrg/xdg"
)

const (
	appDir      = "roz"
	dbFile      = "roz.db"
	envDataHome = "XDG_DATA_HOME"
)

// DefaultDBPath returns where the database lives when neither the --db flag
// nor ROZ_DB is set: <data home>/roz/roz.db.
//
// The data home is $XDG_DATA_HOME when it is set to an absolute path, and
// otherwise ~/.local/share on every Unix platform. macOS is included in that
// deliberately: it diverges from the platform's ~/Library/Application Support
// convention so the path is identical across a user's Unix machines.
//
// On Windows it resolves to Local, never Roaming, AppData. That is not a
// stylistic choice — a SQLite database keeps a WAL and a shared-memory file
// beside it, and a roaming profile will sync those between machines and
// corrupt the database.
//
// The directory is not created.
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	base := dataHome(runtime.GOOS, os.LookupEnv, xdg.DataHome, home)
	return filepath.Join(base, appDir, dbFile), nil
}

// dataHome resolves the base data directory, overriding xdg's macOS default so
// that macOS matches the other Unix platforms.
//
// xdgDataHome is the value xdg has already resolved for the platform, which
// accounts for an absolute XDG_DATA_HOME. xdg ignores a relative one, and so
// does this.
func dataHome(goos string, lookupEnv func(string) (string, bool), xdgDataHome, home string) string {
	if goos != "darwin" {
		return xdgDataHome
	}
	if v, ok := lookupEnv(envDataHome); ok && filepath.IsAbs(v) {
		return xdgDataHome
	}
	return filepath.Join(home, ".local", "share")
}
