package awsops

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// AWS publishes the Session Manager plugin only as a compiled binary: it is not
// importable (no module file, and its entry point reads os.Args by index and
// calls os.Exit), and the port-forward's data channel — framed messages
// multiplexed inside one WebSocket — is in no SDK. So a release binary carries
// AWS's own plugin, gzipped, one platform per build, and writes it out on first
// use. An operator installs nothing; see scripts/plugin-blobs.sh.
//
// Apache-2.0 asks that the licence and notice travel with the binary, so they
// are embedded too and land beside the extracted copy.

// sessionPlugin is the path to a session-manager-plugin this machine can run:
// the embedded copy, extracted into the application directory once, or one
// already on PATH when this build carries none.
func (o Ops) sessionPlugin() (string, error) {
	gz := o.Plugin
	if gz == nil {
		gz = pluginGz
	}
	if len(gz) == 0 {
		look := o.LookPath
		if look == nil {
			look = exec.LookPath
		}
		p, err := look("session-manager-plugin")
		if err != nil {
			return "", errors.New("this build carries no Session Manager plugin and session-manager-plugin is not installed: " +
				"a release binary of Crucible carries it (a build from source needs `scripts/plugin-blobs.sh`), or install it by hand — " +
				"https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html")
		}
		return p, nil
	}

	home, err := o.home()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "bin")
	sum := sha256.Sum256(gz)
	// The checksum names the file, so a Crucible that embeds a newer plugin
	// extracts beside the old one instead of trusting a stale cache.
	path := filepath.Join(dir, "session-manager-plugin-"+hex.EncodeToString(sum[:])[:12])
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := writeGz(path, gz); err != nil {
		return "", fmt.Errorf("unpacking the embedded session-manager-plugin: %w", err)
	}
	for name, body := range map[string][]byte{
		"session-manager-plugin.LICENSE": pluginLicence,
		"session-manager-plugin.NOTICE":  pluginNotice,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			return "", err
		}
	}
	return path, nil
}

// writeGz unpacks gz to path through a temporary file, so a failure leaves no
// half-written binary for the next run to exec.
func writeGz(path string, gz []byte) error {
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".plugin-*")
	if err != nil {
		return err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name()) // a no-op once the rename below has succeeded
	}()
	if _, err := io.Copy(tmp, r); err != nil {
		return err
	}
	if err := r.Close(); err != nil {
		return err
	}
	if err := tmp.Chmod(0o700); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
