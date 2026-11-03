package awsops

import (
	"os"
	"strings"
	"testing"
)

// serialConsole must agree with the file the workspace image bakes in.
func TestSerialConsoleMatchesWorkspaceFile(t *testing.T) {
	b, err := os.ReadFile("../../deploy/workspace/serial-console-endpoints")
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 {
			lines[f[0]] = l
		}
	}
	for region, ep := range serialConsole {
		l, ok := lines[region]
		if !ok || !strings.Contains(" "+l+" ", " "+ep+" ") {
			t.Errorf("%s: endpoint %q not on that region's line of serial-console-endpoints (%q)", region, ep, l)
		}
	}
}
