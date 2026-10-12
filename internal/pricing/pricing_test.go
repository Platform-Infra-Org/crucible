package pricing

import (
	"math"
	"os"
	"strings"
	"testing"
)

type table map[string]float64

func (t table) Price(service, region, key string) (float64, bool) {
	v, ok := t[service+"|"+region+"|"+key]
	return v, ok
}

func prices() table {
	r := "aws|eu-west-1|"
	return table{r + "ec2:t3.micro": 0.0114, r + "ebs:gp3": 0.088, r + "nat:hour": 0.048, r + "nat:gb": 0.048,
		r + "ipv4:hour": 0.005, r + "elb:application": 0.0252, r + "s3:standard": 0.023}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func load(t *testing.T, name string) []byte {
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPricesAPlan(t *testing.T) {
	res, err := FromPlan(load(t, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	usage := map[string]map[string]float64{"aws_s3_bucket.data": {"storage_gb": 100}, "module.net.aws_nat_gateway.this": {"gb_processed": 10}}
	b, err := PriceAWS(res, "eu-west-1", nil, usage, prices())
	if err != nil {
		t.Fatal(err)
	}
	want := 0.0114 + 10*0.088/730 + // instance + its 10 GiB root volume
		20*0.088/730 + // the data volume
		0.048 + 0.005 + 10*0.048/730 + // NAT hour, its public IPv4, 10 GB processed
		0.005 + // the EIP
		0.0252 + // the ALB hour
		100*0.023/730 // 100 GB in S3
	if !near(b.HourlyUSD, want) {
		t.Fatalf("hourly %v, want %v\n%+v", b.HourlyUSD, want, b.Lines)
	}
	for _, l := range b.Lines {
		if l.Address == "aws_instance.old" {
			t.Fatalf("a no-op resource must be ignored: %+v", l)
		}
		if strings.HasPrefix(l.Address, "aws_security_group") || strings.HasPrefix(l.Address, "random_id") {
			if l.HourlyUSD != 0 || l.Note != "free" {
				t.Fatalf("free types cost nothing: %+v", l)
			}
		}
	}
}

func TestUsageMatchesIndexedAndModuleAddresses(t *testing.T) {
	res := []Resource{{Address: "module.net.aws_nat_gateway.this[0]", Type: "aws_nat_gateway", Values: map[string]any{"connectivity_type": "public"}},
		{Address: "module.net.aws_nat_gateway.this[1]", Type: "aws_nat_gateway", Values: map[string]any{"connectivity_type": "public"}}}
	for _, key := range []string{"module.net.aws_nat_gateway.this", "module.net.aws_nat_gateway.this[0]"} {
		b, err := PriceAWS(res, "eu-west-1", nil, map[string]map[string]float64{key: {"gb_processed": 730}}, prices())
		if err != nil {
			t.Fatal(err)
		}
		var usage float64
		for _, l := range b.Lines {
			usage += l.UsageHourlyUSD
		}
		want := 0.048 // 730 GB over 730 h at 0.048 for one instance
		if key == "module.net.aws_nat_gateway.this" {
			want *= 2 // without an index the usage applies to each instance
		}
		if !near(usage, want) {
			t.Fatalf("%s: usage %v want %v", key, usage, want)
		}
	}
}

func TestUnpricedAndDeclared(t *testing.T) {
	res := []Resource{{Address: "aws_kinesis_stream.s", Type: "aws_kinesis_stream"}}
	if _, err := PriceAWS(res, "eu-west-1", nil, nil, prices()); err == nil || !strings.Contains(err.Error(), "aws_kinesis_stream") {
		t.Fatalf("unpriced must fail: %v", err)
	}
	b, err := PriceAWS(res, "eu-west-1", map[string]float64{"aws_kinesis_stream": 0.015}, nil, prices())
	if err != nil || !near(b.HourlyUSD, 0.015) || b.Lines[0].Note != "declared" {
		t.Fatalf("declared: %+v %v", b, err)
	}
	if _, err := PriceAWS([]Resource{{Address: "aws_instance.x", Type: "aws_instance", Values: map[string]any{"instance_type": "x9.huge"}}}, "eu-west-1", nil, nil, prices()); err == nil {
		t.Fatal("an instance type with no price must fail, never $0")
	}
	if !KnownAWS("aws_instance") || !KnownAWS("aws_route_table") || KnownAWS("aws_kinesis_stream") {
		t.Fatal("KnownAWS")
	}
}

func TestFromStateReadsManagedInstances(t *testing.T) {
	res, err := FromState(load(t, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Address != "aws_instance.web" || res[1].Address != "module.net.aws_nat_gateway.this[0]" {
		t.Fatalf("%+v", res)
	}
}
