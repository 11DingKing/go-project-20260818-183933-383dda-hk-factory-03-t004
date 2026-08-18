package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// mkdirAll creates the directory and parents, accepting a permissions mode.
func ensureDataDir(dir string) error {
	if err := os.MkdirAll(filepath.Clean(dir), 0o755); err != nil {
		return fmt.Errorf("store: create data dir %s: %w", dir, err)
	}
	return nil
}
