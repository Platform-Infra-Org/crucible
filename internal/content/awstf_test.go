package content

import (
	"strings"
	"testing"
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
