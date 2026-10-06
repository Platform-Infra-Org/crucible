package labs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Files the trainee's environment must never see: instructions, checks, setups and hints are sent per call.
// quiz.yaml/module.yaml only matter if a lab sits in its module dir (the loader rejects that; this is defence in depth).
var bundleSkip = map[string]bool{"tasks": true, "checks": true, "setup": true, "hints": true, "lab.yaml": true,
	"quiz.yaml": true, "module.yaml": true}

// maxBundleBytes caps the gzipped bundle (it travels over the agent websocket); a var so tests can lower it.
var maxBundleBytes = 32 << 20

func tooBig() error {
	return fmt.Errorf("lab bundle exceeds %d MiB compressed; keep large files out of the lab directory (pull images instead)", maxBundleBytes>>20)
}

// Bundle packs labDir for the trainee's environment. An aws lab's terraform/ is its module: it runs in the runner
// pod, never in the workspace, so it stays out; other runtimes give terraform/ no meaning and ship it.
func Bundle(labDir, runtime string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(labDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if buf.Len() > maxBundleBytes {
			return tooBig()
		}
		rel, _ := filepath.Rel(labDir, p)
		if rel == "." {
			return nil
		}
		top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if bundleSkip[top] || runtime == "aws" && top == "terraform" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > maxBundleBytes {
		return nil, tooBig()
	}
	return buf.Bytes(), nil
}
