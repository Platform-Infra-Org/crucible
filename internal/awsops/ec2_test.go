package awsops

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ec2For points a real client at an httptest server answering the EC2 query
// protocol: one XML body per Action, keyed by the Action form field.
func ec2For(t *testing.T, routes map[string]func() (int, string)) AWS {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		action := r.FormValue("Action")
		h, ok := routes[action]
		w.Header().Set("Content-Type", "text/xml")
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`<Response><Errors><Error><Code>InvalidAction</Code>` +
				`<Message>unrouted ` + action + `</Message></Error></Errors></Response>`))
			return
		}
		code, payload := h()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	a, err := NewAWS(context.Background(), AWSConfig{Region: "il-central-1", Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("NewAWS: %v", err)
	}
	return a
}

func TestFindInstancesReadsThePublicURLTag(t *testing.T) {
	a := ec2For(t, map[string]func() (int, string){
		"DescribeInstances": func() (int, string) {
			return 200, `<DescribeInstancesResponse><reservationSet><item><instancesSet>
				<item><instanceId>i-0abc</instanceId><instanceState><name>running</name></instanceState>
				<tagSet>
					<item><key>Name</key><value>crucible</value></item>
					<item><key>crucible:public-url</key><value>http://localhost:18080</value></item>
				</tagSet></item>
			</instancesSet></item></reservationSet></DescribeInstancesResponse>`
		},
	})
	got, err := a.FindInstances(context.Background(), "crucible", "running")
	if err != nil {
		t.Fatalf("FindInstances: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d instances, want 1", len(got))
	}
	if got[0].ID != "i-0abc" || got[0].State != "running" {
		t.Errorf("got %+v, want i-0abc running", got[0])
	}
	if got[0].PublicURL != "http://localhost:18080" {
		t.Errorf("PublicURL = %q, want the crucible:public-url tag", got[0].PublicURL)
	}
}

func TestFindInstancesLeavesPublicURLEmptyWhenUntagged(t *testing.T) {
	// Connect refuses a node with no crucible:public-url tag, so an absent tag
	// must read as empty rather than as the literal "None" the CLI printed.
	a := ec2For(t, map[string]func() (int, string){
		"DescribeInstances": func() (int, string) {
			return 200, `<DescribeInstancesResponse><reservationSet><item><instancesSet>
				<item><instanceId>i-0abc</instanceId><instanceState><name>running</name></instanceState>
				<tagSet><item><key>Name</key><value>crucible</value></item></tagSet></item>
			</instancesSet></item></reservationSet></DescribeInstancesResponse>`
		},
	})
	got, err := a.FindInstances(context.Background(), "crucible", "running")
	if err != nil {
		t.Fatalf("FindInstances: %v", err)
	}
	if len(got) != 1 || got[0].PublicURL != "" {
		t.Errorf("got %+v, want PublicURL empty", got)
	}
}

func TestInstanceStateLooksUpByID(t *testing.T) {
	// status, sleep, wake and teardown all ask for the node's state by id, not
	// by its Name tag, so this is its own lookup.
	a := ec2For(t, map[string]func() (int, string){
		"DescribeInstances": func() (int, string) {
			return 200, `<DescribeInstancesResponse><reservationSet><item><instancesSet>
				<item><instanceId>i-0abc</instanceId><instanceState><name>stopped</name></instanceState></item>
			</instancesSet></item></reservationSet></DescribeInstancesResponse>`
		},
	})
	st, err := a.InstanceState(context.Background(), "i-0abc")
	if err != nil {
		t.Fatalf("InstanceState: %v", err)
	}
	if st != "stopped" {
		t.Errorf("InstanceState = %q, want stopped", st)
	}
}

func TestInstanceStateErrorsWhenTheInstanceIsGone(t *testing.T) {
	a := ec2For(t, map[string]func() (int, string){
		"DescribeInstances": func() (int, string) {
			return 200, `<DescribeInstancesResponse><reservationSet/></DescribeInstancesResponse>`
		},
	})
	if _, err := a.InstanceState(context.Background(), "i-0gone"); err == nil {
		t.Fatal("InstanceState of a missing instance returned no error")
	}
}

func TestSubnetReportsItsVPCAndCIDR(t *testing.T) {
	a := ec2For(t, map[string]func() (int, string){
		"DescribeSubnets": func() (int, string) {
			return 200, `<DescribeSubnetsResponse><subnetSet><item>
				<subnetId>subnet-0abc</subnetId><vpcId>vpc-0def</vpcId><cidrBlock>10.0.1.0/24</cidrBlock>
			</item></subnetSet></DescribeSubnetsResponse>`
		},
	})
	vpc, cidr, err := a.Subnet(context.Background(), "subnet-0abc")
	if err != nil {
		t.Fatalf("Subnet: %v", err)
	}
	if vpc != "vpc-0def" || cidr != "10.0.1.0/24" {
		t.Errorf("Subnet = %q, %q, want vpc-0def, 10.0.1.0/24", vpc, cidr)
	}
}

func TestSubnetErrorsOnAnUnknownID(t *testing.T) {
	// `init --access ssm` validates the subnet before any apply, so a typo must
	// come back as an error rather than as an empty id.
	a := ec2For(t, map[string]func() (int, string){
		"DescribeSubnets": func() (int, string) {
			return 400, `<Response><Errors><Error><Code>InvalidSubnetID.NotFound</Code>` +
				`<Message>The subnet ID 'subnet-0bad' does not exist</Message></Error></Errors></Response>`
		},
	})
	if _, _, err := a.Subnet(context.Background(), "subnet-0bad"); err == nil {
		t.Fatal("Subnet of an unknown id returned no error")
	}
}

func TestWaitInstanceStopsOnContextCancel(t *testing.T) {
	a := ec2For(t, map[string]func() (int, string){
		"DescribeInstances": func() (int, string) {
			return 200, `<DescribeInstancesResponse><reservationSet><item><instancesSet>
				<item><instanceId>i-0abc</instanceId><instanceState><name>running</name></instanceState></item>
			</instancesSet></item></reservationSet></DescribeInstancesResponse>`
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := a.WaitInstance(ctx, "i-0abc", "stopped")
	if err == nil {
		t.Fatal("WaitInstance on a cancelled context returned no error")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("WaitInstance error = %v, want the context's cancellation", err)
	}
}

func TestSerialConsoleEnabledReadsTheAccountSetting(t *testing.T) {
	a := ec2For(t, map[string]func() (int, string){
		"GetSerialConsoleAccessStatus": func() (int, string) {
			return 200, `<GetSerialConsoleAccessStatusResponse><serialConsoleAccessEnabled>true` +
				`</serialConsoleAccessEnabled></GetSerialConsoleAccessStatusResponse>`
		},
	})
	on, err := a.SerialConsoleEnabled(context.Background())
	if err != nil {
		t.Fatalf("SerialConsoleEnabled: %v", err)
	}
	if !on {
		t.Error("SerialConsoleEnabled = false, want true")
	}
}

func TestStartAndStopInstance(t *testing.T) {
	var seen []string
	a := ec2For(t, map[string]func() (int, string){
		"StartInstances": func() (int, string) {
			seen = append(seen, "start")
			return 200, `<StartInstancesResponse/>`
		},
		"StopInstances": func() (int, string) {
			seen = append(seen, "stop")
			return 200, `<StopInstancesResponse/>`
		},
	})
	if err := a.StartInstance(context.Background(), "i-0abc"); err != nil {
		t.Fatalf("StartInstance: %v", err)
	}
	if err := a.StopInstance(context.Background(), "i-0abc"); err != nil {
		t.Fatalf("StopInstance: %v", err)
	}
	if len(seen) != 2 || seen[0] != "start" || seen[1] != "stop" {
		t.Errorf("calls = %v, want start then stop", seen)
	}
}
