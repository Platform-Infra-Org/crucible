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
	want := 0.0114 + 0.005 + 10*0.088/730 + // instance, its public IPv4 (flag unset), its 10 GiB root volume
		20*0.088/730 + // the data volume
		5*0.088/730 + // the replaced volume (delete+create still exists afterwards)
		0.048 + 10*0.048/730 + // NAT hour (its IP is the EIP below), 10 GB processed
		0.005 + // the EIP
		0.0252 + 2*0.005 + // the ALB hour, and an IPv4 per subnet (2 when unknown)
		100*0.023/730 // 100 GB in S3
	if !near(b.HourlyUSD, want) {
		t.Fatalf("hourly %v, want %v\n%+v", b.HourlyUSD, want, b.Lines)
	}
	for _, l := range b.Lines {
		if l.Address == "aws_instance.old" || l.Address == "aws_instance.gone" {
			t.Fatalf("no-op and delete-only resources must be ignored: %+v", l)
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
	if !KnownAWS("aws_instance") || !KnownAWS("aws_route_table") || !KnownAWS("aws_volume_attachment") || KnownAWS("aws_kinesis_stream") {
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

func priceOne(r Resource, declared map[string]float64) (Breakdown, error) {
	return PriceAWS([]Resource{r}, "eu-west-1", declared, nil, prices())
}

func TestFailClosedOnWhatItCannotPrice(t *testing.T) {
	vol := func(v map[string]any) Resource {
		return Resource{Address: "aws_ebs_volume.v", Type: "aws_ebs_volume", Values: v}
	}
	inst := func(v map[string]any) Resource {
		v["instance_type"] = "t3.micro"
		return Resource{Address: "aws_instance.i", Type: "aws_instance", Values: v}
	}
	bad := []Resource{
		vol(map[string]any{"type": "gp3"}),                                                     // size unknown
		vol(map[string]any{"type": "gp3", "size": nil}),                                        // size null
		vol(map[string]any{"type": "io2", "size": 10.0, "iops": 1000.0}),                       // provisioned IOPS
		vol(map[string]any{"type": "gp3", "size": 10.0, "iops": 6000.0}),                       // gp3 IOPS above 3000
		vol(map[string]any{"type": "gp3", "size": 10.0, "throughput": 250.0}),                  // gp3 throughput above 125
		inst(map[string]any{"root_block_device": []any{map[string]any{"volume_type": "io1"}}}), // root io1
		inst(map[string]any{"tenancy": "dedicated"}),
		inst(map[string]any{"host_id": "h-1"}),
	}
	for _, r := range bad {
		if _, err := priceOne(r, nil); err == nil {
			t.Fatalf("must fail closed: %+v", r)
		}
	}
	if _, err := priceOne(vol(map[string]any{"type": "gp3"}), nil); err == nil || !strings.Contains(err.Error(), "aws_ebs_volume.v") {
		t.Fatalf("error must name the address: %v", err)
	}
	if b, err := priceOne(vol(map[string]any{"type": "io2"}), map[string]float64{"aws_ebs_volume": 0.5}); err != nil || !near(b.HourlyUSD, 0.5) {
		t.Fatalf("declared wins: %+v %v", b, err)
	}
	if _, err := priceOne(vol(map[string]any{"type": "gp3", "size": 10.0, "iops": 3000.0, "throughput": 125.0}), nil); err != nil {
		t.Fatalf("gp3 baseline is included in the GB price: %v", err)
	}
}

func TestDefaultRootAndPublicIPs(t *testing.T) {
	b, err := priceOne(Resource{Address: "aws_instance.i", Type: "aws_instance", Values: map[string]any{"instance_type": "t3.micro", "associate_public_ip_address": true}}, nil)
	if err != nil || !near(b.HourlyUSD, 0.0114+8*0.088/730+0.005) {
		t.Fatalf("default 8 GiB gp3 root plus public IPv4: %+v %v", b, err)
	}
	b, err = priceOne(Resource{Address: "aws_lb.a", Type: "aws_lb", Values: map[string]any{"internal": true}}, nil)
	if err != nil || !near(b.HourlyUSD, 0.0252) || b.Lines[0].Note != "LCU usage not priced" {
		t.Fatalf("internal lb: %+v %v", b, err)
	}
	b, err = priceOne(Resource{Address: "aws_lb.a", Type: "aws_lb", Values: map[string]any{"subnets": []any{"a", "b", "c"}}}, nil)
	if err != nil || !near(b.HourlyUSD, 0.0252+3*0.005) {
		t.Fatalf("internet-facing lb: %+v %v", b, err)
	}
	b, err = priceOne(Resource{Address: "aws_nat_gateway.n", Type: "aws_nat_gateway", Values: map[string]any{"connectivity_type": "public"}}, nil)
	if err != nil || !near(b.HourlyUSD, 0.048+0.005) {
		t.Fatalf("nat without allocation_id counts its IP: %+v %v", b, err)
	}
}

func TestPlanNullValueFailsClosed(t *testing.T) {
	plan := `{"resource_changes":[{"address":"aws_ebs_volume.s","mode":"managed","type":"aws_ebs_volume","change":{"actions":["create"],"after":{"size":null,"snapshot_id":"snap-1","type":"gp3"}}}]}`
	res, err := FromPlan([]byte(plan))
	if err != nil || len(res) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := priceOne(res[0], nil); err == nil {
		t.Fatal("null size must fail")
	}
}

func TestRound2(t *testing.T) {
	res, err := FromPlan(load(t, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var nat Resource
	for _, r := range res {
		if r.Type == "aws_nat_gateway" {
			nat = r
		}
	}
	if nat.Unknown["allocation_id"] != true {
		t.Fatalf("after_unknown must be kept: %+v", nat)
	}
	if b, err := priceOne(nat, nil); err != nil || !near(b.HourlyUSD, 0.048) {
		t.Fatalf("unknown allocation_id is an EIP counted elsewhere: %+v %v", b, err)
	}
	inst := func(v map[string]any) Resource {
		return Resource{Address: "aws_instance.i", Type: "aws_instance", Values: v}
	}
	for _, v := range []any{nil, true} {
		b, err := priceOne(inst(map[string]any{"instance_type": "t3.micro", "associate_public_ip_address": v}), nil)
		if err != nil || !near(b.HourlyUSD, 0.0114+8*0.088/730+0.005) {
			t.Fatalf("public IP %v counts: %+v %v", v, b, err)
		}
	}
	if b, err := priceOne(inst(map[string]any{"instance_type": "t3.micro", "associate_public_ip_address": false}), nil); err != nil || !near(b.HourlyUSD, 0.0114+8*0.088/730) {
		t.Fatalf("explicit false: %+v %v", b, err)
	}
	b, err := priceOne(Resource{Address: "aws_lb.a", Type: "aws_lb", Values: map[string]any{"subnet_mapping": []any{1, 2, 3}}}, nil)
	if err != nil || !near(b.HourlyUSD, 0.0252+3*0.005) {
		t.Fatalf("subnet_mapping: %+v %v", b, err)
	}
	_, err = priceOne(Resource{Address: "aws_vpc.v", Type: "aws_vpc", Values: map[string]any{"instance_tenancy": "dedicated"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "aws_vpc.v") {
		t.Fatalf("dedicated vpc: %v", err)
	}
	_, err = priceOne(inst(map[string]any{"instance_type": nil}), nil)
	if err == nil || !strings.Contains(err.Error(), "instance_type unknown") {
		t.Fatalf("unknown instance_type: %v", err)
	}
	_, err = priceOne(inst(map[string]any{"instance_type": "t3.micro", "root_block_device": []any{map[string]any{"volume_type": "io1"}}}), nil)
	if err == nil || !strings.Contains(err.Error(), "declare aws_instance") {
		t.Fatalf("root message: %v", err)
	}
}

func TestDedicatedVPC(t *testing.T) {
	vpc := Resource{Address: "aws_vpc.v", Type: "aws_vpc", Values: map[string]any{"instance_tenancy": "dedicated"}}
	if _, err := priceOne(vpc, map[string]float64{"aws_instance": 0.5}); err != nil {
		t.Fatalf("declared aws_instance covers a dedicated VPC: %v", err)
	}
	if _, err := priceOne(vpc, map[string]float64{"aws_vpc": 0.1}); err == nil {
		t.Fatal("declaring aws_vpc must not bypass the dedicated check")
	}
	if _, err := priceOne(vpc, nil); err == nil || !strings.Contains(err.Error(), "aws_vpc.v") {
		t.Fatalf("dedicated VPC alone: %v", err)
	}
}

func TestDeclaredNeverLowersAListPrice(t *testing.T) {
	inst := Resource{Address: "aws_instance.i", Type: "aws_instance", Values: map[string]any{"instance_type": "t3.micro", "associate_public_ip_address": false}}
	own := 0.0114 + 8*0.088/730
	if b, err := priceOne(inst, map[string]float64{"aws_instance": 0.001}); err != nil || !near(b.HourlyUSD, own) {
		t.Fatalf("a declared price below the list price must not lower it: %+v %v", b, err)
	}
	if b, err := priceOne(inst, map[string]float64{"aws_instance": 0.5}); err != nil || !near(b.HourlyUSD, 0.5) || b.Lines[0].Note != "declared" {
		t.Fatalf("a declared price above the list price wins: %+v %v", b, err)
	}
	// a free type declared costs its declared price; usage-priced lines compare their total
	if b, err := priceOne(Resource{Address: "aws_vpc.v", Type: "aws_vpc"}, map[string]float64{"aws_vpc": 0.01}); err != nil || !near(b.HourlyUSD, 0.01) {
		t.Fatalf("declared free type: %+v %v", b, err)
	}
	s3 := []Resource{{Address: "aws_s3_bucket.d", Type: "aws_s3_bucket"}}
	b, err := PriceAWS(s3, "eu-west-1", map[string]float64{"aws_s3_bucket": 0.0001}, map[string]map[string]float64{"aws_s3_bucket.d": {"storage_gb": 730}}, prices())
	if err != nil || !near(b.HourlyUSD, 0.023) {
		t.Fatalf("stated usage above the declared price: %+v %v", b, err)
	}
	// where Crucible's own pricer fails closed, the declared price is the remedy
	for _, r := range []Resource{
		{Address: "aws_instance.d", Type: "aws_instance", Values: map[string]any{"instance_type": "t3.micro", "tenancy": "dedicated"}},
		{Address: "aws_instance.u", Type: "aws_instance", Values: map[string]any{}},
		{Address: "aws_instance.x", Type: "aws_instance", Values: map[string]any{"instance_type": "x9.huge"}},
		{Address: "aws_ebs_volume.v", Type: "aws_ebs_volume", Values: map[string]any{"type": "io2", "size": 10.0}},
		{Address: "aws_ebs_volume.n", Type: "aws_ebs_volume", Values: map[string]any{"type": "gp3"}},
	} {
		b, err := priceOne(r, map[string]float64{r.Type: 0.3})
		if err != nil || !near(b.HourlyUSD, 0.3) || b.Lines[0].Note != "declared" {
			t.Fatalf("%s: the declared price covers what Crucible can't price: %+v %v", r.Address, b, err)
		}
	}
}

func TestInstanceEBSBlockDevicesArePriced(t *testing.T) {
	base := 0.0114 + 8*0.088/730 // the instance and its default root
	plan := `{"resource_changes":[{"address":"aws_instance.i","mode":"managed","type":"aws_instance","change":{"actions":["create"],
		"after":{"instance_type":"t3.micro","associate_public_ip_address":false,"ebs_block_device":[{"device_name":"/dev/sdf","volume_size":50,"volume_type":"gp3"},{"device_name":"/dev/sdg","volume_size":10,"volume_type":"gp3"}]},
		"after_unknown":{"ebs_block_device":[{"volume_id":true},{"volume_id":true}]}}}]}`
	res, err := FromPlan([]byte(plan))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := priceOne(res[0], nil); err != nil || !near(b.HourlyUSD, base+60*0.088/730) {
		t.Fatalf("plan: each inline volume is priced: %+v %v", b, err)
	}
	state := `{"version":4,"resources":[{"mode":"managed","type":"aws_instance","name":"i","instances":[{"attributes":
		{"instance_type":"t3.micro","associate_public_ip_address":false,"root_block_device":[{"volume_size":8,"volume_type":"gp3"}],
		"ebs_block_device":[{"device_name":"/dev/sdf","volume_size":100,"volume_type":"gp3","iops":3000,"throughput":125}]}}]}]}`
	if res, err = FromState([]byte(state)); err != nil {
		t.Fatal(err)
	}
	if b, err := priceOne(res[0], nil); err != nil || !near(b.HourlyUSD, base+100*0.088/730) {
		t.Fatalf("state: each inline volume is priced: %+v %v", b, err)
	}
	inst := func(d map[string]any) Resource {
		return Resource{Address: "aws_instance.i", Type: "aws_instance", Values: map[string]any{"instance_type": "t3.micro", "ebs_block_device": []any{d}}}
	}
	for _, d := range []map[string]any{
		{"volume_type": "gp3"},                                           // size unknown at plan time
		{"volume_size": 10.0},                                            // type unknown at plan time
		{"volume_size": 10.0, "volume_type": nil},                        // type null
		{"volume_size": 10.0, "volume_type": "io2", "iops": 1000.0},      // provisioned IOPS
		{"volume_size": 10.0, "volume_type": "gp3", "throughput": 500.0}, // gp3 throughput above 125
	} {
		if _, err := priceOne(inst(d), nil); err == nil || !strings.Contains(err.Error(), "aws_instance.i") {
			t.Fatalf("must fail closed: %v %v", d, err)
		}
	}
}

func TestUsageMatchesCountedModules(t *testing.T) {
	res := []Resource{{Address: `module.net[0].aws_nat_gateway.x`, Type: "aws_nat_gateway", Values: map[string]any{"connectivity_type": "private"}},
		{Address: `module.a["k"].module.b.aws_s3_bucket.y[1]`, Type: "aws_s3_bucket"}}
	for _, usage := range []map[string]map[string]float64{
		{`module.net[0].aws_nat_gateway.x`: {"gb_processed": 730}, `module.a["k"].module.b.aws_s3_bucket.y`: {"storage_gb": 730}},
		{`module.net.aws_nat_gateway.x`: {"gb_processed": 730}, `module.a.module.b.aws_s3_bucket.y`: {"storage_gb": 730}},
	} {
		b, err := PriceAWS(res, "eu-west-1", nil, usage, prices())
		if err != nil || !near(b.HourlyUSD, 0.048+0.048+0.023) {
			t.Fatalf("%v: %+v %v", usage, b, err)
		}
	}
}
