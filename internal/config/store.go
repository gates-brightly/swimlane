package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// StoreOff is the store_dir value that turns the store off.
const StoreOff = "off"

// StoreBase is where swim keeps what each round ran and wrote (the store
// package): store_dir when set, else $XDG_DATA_HOME/swim/store or
// ~/.local/share/swim/store. ok is false when store_dir is off.
func (c *Config) StoreBase() (dir string, ok bool, err error) {
	switch c.StoreDir {
	case StoreOff:
		return "", false, nil
	case "":
		if x := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(x) {
			return filepath.Join(x, "swim", "store"), true, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false, fmt.Errorf("no home directory for the store (set store_dir: %v)", err)
		}
		return filepath.Join(home, ".local", "share", "swim", "store"), true, nil
	}
	dir = expandHome(c.StoreDir)
	if !filepath.IsAbs(dir) {
		return "", false, fmt.Errorf("store_dir must be an absolute path or off, got %q", c.StoreDir)
	}
	return filepath.Clean(dir), true, nil
}
