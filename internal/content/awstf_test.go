package content

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Every terraform pod of every lab gets the same aws provider build (apply and destroy alike), and every resource,
// root volumes included, the crucible:* tags. The pin sits in the provider block: a second required_providers block
// would clash with the module's own ("Duplicate required providers configuration"); terraform intersects the two.
func TestLabTFPinsTheProviderAndTagsEverything(t *testing.T) {
	if strings.Contains(LabTF, "required_providers") {
		t.Fatal("crucible.tf must not declare required_providers: modules may")
	}
	for _, want := range []string{`version = "6.67.0"`, `"crucible:lab-id"   = var.crucible_lab_id`,
		`"crucible:team"     = var.crucible_team`, `"crucible:training" = var.crucible_training`, "default_tags {"} {
		if !strings.Contains(LabTF, want) {
			t.Fatalf("crucible.tf lacks %s:\n%s", want, LabTF)
		}
	}
}

func TestLabTFVarsInstanceProfileAndSSHSource(t *testing.T) {
	net := LabNetwork{Region: "il-central-1", VPCID: "vpc-1", SubnetIDs: []string{"subnet-a"}, InstanceProfile: "crucible-lab-instance", NodeIP: "10.2.129.17"}
	var v map[string]any
	if err := json.Unmarshal(LabTFVars("abc", "t", "tr", "il-central-1", net, true), &v); err != nil {
		t.Fatal(err)
	}
	if v["crucible_instance_profile"] != "crucible-lab-instance" || v["crucible_ssh_source_cidr"] != "10.2.129.17/32" {
		t.Fatalf("direct_ssh lab: %v", v)
	}
	if err := json.Unmarshal(LabTFVars("abc", "t", "tr", "il-central-1", net, false), &v); err != nil {
		t.Fatal(err)
	}
	if v["crucible_ssh_source_cidr"] != "" {
		t.Fatalf("no direct_ssh, no source CIDR: %v", v)
	}
	if err := json.Unmarshal(LabTFVars("abc", "t", "tr", "il-central-1", LabNetwork{}, true), &v); err != nil {
		t.Fatal(err)
	}
	if v["crucible_instance_profile"] != "" || v["crucible_ssh_source_cidr"] != "" {
		t.Fatalf("an older labs stack: empty strings, never null: %v", v)
	}
	for _, name := range []string{"crucible_instance_profile", "crucible_ssh_source_cidr"} {
		if !strings.Contains(LabTF, `variable "`+name+`" { type = string }`) {
			t.Errorf("LabTF must declare %s", name)
		}
	}
}

func TestDirectSSHParses(t *testing.T) {
	var c AWSConfig
	if err := yaml.Unmarshal([]byte("region: il-central-1\nmax_hourly_usd: 1\ndirect_ssh: true\n"), &c); err != nil || !c.DirectSSH {
		t.Fatalf("direct_ssh: %v %+v", err, c)
	}
}
