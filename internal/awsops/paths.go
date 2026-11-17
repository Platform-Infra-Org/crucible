package awsops

import (
	"os"
	"path/filepath"
)

// ConfigDir is where crucible.yaml lives: $CRUCIBLE_CONFIG_DIR, else the
// platform's user configuration directory plus "crucible".
//
// CRUCIBLE_CONFIG names the configuration *file* (see LoadConfig); this names
// the directory that holds it by default.
func ConfigDir() (string, error) {
	if d := os.Getenv("CRUCIBLE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	return defaultDir()
}

// AppDir is where the materialised Terraform stacks, the persistent stack's
// state and the cached install details live: $CRUCIBLE_HOME, else the same
// directory as the configuration.
//
// It is the one directory worth backing up on an operator's machine.
func AppDir() (string, error) {
	if d := os.Getenv("CRUCIBLE_HOME"); d != "" {
		return d, nil
	}
	return defaultDir()
}

func defaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "crucible"), nil
}
