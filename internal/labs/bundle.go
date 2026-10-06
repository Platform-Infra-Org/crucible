package labs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"crucible/internal/content"
)

// Files the trainee's environment must never see: instructions, checks, setups and hints are sent per call.
// quiz.yaml/module.yaml only matter if a lab sits in its module dir (the loader rejects that; this is defence in depth).
var bundleSkip = map[string]bool{"tasks": true, "checks": true, "setup": true, "hints": true, "lab.yaml": true,
	"quiz.yaml": true, "module.yaml": true}

// maxBundleBytes caps the gzipped bundle (it travels over the agent websocket); a var so tests can lower it.
var maxBundleBytes = 32 << 20

func tooBig(limit int) error {
	size := fmt.Sprintf("%d KiB", limit>>10)
	if limit >= 1<<20 {
		size = fmt.Sprintf("%d MiB", limit>>20)
	}
	return fmt.Errorf("lab bundle exceeds %s compressed; keep large files out of the lab directory (pull images instead)", size)
}

// maxModuleBundle keeps an aws lab's module under the 1 MiB ConfigMap limit (binaryData is base64 in etcd).
const maxModuleBundle = 700 << 10

// Bundle packs labDir for the trainee's environment. An aws lab's terraform/ is its module: it runs in the runner
// pod, never in the workspace, so it stays out; other runtimes give terraform/ no meaning and ship it.
func Bundle(labDir, runtime string) ([]byte, error) {
	return tarGz(labDir, func(top string) bool { return bundleSkip[top] || runtime == "aws" && top == "terraform" },
		nil, maxBundleBytes)
}

// BundleWith is an aws lab's workspace bundle (Bundle with runtime aws) plus generated files at its root (the
// workspace compose file).
func BundleWith(labDir string, extra map[string][]byte) ([]byte, error) {
	return tarGz(labDir, func(top string) bool { return bundleSkip[top] || top == "terraform" }, extra, maxBundleBytes)
}

// ModuleBundle is an aws lab's terraform/ directory plus Crucible's provider file and this lab's variables, all at
// the root module (/w after unpacking, where tfScript's -var-file points).
func ModuleBundle(lab *content.Lab, inst *Instance) ([]byte, error) {
	return tarGz(filepath.Join(lab.Dir, "terraform"), func(string) bool { return false }, map[string][]byte{
		content.LabTFFile:     []byte(content.LabTF),
		content.LabTFVarsFile: content.LabTFVars(inst.ID, inst.Team, inst.Training, lab.AWS.Region),
	}, maxModuleBundle)
}

// tarGz packs dir (minus top-level names skip reports, minus symlinks) and then the extra files, gzipped, up to
// limit bytes. An extra file replaces a file of the same name in dir.
func tarGz(dir string, skip func(top string) bool, extra map[string][]byte, limit int) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if buf.Len() > limit {
			return tooBig(limit)
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		if _, ok := extra[filepath.ToSlash(rel)]; ok {
			return nil
		}
		top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if skip(top) {
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
	for _, name := range slices.Sorted(maps.Keys(extra)) {
		b := extra[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg,
			ModTime: time.Unix(0, 0)}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(b); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > limit {
		return nil, tooBig(limit)
	}
	return buf.Bytes(), nil
}
