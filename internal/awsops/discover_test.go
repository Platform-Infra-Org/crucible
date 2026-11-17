package awsops

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// "Is it up?" is answerable from the node's tags alone — that is how connect
// finds it. status must not refuse the question because /<name>/install is
// missing: an install made before that parameter existed, or by a colleague
// running an older Crucible, is exactly the case an operator holding only this
// binary cannot get out of.
func TestStatusAnswersWithoutTheInstallParameter(t *testing.T) {
	r := &recorder{
		region: "il-central-1",
		output: fakeNode("i-07db1bbc311efcef7\thttp://localhost:18080"),
		outErr: func(cmd string) error {
			if strings.Contains(cmd, "get-parameter") && strings.Contains(cmd, "/crucible/install") {
				return errors.New("ParameterNotFound")
			}
			return nil
		},
	}
	o := r.ops(t.TempDir())
	if err := o.Status(context.Background()); err != nil {
		t.Fatalf("status must answer from the node's tags when the parameter is absent: %v", err)
	}
	log := r.log.String()
	for _, want := range []string{"i-07db1bbc311efcef7", "http://localhost:18080"} {
		if !strings.Contains(log, want) {
			t.Errorf("status must report %s:\n%s", want, log)
		}
	}
	// And it must say why it knows less than usual, so nobody reads a thinner
	// report as a healthy one.
	if !strings.Contains(log, "crucible aws up") {
		t.Errorf("status must say the install's details are not in SSM yet:\n%s", log)
	}
}

// With no parameter and no node, there is nothing to report and saying so
// plainly beats an SSM error about a parameter.
func TestStatusSaysWhenThereIsNoInstallAtAll(t *testing.T) {
	r := &recorder{
		region: "il-central-1",
		output: func(cmd string) string {
			if strings.Contains(cmd, "describe-instances") {
				return ""
			}
			return fakeAWS(cmd)
		},
		outErr: func(cmd string) error {
			if strings.Contains(cmd, "get-parameter") && strings.Contains(cmd, "/crucible/install") {
				return errors.New("ParameterNotFound")
			}
			return nil
		},
	}
	err := r.ops(t.TempDir()).Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no install") {
		t.Fatalf("no parameter and no node must say there is no install: %v", err)
	}
}
