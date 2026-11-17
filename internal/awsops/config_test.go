package awsops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "crucible.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimalConfig = `
install:
  name: crucible
  region: il-central-1
access:
  mode: ssm
  subnet_id: subnet-0abc
  local_port: 18080
server:
  bootstrap_admin: you@example.com
  ghcr_pull_token: ghp_tok
  git_credentials: https://bot:t@github.com
`

func TestLoadConfigReadsTheMinimalFile(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.Install.Region != "il-central-1" || c.Access.Mode != "ssm" || c.Access.LocalPort != 18080 {
		t.Errorf("got %+v, want the file's install and access values", c)
	}
	if c.Server.BootstrapAdmin != "you@example.com" {
		t.Errorf("BootstrapAdmin = %q", c.Server.BootstrapAdmin)
	}
}

func TestLoadConfigOfAMissingFileIsNotAnError(t *testing.T) {
	// An operator who passes every value as a flag has no file, and that is fine.
	c, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("a missing config must not be an error: %v", err)
	}
	if c.Install.Region != "" {
		t.Errorf("want a zero Config, got %+v", c)
	}
}

func TestLoadConfigRejectsAnUnknownKey(t *testing.T) {
	// A typo must be found, not ignored: a silently dropped budget is money.
	_, err := LoadConfig(writeConfig(t, minimalConfig+"\nlabz:\n  budget_usd: 200\n"))
	if err == nil || !strings.Contains(err.Error(), "labz") {
		t.Fatalf("an unknown key must be named in the error, got %v", err)
	}
}

func TestLoadConfigExpandsEnvReferences(t *testing.T) {
	t.Setenv("GHCR_TOKEN", "ghp_fromenv")
	c, err := LoadConfig(writeConfig(t, strings.Replace(minimalConfig,
		"ghcr_pull_token: ghp_tok", "ghcr_pull_token: ${GHCR_TOKEN}", 1)))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.Server.GHCRPullToken != "ghp_fromenv" {
		t.Errorf("GHCRPullToken = %q, want the environment's value", c.Server.GHCRPullToken)
	}
}

func TestLoadConfigRefusesAnUnsetEnvReference(t *testing.T) {
	t.Setenv("GHCR_TOKEN", "")
	_, err := LoadConfig(writeConfig(t, strings.Replace(minimalConfig,
		"ghcr_pull_token: ghp_tok", "ghcr_pull_token: ${GHCR_TOKEN}", 1)))
	if err == nil || !strings.Contains(err.Error(), "GHCR_TOKEN") {
		t.Fatalf("an unset variable must be an error naming it, not an empty token: %v", err)
	}
}

func TestLoadConfigRejectsBothAccessModes(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, strings.Replace(minimalConfig,
		"  subnet_id: subnet-0abc", "  subnet_id: subnet-0abc\n  domain: c.example.com", 1)))
	if err == nil || !strings.Contains(err.Error(), "domain") || !strings.Contains(err.Error(), "ssm") {
		t.Fatalf("ssm with a domain must be refused, naming both: %v", err)
	}
}

// Review Focus 3: a re-run must never silently remove what is deployed.
func TestTFVarsKeepsDeployedValuesForAbsentKeys(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, minimalConfig)) // no labs: block at all
	if err != nil {
		t.Fatal(err)
	}
	deployed := map[string]string{"lab_budget_usd": "200", "lab_regions": "il-central-1"}
	got, err := c.TFVars(deployed)
	if err != nil {
		t.Fatalf("TFVars: %v", err)
	}
	if got["lab_budget_usd"] != "200" {
		t.Errorf("lab_budget_usd = %q, want 200 kept: dropping a labs block must not destroy the AWS Budget", got["lab_budget_usd"])
	}
	if got["lab_regions"] != "il-central-1" {
		t.Errorf("lab_regions = %q, want the deployed value kept", got["lab_regions"])
	}
}

func TestTFVarsAbsentAccessKeepsTheDeployedMode(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, strings.Replace(minimalConfig,
		"access:\n  mode: ssm\n  subnet_id: subnet-0abc\n  local_port: 18080\n", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.TFVars(map[string]string{"access": "ssm", "local_port": "18080"})
	if err != nil {
		t.Fatalf("TFVars: %v", err)
	}
	if got["access"] != "ssm" {
		t.Errorf("access = %q, want ssm kept: it must never revert to the public default", got["access"])
	}
}

func TestTFVarsConfigBeatsDeployed(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, strings.Replace(minimalConfig,
		"  local_port: 18080", "  local_port: 19090", 1)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.TFVars(map[string]string{"local_port": "18080"})
	if err != nil {
		t.Fatalf("TFVars: %v", err)
	}
	if got["local_port"] != "19090" {
		t.Errorf("local_port = %q, want the config's 19090 to win over the deployed 18080", got["local_port"])
	}
}

func TestTFVarsCarriesTheServerSecrets(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.TFVars(nil)
	if err != nil {
		t.Fatalf("TFVars: %v", err)
	}
	for k, want := range map[string]string{
		"bootstrap_admin": "you@example.com",
		"ghcr_pull_token": "ghp_tok",
		"git_credentials": "https://bot:t@github.com",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

func TestTerraformVarsPassthroughReachesTFVars(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, minimalConfig+"\nterraform_vars:\n  k3s_version: v1.2.3\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.TFVars(nil)
	if err != nil {
		t.Fatalf("TFVars: %v", err)
	}
	if got["k3s_version"] != "v1.2.3" {
		t.Errorf("k3s_version = %q, want the passthrough value", got["k3s_version"])
	}
}

func TestTerraformVarsCannotShadowATypedKey(t *testing.T) {
	// Two sources for one variable is how an install stops matching itself.
	c, err := LoadConfig(writeConfig(t, minimalConfig+"\nterraform_vars:\n  bootstrap_admin: someone@else\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.TFVars(nil); err == nil || !strings.Contains(err.Error(), "bootstrap_admin") {
		t.Fatalf("a passthrough key the schema owns must be refused, naming it: %v", err)
	}
}

func TestAWSConfigComesFromTheAWSBlock(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, minimalConfig+
		"\naws:\n  sso_start_url: https://example/start\n  sso_region: il-central-1\n  account: \"111122223333\"\n  role: Admin\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := c.AWSConfig()
	if got.SSOStartURL != "https://example/start" || got.Account != "111122223333" || got.Role != "Admin" {
		t.Errorf("AWSConfig = %+v, want the aws block's values", got)
	}
	if got.Region != "il-central-1" {
		t.Errorf("Region = %q, want the install's region", got.Region)
	}
}

func TestAWSConfigIsEmptyWithoutTheBlock(t *testing.T) {
	// No aws block means "use ~/.aws/config", which the SDK does for free.
	c, err := LoadConfig(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.AWSConfig(); got.SSOStartURL != "" {
		t.Errorf("SSOStartURL = %q, want empty so the default chain is used", got.SSOStartURL)
	}
}

// The headline goal: an operator writes crucible.yaml, runs up on a machine
// with no checkout, and the file's values reach Terraform. Without this the
// documented sequence fails on a missing crucible.tfvars.
func TestUpGeneratesTheVarFileFromTheConfig(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	r := &recorder{output: fakeAWS}
	o := r.ops(t.TempDir())
	o.Home, o.Config = home, cfg
	if err := o.Up(context.Background(), "", true, true, DeployOpts{Version: "v1.4.0"}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	generated := filepath.Join(home, "crucible.tfvars")
	b, err := os.ReadFile(generated)
	if err != nil {
		t.Fatalf("up must generate %s from crucible.yaml: %v", generated, err)
	}
	body := string(b)
	for _, want := range []string{
		`bootstrap_admin = "you@example.com"`,
		`ghcr_pull_token = "ghp_tok"`,
		`git_credentials = "https://bot:t@github.com"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the generated tfvars lacks %s:\n%s", want, body)
		}
	}
	if !strings.Contains(strings.Join(r.calls, "\n"), "-var-file="+generated) {
		t.Errorf("the apply must use the generated file:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestUpPrefersAnExplicitVarFile(t *testing.T) {
	// A developer's --var-file still wins over the config file.
	cfg, err := LoadConfig(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(t.TempDir(), "mine.tfvars")
	if err := os.WriteFile(own, []byte("bootstrap_admin = \"dev@example.com\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recorder{output: fakeAWS}
	o := r.ops(t.TempDir())
	o.Home, o.Config = t.TempDir(), cfg
	if err := o.Up(context.Background(), own, true, true, DeployOpts{Version: "v1.4.0"}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !strings.Contains(strings.Join(r.calls, "\n"), "-var-file="+own) {
		t.Errorf("--var-file must win:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestUpWithNoConfigAndNoVarFileSaysWhatIsMissing(t *testing.T) {
	r := &recorder{output: fakeAWS}
	o := r.ops(t.TempDir())
	o.Home = t.TempDir()
	err := o.Up(context.Background(), "", true, true, DeployOpts{Version: "v1.4.0"})
	if err == nil {
		t.Fatal("with neither a config nor a var file, up must say so rather than hand terraform a missing path")
	}
	if !strings.Contains(err.Error(), "crucible.yaml") {
		t.Errorf("the error must name crucible.yaml: %v", err)
	}
}

func TestFillInitPutsTheFileBehindTheFlags(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	// Nothing given: every value comes from the file.
	region, domain, access, subnet := "", "", "", ""
	port := 0
	c.FillInit(&region, &domain, &access, &subnet, &port)
	if region != "il-central-1" || access != "ssm" || subnet != "subnet-0abc" || port != 18080 {
		t.Errorf("got %q/%q/%q/%d, want the file's values", region, access, subnet, port)
	}
	// A flag wins.
	region, access = "eu-west-1", "public"
	c.FillInit(&region, &domain, &access, &subnet, &port)
	if region != "eu-west-1" || access != "public" {
		t.Errorf("a flag must win: got %q/%q", region, access)
	}
}

func TestFillInitOnANilConfigChangesNothing(t *testing.T) {
	var c *Config
	region := "eu-west-1"
	port := 0
	empty := ""
	c.FillInit(&region, &empty, &empty, &empty, &port)
	if region != "eu-west-1" || port != 0 {
		t.Errorf("a nil config must fill nothing: %q/%d", region, port)
	}
}

// crucible.yaml's lab switches must reach Terraform under the variable names
// labSwitches uses — underscores, as strings. Keyed any other way the file is
// read, parsed, validated and then silently ignored, which is the worst of the
// three outcomes because nothing says so.
func TestFillLabsInitPutsSwitchesUnderTheirTerraformNames(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, minimalConfig+
		"\nlabs:\n  regions: [il-central-1]\n  billing_export: false\n  account_public_access_block: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	region, vpc := "", ""
	var regions, subnets, emails []string
	budget := 0.0
	settings := map[string]string{}
	c.FillLabsInit(&region, &regions, &vpc, &subnets, &budget, &emails, settings)

	for name, want := range map[string]string{"billing_export": "false", "account_public_access_block": "false"} {
		if got, ok := settings[name]; !ok || got != want {
			t.Errorf("settings[%q] = %q (present %v), want %q: the file would otherwise be ignored", name, got, ok, want)
		}
	}

	// A flag given on the command line wins: the file fills what was left out.
	given := map[string]string{"billing_export": "true"}
	c.FillLabsInit(&region, &regions, &vpc, &subnets, &budget, &emails, given)
	if given["billing_export"] != "true" {
		t.Errorf("crucible.yaml overrode a flag that was given: %q", given["billing_export"])
	}
}
