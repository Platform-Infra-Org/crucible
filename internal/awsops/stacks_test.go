package awsops

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crucible/deploy"
)

func TestMaterialiseWritesTheEmbeddedFiles(t *testing.T) {
	home := t.TempDir()
	r := &recorder{output: fakeAWS}
	o := r.ops(t.TempDir())
	o.Home = home
	if err := o.materialise("main"); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	for _, name := range []string{"main.tf", "variables.tf", ".terraform.lock.hcl"} {
		p := filepath.Join(home, "deploy", "aws", "main", name)
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want, err := os.ReadFile(filepath.Join("..", "..", "deploy", "aws", "main", name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from deploy/aws/main/%s", p, name)
		}
	}
}

// The provider lock pins versions and their checksums; without it a fresh
// terraform init could resolve a provider the release was never tested with.
func TestMaterialiseShipsTheProviderLock(t *testing.T) {
	home := t.TempDir()
	r := &recorder{output: fakeAWS}
	o := r.ops(t.TempDir())
	o.Home = home
	for _, stack := range []string{"persistent", "main", "labs"} {
		if err := o.materialise(stack); err != nil {
			t.Fatalf("materialise %s: %v", stack, err)
		}
		if _, err := os.Stat(filepath.Join(home, "deploy", "aws", stack, ".terraform.lock.hcl")); err != nil {
			t.Errorf("%s has no provider lock: %v", stack, err)
		}
	}
}

func TestMaterialiseAdoptsStateFromACheckout(t *testing.T) {
	// The co-worker's laptop holds the live install's persistent state in a
	// checkout. Moving to the application directory must adopt it, never start
	// a second install beside it.
	home, root := t.TempDir(), t.TempDir()
	checkout := filepath.Join(root, "deploy", "aws", "persistent")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "terraform.tfstate"), []byte(`{"serial":7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recorder{output: fakeAWS}
	o := r.ops(root)
	o.Home = home
	if err := o.materialise("persistent"); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, "deploy", "aws", "persistent", "terraform.tfstate"))
	if err != nil {
		t.Fatalf("state was not adopted: %v", err)
	}
	if string(got) != `{"serial":7}` {
		t.Errorf("adopted state = %q, want the checkout's", got)
	}
	if !strings.Contains(r.log.String(), "adopted") {
		t.Errorf("the adoption must be said out loud: %q", r.log.String())
	}
}

func TestMaterialiseNeverOverwritesAdoptedState(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	checkout := filepath.Join(root, "deploy", "aws", "persistent")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "terraform.tfstate"), []byte(`{"serial":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recorder{output: fakeAWS}
	o := r.ops(root)
	o.Home = home
	if err := o.materialise("persistent"); err != nil {
		t.Fatal(err)
	}
	// An apply has moved the state on since.
	live := filepath.Join(home, "deploy", "aws", "persistent", "terraform.tfstate")
	if err := os.WriteFile(live, []byte(`{"serial":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := o.materialise("persistent"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(live)
	if string(got) != `{"serial":9}` {
		t.Fatalf("state = %q: a second run must not copy the stale checkout state over it", got)
	}
}

func TestMaterialiseLeavesTheFilesForHandImport(t *testing.T) {
	// The documented recovery runs terraform import fourteen times by hand, so
	// the directory has to stay where `crucible aws where` says it is.
	home := t.TempDir()
	r := &recorder{output: fakeAWS}
	o := r.ops(t.TempDir())
	o.Home = home
	if err := o.materialise("persistent"); err != nil {
		t.Fatal(err)
	}
	if o.dir("persistent") != filepath.Join(home, "deploy", "aws", "persistent") {
		t.Errorf("dir = %q, want it under the application directory", o.dir("persistent"))
	}
	if _, err := os.Stat(o.dir("persistent")); err != nil {
		t.Errorf("the stack directory must survive the call: %v", err)
	}
}

func TestDirPrefersACheckout(t *testing.T) {
	// A developer working in the repository keeps using deploy/aws, unchanged.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "aws", "main"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "deploy", "aws", "main", "main.tf"), []byte("# dev"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recorder{output: fakeAWS}
	o := r.ops(root)
	o.Home = t.TempDir()
	if got := o.dir("main"); got != filepath.Join(root, "deploy", "aws", "main") {
		t.Errorf("dir = %q, want the checkout's", got)
	}
}

// Review Focus 2: an empty application directory and a backup that cannot be
// read must stop, never apply. An apply against empty state is how a second
// persistent stack with duplicate Cognito pools and buckets gets created.
func TestInitRefusesWhenTheBackupReadIsDenied(t *testing.T) {
	r := &recorder{output: fakeAWS, outErr: func(cmd string) error {
		if strings.Contains(cmd, "head-object") {
			return errors.New("An error occurred (403) when calling the HeadObject operation: Forbidden")
		}
		return nil
	}}
	err := r.ops(t.TempDir()).Init(context.Background(), InitOpts{Region: "eu-west-1", Domain: "c.example.com"})
	if err == nil {
		t.Fatal("a denied backup read must stop init")
	}
	if !strings.Contains(err.Error(), "can't tell whether") {
		t.Errorf("the error must say it cannot tell, not that there is no backup: %v", err)
	}
	if indexOf(r.calls, "apply") >= 0 {
		t.Fatalf("nothing may be applied:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestInitProceedsWhenTheBackupIsAbsent(t *testing.T) {
	// A genuinely new install: 404 means there is nothing to adopt.
	r := &recorder{output: fakeAWS, outErr: func(cmd string) error {
		if strings.Contains(cmd, "head-object") {
			return errors.New("An error occurred (404) when calling the HeadObject operation: Not Found")
		}
		return nil
	}}
	if err := r.ops(t.TempDir()).Init(context.Background(), InitOpts{Region: "eu-west-1", Domain: "c.example.com"}); err != nil {
		t.Fatalf("a new install must be allowed: %v", err)
	}
	if indexOf(r.calls, "persistent apply") < 0 {
		t.Fatalf("the apply must happen:\n%s", strings.Join(r.calls, "\n"))
	}
}

// The build tree's gitignored files must never reach the binary: deploy/aws
// holds terraform.tfstate, .terraform/ and crucible.tfvars on any machine that
// has run `crucible aws init`, and shipping them would publish the secret key
// and the GHCR token — and, worse, materialise would write that snapshot over
// the operator's live state.
func TestEmbeddedStacksCarryNoStateOrSecrets(t *testing.T) {
	var bad []string
	err := fs.WalkDir(deploy.Stacks, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		switch {
		case d.IsDir() && name == ".terraform":
			bad = append(bad, p)
		case d.IsDir():
		case strings.HasPrefix(name, "terraform.tfstate"),
			strings.HasSuffix(name, ".tfvars"),
			strings.Contains(p, "/.terraform/"):
			bad = append(bad, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("the binary carries files it must not: %v", bad)
	}
	// And the files it does need are all there.
	for _, want := range []string{
		"aws/main/main.tf", "aws/main/variables.tf", "aws/main/.terraform.lock.hcl",
		"aws/persistent/.terraform.lock.hcl", "aws/labs/.terraform.lock.hcl",
	} {
		if _, err := fs.Stat(deploy.Stacks, want); err != nil {
			t.Errorf("%s is missing from the binary: %v", want, err)
		}
	}
}

func TestMaterialiseWritesOnlyStackFiles(t *testing.T) {
	home := t.TempDir()
	r := &recorder{output: fakeAWS}
	o := r.ops(t.TempDir())
	o.Home = home
	dst := filepath.Join(home, "deploy", "aws", "main")
	if err := os.MkdirAll(filepath.Join(dst, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The operator's live state, and a file a later release renamed away.
	live := filepath.Join(dst, "terraform.tfstate")
	if err := os.WriteFile(live, []byte(`{"serial":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dst, "retired.tf")
	if err := os.WriteFile(orphan, []byte("# removed in a later release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := o.materialise("main"); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	got, err := os.ReadFile(live)
	if err != nil || string(got) != `{"serial":42}` {
		t.Fatalf("live state = %q (%v): materialise must never write over it", got, err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".terraform")); err != nil {
		t.Errorf(".terraform/ must be left alone: %v", err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a stack file no longer in the binary must be pruned, or terraform reads both: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "main.tf")); err != nil {
		t.Errorf("main.tf was not written: %v", err)
	}
}

// connect used to run `aws ssm start-session`. It no longer does: the session is
// opened through the SDK and handed to the embedded plugin, so a machine with no
// AWS CLI at all reaches the app.
func TestConnectDoesNotNeedTheAWSCLI(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080")}
	o := r.ops(t.TempDir())
	o.LookPath = func(file string) (string, error) {
		if file == "aws" {
			return "", errors.New("executable file not found in $PATH")
		}
		return "/usr/local/bin/" + file, nil
	}
	if err := o.Connect(context.Background(), ConnectOpts{Region: "il-central-1"}); err != nil {
		t.Fatalf("connect must work with no AWS CLI installed: %v", err)
	}
}
