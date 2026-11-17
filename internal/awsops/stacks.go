package awsops

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"crucible/deploy"
)

// stackFiles is the embedded deploy/aws, carried inside the binary so that an
// operator needs no checkout of this repository.
const embeddedRoot = "aws"

// home is where the stacks are written and where the persistent stack's state
// lives: Ops.Home when set, else the application directory.
//
// It is not a temporary directory on purpose. The persistent stack keeps its
// state in a local file — it must, because on the first init the bucket that
// state would live in does not exist yet — so the directory has to survive.
// That makes it the one thing on an operator's machine worth backing up.
func (o Ops) home() (string, error) {
	if o.Home != "" {
		return o.Home, nil
	}
	return AppDir()
}

// dir is where Terraform runs for a stack. A checkout wins, so a developer
// working in the repository keeps using deploy/aws exactly as before;
// otherwise it is the materialised copy under the application directory.
func (o Ops) dir(stack string) string {
	if o.Root != "" {
		d := filepath.Join(o.Root, "deploy", "aws", stack)
		if _, err := os.Stat(filepath.Join(d, "main.tf")); err == nil {
			return d
		}
	}
	h, err := o.home()
	if err != nil {
		return filepath.Join(o.Root, "deploy", "aws", stack) // nothing better to say
	}
	return filepath.Join(h, "deploy", "aws", stack)
}

// materialise writes a stack's files out of the binary and adopts any state a
// checkout already holds. It is called by the four commands that run Terraform.
//
// The .tf files and the provider lock are rewritten every time, so upgrading
// Crucible upgrades the stacks. terraform.tfstate and .terraform/ are never
// touched, because they are the only link to the real resources.
func (o Ops) materialise(stack string) error {
	dst := o.dir(stack)
	if o.Root != "" && dst == filepath.Join(o.Root, "deploy", "aws", stack) {
		return nil // a developer's checkout: the files are already the real ones
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	src := filepath.Join(embeddedRoot, stack)
	entries, err := fs.ReadDir(deploy.Stacks, src)
	if err != nil {
		return fmt.Errorf("no embedded stack %q: %w", stack, err)
	}
	// Prune first: a release that renames or drops a .tf file would otherwise
	// leave the old one behind, and Terraform reads every .tf in the directory —
	// so the next apply would either re-create what the release removed or fail
	// on duplicate declarations, with nothing on the operator's disk to explain
	// why. Only stack files are pruned; state and .terraform/ are never touched.
	shipped := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			shipped[e.Name()] = true
		}
	}
	existing, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e.IsDir() || shipped[e.Name()] || !isStackFile(e.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Belt and braces: the embed patterns already exclude state and
		// secrets, and this makes it impossible to write one even if they
		// change.
		if !isStackFile(e.Name()) {
			continue
		}
		b, err := fs.ReadFile(deploy.Stacks, filepath.ToSlash(filepath.Join(src, e.Name())))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			return err
		}
	}
	return o.adoptState(stack, dst)
}

// adoptState copies a checkout's terraform.tfstate into the application
// directory the first time, so an operator who already has an install keeps it
// instead of creating a second one. State already here is never replaced: by
// then it is newer than the checkout's.
func (o Ops) adoptState(stack, dst string) error {
	if o.Root == "" {
		return nil
	}
	live := filepath.Join(dst, "terraform.tfstate")
	if _, err := os.Stat(live); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	old := filepath.Join(o.Root, "deploy", "aws", stack, "terraform.tfstate")
	b, err := os.ReadFile(old)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.WriteFile(live, b, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(o.Log, "adopted the %s stack's state from %s into %s\n", stack, old, live)
	return nil
}

// Where prints the paths an operator needs when something has to be done by
// hand — above all the documented recovery that runs terraform import fourteen
// times against the persistent stack.
func (o Ops) Where() error {
	cfg, err := ConfigPath()
	if err != nil {
		return err
	}
	h, err := o.home()
	if err != nil {
		return err
	}
	fmt.Fprintf(o.Log, "config:      %s\n", cfg)
	fmt.Fprintf(o.Log, "application: %s   (back this up: it holds the persistent stack's state)\n", h)
	for _, stack := range []string{"persistent", "main", "labs"} {
		fmt.Fprintf(o.Log, "%-12s %s\n", stack+":", o.dir(stack))
	}
	look := o.LookPath
	if look == nil {
		look = osLookPath
	}
	tf, err := look("terraform")
	if err != nil {
		tf = "not on your PATH (only init, labs-init, up and teardown need it)"
	}
	fmt.Fprintf(o.Log, "terraform:   %s\n", tf)
	return nil
}

// osLookPath is exec.LookPath, named so stacks.go needs no os/exec import name
// collision with the Ops seam.
var osLookPath = func(file string) (string, error) { return execLookPath(file) }

// materialiseAll writes every stack out and adopts any state a checkout holds.
// The four Terraform commands call it: each reads more than one stack's outputs,
// so all three have to be on disk before any of them runs.
func (o Ops) materialiseAll() error {
	for _, stack := range []string{"persistent", "main", "labs"} {
		if err := o.materialise(stack); err != nil {
			return err
		}
	}
	return nil
}

// isStackFile is what a stack is made of, and so what materialise may write and
// prune: the Terraform sources, the templates they render and the provider lock.
// terraform.tfstate, .terraform/ and any .tfvars are deliberately not included —
// state is the only link to the real resources, and a .tfvars holds tokens.
func isStackFile(name string) bool {
	switch {
	case name == ".terraform.lock.hcl":
		return true
	case strings.HasSuffix(name, ".tfvars"), strings.HasSuffix(name, ".tfvars.example"):
		return false
	case strings.HasPrefix(name, "terraform.tfstate"):
		return false
	case strings.HasSuffix(name, ".tf"), strings.HasSuffix(name, ".tftpl"):
		return true
	}
	return false
}
