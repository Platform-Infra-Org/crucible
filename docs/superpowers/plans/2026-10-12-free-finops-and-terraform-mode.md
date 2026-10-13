# Free FinOps and Terraform Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Price AWS labs and track their spend with no paid service (no infracost, no Cost Explorer), and accept `runtime: terraform` + `cloud: aws` labs behind a cloud interface.

**Architecture:** A new leaf package `internal/pricing` turns Terraform resources (from a plan or a state) into hourly and usage prices, using on-demand prices downloaded weekly from the free public AWS Price List into a `prices` table. The estimate runs `terraform plan` in a pod with a read-only plan role, prices the plan and caches per content version (replacing the infracost estimator). After each apply Crucible reads the lab's state and meters every priced resource in `lab_resources`; spend is metered live and replaced by the free CUR 2.0 export (CSV in an S3 bucket, read by the ops role) once settled. `deploy/aws/labs` gains the plan role, the export and a free AWS Budget.

**Tech Stack:** Go 1.26 (stdlib JSON streaming, `encoding/csv`, `compress/gzip`, aws-sdk-go-v2 STS/S3), Postgres (goose), Terraform (mock-provider `terraform test`), React.

**Spec:** `docs/superpowers/specs/2026-10-12-free-finops-and-terraform-mode-design.md`

## Global Constraints

- Every shell that builds or tests starts with `export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH`.
- **Never create real AWS resources.** AWS code is tested against `awscloud.Fake` or `httptest`; Terraform only with mock providers (`terraform test`, `validate`).
- **No paid services for cost:** no infracost (CLI, key or API), no Cost Explorer (`ce:*`). Only the public AWS Price List files, S3 (Data Exports CSV), AWS Budgets, STS.
- **Fail closed:** missing or stale price data (older than 30 days), a failed plan, or a resource Crucible can't price and the lab doesn't declare means no estimate and the lab can't be requested — never $0.
- Lab format: `runtime: terraform` with `cloud: aws` and the `aws:` block; `runtime: aws` is an alias. Internally a Terraform lab's runtime key is its cloud name (`"aws"`), so runners, estimators and approvals keep their keys. `aws.region` is required.
- `aws.prices`: map of resource type → USD per hour (> 0). `aws.usage`: map of resource address → map of usage attribute → monthly amount (≥ 0). Hours per month: 730.
- Price data: on-demand, Linux, shared tenancy, `capacitystatus=Used`, `preInstalledSw=NA`, `licenseModel=No License required`; refreshed weekly for the allowed regions (`CRUCIBLE_AWS_LAB_REGIONS`).
- Modules: local only (unchanged lint). Metering reads state with the ops role; billed spend reads the CUR 2.0 export (CSV, gzip, daily, overwrite) with the ops role.
- Migrations: next free numbers `00030`, `00031`; every one has a working `-- +goose Down`.
- Tests first for every behaviour change; every money path keeps a test that fails without it. Commits: pathspec form, never `git add -A`; trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Capability changes update `docs/user` in the same commit (`go test ./internal/docs`).

## Review Focus

1. **Price data never downloaded or older than 30 days** (a fresh install, a blocked network): the lab page says estimates are unavailable and why; no lab is priced $0. Pinned in Task 3 (`TestStalePricesFailClosed`).
2. **`count`/`for_each` and module addresses** (`module.net.aws_nat_gateway.this[0]`): each instance is priced, and a usage entry keyed with or without the index applies. Pinned in Task 1 (`TestUsageMatchesIndexedAndModuleAddresses`).
3. **An unpriced resource hidden in a nested local module**: lint finds it statically, without a plan. Pinned in Task 2 (`TestUnpricedTypeInANestedModuleIsRefused`).
4. **An apply that fails half-way**: the resources it did create are metered, and destroy ends them. Pinned in Task 5 (`TestPartialApplyIsMetered`).
5. **A late, missing or malformed export file, or a month boundary**: ingest records the error, actuals go stale, metered spend is used; last month's late corrections still land; nothing goes negative. Pinned in Task 6 (`TestExportIngestEdges`).

---

### Task 1: The `pricing` package — resources, pricers and the free list

**Files:**
- Create: `internal/pricing/pricing.go`, `internal/pricing/aws.go`, `internal/pricing/pricing_test.go`, `internal/pricing/testdata/plan.json`, `internal/pricing/testdata/state.json`

**Interfaces:**
- Produces: `pricing.Resource{Address, Type string; Values map[string]any}`; `pricing.FromPlan(planJSON []byte) ([]Resource, error)` (resources the plan creates or keeps); `pricing.FromState(stateJSON []byte) ([]Resource, error)` (managed resources); `pricing.Lookup` interface `Price(service, region, key string) (usd float64, ok bool)`; `pricing.Line{Address, Type string; HourlyUSD, UsageHourlyUSD float64; Note string}`; `pricing.Breakdown{Lines []Line; HourlyUSD float64}`; `pricing.PriceAWS(res []Resource, region string, declared map[string]float64, usage map[string]map[string]float64, l Lookup) (Breakdown, error)`; `pricing.KnownAWS(resourceType string) bool` (priced or free); `pricing.Unpriced` error type listing types; `pricing.HoursPerMonth = 730`; price keys (below).

Price keys (what the price store stores and pricers look up), service `aws`:

| Key | Meaning |
|---|---|
| `ec2:<instance-type>` | instance hour |
| `ebs:<volume-type>` | GB-month (gp2, gp3, io1, io2, st1, sc1, standard) |
| `nat:hour`, `nat:gb` | NAT gateway hour, GB processed |
| `ipv4:hour` | public IPv4 address hour |
| `elb:application`, `elb:network` | load balancer hour |
| `s3:standard` | S3 Standard GB-month (first tier) |

- [ ] **Step 1: Fixtures and the failing tests**

`testdata/plan.json` — a minimal `terraform show -json` plan (format_version "1.2") whose `resource_changes` create: `aws_instance.web` (`instance_type: "t3.micro"`, `root_block_device: [{volume_size: 10, volume_type: "gp3"}]`), `aws_ebs_volume.data` (`size: 20, type: "gp3"`), `module.net.aws_nat_gateway.this[0]` (`connectivity_type: "public"`), `aws_eip.ip`, `aws_lb.app` (`load_balancer_type: "application"`), `aws_s3_bucket.data`, `aws_security_group.web`, `random_id.suffix`, and one resource with `"actions": ["no-op"]` that must be ignored. `testdata/state.json` — a state (version 4) with `aws_instance.web` (`instance_type: "t3.micro"`) in `resources[].instances[]`, one `data` mode resource that must be ignored, and `module.net.aws_nat_gateway.this` with `index_key: 0`.

```go
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
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/pricing`
Expected: FAIL (package does not exist).

- [ ] **Step 3: Implement**

`internal/pricing/pricing.go`:

```go
// Package pricing turns Terraform resources into hourly prices from list prices Crucible downloads for free
// (spec 2026-10-12 §2). It never guesses: a resource it can't price is an error, never $0.
package pricing

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const HoursPerMonth = 730.0

type Resource struct {
	Address string
	Type    string
	Values  map[string]any
}

type Lookup interface {
	Price(service, region, key string) (float64, bool)
}

type Line struct {
	Address        string  `json:"address"`
	Type           string  `json:"type"`
	HourlyUSD      float64 `json:"hourly_usd"`
	UsageHourlyUSD float64 `json:"usage_hourly_usd"`
	Note           string  `json:"note,omitempty"` // "free", "declared", "usage not stated", …
}

type Breakdown struct {
	Lines     []Line  `json:"lines"`
	HourlyUSD float64 `json:"hourly_usd"`
}

// Unpriced lists resource types Crucible can't price and the lab doesn't declare.
type Unpriced struct{ Types []string }

func (u *Unpriced) Error() string {
	return "no price for " + strings.Join(u.Types, ", ") + ": declare it under aws.prices (USD per hour)"
}

// FromPlan reads `terraform show -json` output: every resource the plan creates or keeps.
func FromPlan(b []byte) ([]Resource, error) {
	var p struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Mode    string `json:"mode"`
			Type    string `json:"type"`
			Change  struct {
				Actions []string       `json:"actions"`
				After   map[string]any `json:"after"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("reading the plan: %w", err)
	}
	var out []Resource
	for _, rc := range p.ResourceChanges {
		// an estimate plans against an empty state, so what will exist is what the plan creates or updates
		if rc.Mode != "managed" || !(slices.Contains(rc.Change.Actions, "create") || slices.Contains(rc.Change.Actions, "update")) {
			continue
		}
		out = append(out, Resource{Address: rc.Address, Type: rc.Type, Values: rc.Change.After})
	}
	return out, nil
}

// FromState reads a Terraform state (version 4): every managed resource instance.
func FromState(b []byte) ([]Resource, error) {
	var s struct {
		Resources []struct {
			Module    string `json:"module"`
			Mode      string `json:"mode"`
			Type      string `json:"type"`
			Name      string `json:"name"`
			Instances []struct {
				IndexKey   any            `json:"index_key"`
				Attributes map[string]any `json:"attributes"`
			} `json:"instances"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("reading the state: %w", err)
	}
	var out []Resource
	for _, r := range s.Resources {
		if r.Mode != "managed" {
			continue
		}
		base := r.Type + "." + r.Name
		if r.Module != "" {
			base = r.Module + "." + base
		}
		for _, in := range r.Instances {
			addr := base
			switch k := in.IndexKey.(type) {
			case float64:
				addr = fmt.Sprintf("%s[%d]", base, int(k))
			case string:
				addr = fmt.Sprintf("%s[%q]", base, k)
			}
			out = append(out, Resource{Address: addr, Type: r.Type, Values: in.Attributes})
		}
	}
	return out, nil
}

// usageFor finds the stated monthly usage for an address, by exact address or the address without its index.
func usageFor(usage map[string]map[string]float64, addr string) map[string]float64 {
	if u, ok := usage[addr]; ok {
		return u
	}
	if i := strings.LastIndex(addr, "["); i > 0 && strings.HasSuffix(addr, "]") {
		return usage[addr[:i]]
	}
	return nil
}

func str(v map[string]any, k string) string { s, _ := v[k].(string); return s }

func num(v map[string]any, k string) float64 {
	switch n := v[k].(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}

// first returns the first element of a nested block list (Terraform renders blocks as lists).
func first(v map[string]any, k string) map[string]any {
	if l, ok := v[k].([]any); ok && len(l) > 0 {
		m, _ := l[0].(map[string]any)
		return m
	}
	return nil
}
```

`internal/pricing/aws.go`:

```go
package pricing

import (
	"fmt"
	"slices"
	"strings"
)

// freeAWS are resource types with no charge of their own (their cost, if any, is in a priced resource).
var freeAWS = map[string]bool{
	"aws_vpc": true, "aws_subnet": true, "aws_security_group": true, "aws_security_group_rule": true,
	"aws_vpc_security_group_ingress_rule": true, "aws_vpc_security_group_egress_rule": true,
	"aws_route_table": true, "aws_route_table_association": true, "aws_route": true, "aws_internet_gateway": true,
	"aws_default_route_table": true, "aws_default_security_group": true, "aws_key_pair": true,
	"aws_lb_listener": true, "aws_lb_target_group": true, "aws_lb_target_group_attachment": true,
	"aws_s3_bucket_versioning": true, "aws_s3_bucket_public_access_block": true,
	"aws_s3_bucket_server_side_encryption_configuration": true, "aws_s3_bucket_policy": true,
	"aws_s3_bucket_ownership_controls": true, "aws_s3_object": true,
	"aws_network_interface": true, "aws_eip_association": true,
	"random_id": true, "random_string": true, "random_password": true, "random_pet": true, "random_integer": true,
	"tls_private_key": true, "terraform_data": true, "null_resource": true, "time_sleep": true,
}

// pricedAWS are the types a pricer below handles.
var pricedAWS = []string{"aws_instance", "aws_ebs_volume", "aws_nat_gateway", "aws_eip", "aws_lb", "aws_alb", "aws_s3_bucket"}

// KnownAWS: Crucible can price this type (or it's free) without a declaration.
func KnownAWS(t string) bool { return freeAWS[t] || slices.Contains(pricedAWS, t) }

// PriceAWS prices resources in one region. Declared prices win over Crucible's own; anything else unknown is an error.
func PriceAWS(res []Resource, region string, declared map[string]float64, usage map[string]map[string]float64, l Lookup) (Breakdown, error) {
	var b Breakdown
	var missing []string
	get := func(key string) (float64, error) {
		if v, ok := l.Price("aws", region, key); ok {
			return v, nil
		}
		return 0, fmt.Errorf("no %s price for %s in %s (price data missing or out of date)", "aws", key, region)
	}
	for _, r := range res {
		line := Line{Address: r.Address, Type: r.Type}
		u := usageFor(usage, r.Address)
		if p, ok := declared[r.Type]; ok {
			line.HourlyUSD, line.Note = p, "declared"
			b.Lines = append(b.Lines, line)
			b.HourlyUSD += p
			continue
		}
		switch {
		case freeAWS[r.Type]:
			line.Note = "free"
		case r.Type == "aws_instance":
			p, err := get("ec2:" + str(r.Values, "instance_type"))
			if err != nil {
				return b, err
			}
			line.HourlyUSD = p
			if rb := first(r.Values, "root_block_device"); rb != nil {
				vt := str(rb, "volume_type")
				if vt == "" {
					vt = "gp3"
				}
				size := num(rb, "volume_size")
				if size == 0 {
					size = 8 // the smallest common AMI root; the real size is metered from the state
				}
				gb, err := get("ebs:" + vt)
				if err != nil {
					return b, err
				}
				line.HourlyUSD += size * gb / HoursPerMonth
			}
		case r.Type == "aws_ebs_volume":
			vt := str(r.Values, "type")
			if vt == "" {
				vt = "gp3"
			}
			gb, err := get("ebs:" + vt)
			if err != nil {
				return b, err
			}
			line.HourlyUSD = num(r.Values, "size") * gb / HoursPerMonth
		case r.Type == "aws_nat_gateway":
			h, err := get("nat:hour")
			if err != nil {
				return b, err
			}
			line.HourlyUSD = h
			if str(r.Values, "connectivity_type") != "private" {
				ip, err := get("ipv4:hour")
				if err != nil {
					return b, err
				}
				line.HourlyUSD += ip
			}
			if gbs, ok := u["gb_processed"]; ok {
				gb, err := get("nat:gb")
				if err != nil {
					return b, err
				}
				line.UsageHourlyUSD = gbs * gb / HoursPerMonth
			} else {
				line.Note = "usage not stated"
			}
		case r.Type == "aws_eip":
			ip, err := get("ipv4:hour")
			if err != nil {
				return b, err
			}
			line.HourlyUSD = ip
		case r.Type == "aws_lb" || r.Type == "aws_alb":
			kind := str(r.Values, "load_balancer_type")
			if kind == "" {
				kind = "application"
			}
			h, err := get("elb:" + kind)
			if err != nil {
				return b, err
			}
			line.HourlyUSD = h
		case r.Type == "aws_s3_bucket":
			if gbs, ok := u["storage_gb"]; ok {
				gb, err := get("s3:standard")
				if err != nil {
					return b, err
				}
				line.UsageHourlyUSD = gbs * gb / HoursPerMonth
			} else {
				line.Note = "usage not stated"
			}
		default:
			missing = append(missing, r.Type)
			continue
		}
		b.Lines = append(b.Lines, line)
		b.HourlyUSD += line.HourlyUSD + line.UsageHourlyUSD
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return b, &Unpriced{Types: slices.Compact(missing)}
	}
	return b, nil
}
```

Drop the `strings` import from `aws.go` if nothing in it uses `strings`. In `TestPricesAPlan` also assert no line has the no-op resource's address.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/pricing && go vet ./internal/pricing`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pricing
git commit -m "feat(pricing): price Terraform resources from list prices; unpriced is an error, never \$0

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/pricing
```

---

### Task 2: The lab format — `runtime: terraform`, `prices`, `usage`, and an offline type check

**Files:**
- Modify: `internal/content/types.go` (`Lab.Cloud`, `AWSConfig.Prices`, `AWSConfig.Usage`), `internal/content/load.go` (`lab()` runtime switch, `awsModule`), `internal/content/load_test.go`
- Modify: `internal/content/blocks/fields.go` (+ registry test) for the new fields

**Interfaces:**
- Consumes: `pricing.KnownAWS` (Task 1).
- Produces: `content.Lab.Cloud string` (yaml `cloud`); after Load a Terraform lab has `Runtime == "aws"` and `Cloud == "aws"` whether written `runtime: aws` or `runtime: terraform` + `cloud: aws`; `content.AWSConfig{Region string; MaxHourlyUSD float64; Prices map[string]float64; Usage map[string]map[string]float64}` (yaml `prices`, `usage`).

- [ ] **Step 1: Write the failing tests** (in `load_test.go`, using the existing aws-lab fixture helper; find it by `runtime: aws` in the file)

```go
func TestTerraformRuntimeIsAWSUnderTheHood(t *testing.T) {
	// the existing aws fixture, with `runtime: terraform` + `cloud: aws` instead of `runtime: aws`
	tr, probs := Load(awsTree(t, map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(awsLabYAML, "runtime: aws", "runtime: terraform\ncloud: aws", 1)}))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	if l := tr.Modules[0].Lab; l.Runtime != "aws" || l.Cloud != "aws" {
		t.Fatalf("%q %q", l.Runtime, l.Cloud)
	}
	_, probs = Load(awsTree(t, map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(awsLabYAML, "runtime: aws", "runtime: terraform\ncloud: azure", 1)}))
	if !strings.Contains(fmt.Sprint(probs), "cloud must be aws") {
		t.Fatal(probs)
	}
}

func TestPricesAndUsageAreChecked(t *testing.T) {
	for name, c := range map[string]struct{ extra, want string }{
		"bad price":     {"  prices: {aws_kinesis_stream: 0}\n", "aws.prices"},
		"not a type":    {"  prices: {Kinesis: 1}\n", "aws.prices"},
		"negative use":  {"  usage: {aws_s3_bucket.data: {storage_gb: -1}}\n", "aws.usage"},
		"bad address":   {"  usage: {\"not an address\": {storage_gb: 1}}\n", "aws.usage"},
	} {
		t.Run(name, func(t *testing.T) {
			_, probs := Load(awsTree(t, map[string]string{"modules/m1/lab/lab.yaml": awsLabYAML + c.extra}))
			if !strings.Contains(fmt.Sprint(probs), c.want) {
				t.Fatalf("want %q in %v", c.want, probs)
			}
		})
	}
}

func TestUnpricedTypeInANestedModuleIsRefused(t *testing.T) {
	files := map[string]string{
		"modules/m1/lab/terraform/main.tf":           "module \"q\" {\n  source = \"./q\"\n}\n",
		"modules/m1/lab/terraform/q/main.tf":         "resource \"aws_kinesis_stream\" \"s\" {\n  name = \"s\"\n  shard_count = 1\n}\n",
	}
	_, probs := Load(awsTree(t, files))
	if !strings.Contains(fmt.Sprint(probs), "aws_kinesis_stream") || !strings.Contains(fmt.Sprint(probs), "aws.prices") {
		t.Fatalf("an unpriced type deep in a local module is refused: %v", probs)
	}
	ok := map[string]string{"modules/m1/lab/lab.yaml": awsLabYAML + "  prices: {aws_kinesis_stream: 0.015}\n"}
	for k, v := range files {
		ok[k] = v
	}
	if _, probs := Load(awsTree(t, ok)); strings.Contains(fmt.Sprint(probs), "aws_kinesis_stream") {
		t.Fatalf("declared: %v", probs)
	}
}
```

Adjust `awsTree`/`awsLabYAML` to the file's real helper names (the aws fixture the existing `awsModule` tests use); `awsLabYAML` must end inside the `aws:` block so the appended lines are part of it.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/content -run 'Terraform|PricesAndUsage|Unpriced'`
Expected: FAIL (unknown fields / no problems).

- [ ] **Step 3: Implement**

1. `types.go`: add `Cloud string \`yaml:"cloud"\`` to `Lab` (comment: "runtime terraform: the cloud it runs on (aws)"); extend `AWSConfig`:

```go
type AWSConfig struct {
	Region       string                        `yaml:"region"`
	MaxHourlyUSD float64                       `yaml:"max_hourly_usd"`
	Prices       map[string]float64            `yaml:"prices"` // resource type → USD per hour, for types Crucible can't price
	Usage        map[string]map[string]float64 `yaml:"usage"`  // resource address → usage attribute → monthly amount
}
```

2. `load.go` `lab()`: before the runtime switch,

```go
	if lab.Runtime == "terraform" { // a Terraform lab runs on its cloud's runtime (spec 2026-10-12 §1)
		if lab.Cloud != "aws" {
			l.add(lf, "cloud must be aws for runtime: terraform")
		}
		lab.Runtime = "aws"
	} else if lab.Runtime == "aws" {
		lab.Cloud = "aws"
	} else if lab.Cloud != "" {
		l.add(lf, "cloud is only for runtime: terraform")
	}
```

and change the default case's message to `runtime must be local, cluster, terraform (or aws)`.

3. `awsModule`: after the existing region and `max_hourly_usd` checks,

```go
	typeRe := regexp.MustCompile(`^[a-z0-9]+_[a-z0-9_]+$`)
	addrRe := regexp.MustCompile(`^(module\.[a-zA-Z0-9_-]+\.)*[a-z0-9]+_[a-z0-9_]+\.[a-zA-Z0-9_-]+(\[[^\]]+\])?$`)
	for t, p := range lab.AWS.Prices {
		if !typeRe.MatchString(t) || !(p > 0) {
			l.add(lf, "aws.prices: %s needs a resource type and a price above 0 (USD per hour)", t)
		}
	}
	for a, u := range lab.AWS.Usage {
		if !addrRe.MatchString(a) {
			l.add(lf, "aws.usage: %q is not a resource address", a)
		}
		for k, v := range u {
			if v < 0 {
				l.add(lf, "aws.usage: %s.%s must not be negative", a, k)
			}
		}
	}
```

and a static walk of every `resource "<type>" "<name>"` block in the lab's `terraform/` tree (the module files the lint already parses with HCL — reuse that parse; collect each managed resource block's type): any type for which `!pricing.KnownAWS(t)` and not in `lab.AWS.Prices` → `l.add(file, "%s: Crucible can't price this resource type yet; declare it under aws.prices (USD per hour)", t)`. Walk nested local module directories (the lint already follows local `source` paths for its checks — reuse that set of files).

4. `blocks/fields.go`: describe `Lab.cloud`, `AWSConfig.prices`, `AWSConfig.usage`; add to the registry test's types if it enumerates them. Update the `lab.aws` catalog block's template to `runtime: terraform` + `cloud: aws` if it generates an AWS lab.

- [ ] **Step 4: Run the content tests**

Run: `go test ./internal/content/... && go vet ./...`
Expected: PASS (update any test that asserted the old "runtime must be …" message).

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(content): runtime terraform with cloud aws; aws.prices and aws.usage; unpriced types refused offline

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/content
```

---

### Task 3: The price store and its weekly refresh from the public AWS Price List

**Files:**
- Create: `internal/db/migrations/00030_prices.sql`
- Create: `internal/pricing/store.go`, `internal/pricing/fetch.go`, `internal/pricing/store_test.go`, `internal/pricing/fetch_test.go`, `internal/pricing/testdata/offers/…`
- Modify: `internal/labs/jobs.go` (`PriceArgs`, `PriceWorker`), `cmd/crucible-api/main.go` (register worker + weekly periodic job)

**Interfaces:**
- Consumes: Task 1's key scheme.
- Produces: tables `prices(service, region, key, usd, unit, updated_at, PK(service, region, key))` and `price_refresh(id=1, ok_at, error, error_at)`; `pricing.Store{DB *pgxpool.Pool; Now func() time.Time}` implementing `Lookup` via a snapshot: `(*Store) Snapshot(ctx) (Snapshot, error)` (all rows for allowed regions, fails with `ErrStale` when `ok_at` is missing or older than 30 days), `Snapshot` implements `Lookup`; `pricing.Fetcher{HTTP *http.Client; Base string /* https://pricing.us-east-1.amazonaws.com */}`; `(*Fetcher) AWS(ctx, regions []string) ([]Row, error)`; `pricing.Row{Service, Region, Key, Unit string; USD float64}`; `(*Store) Replace(ctx, rows []Row) error` (one transaction, sets `ok_at`); `(*Store) Failed(ctx, err error) error`; `pricing.ErrStale`; labs job `PriceArgs` kind `"price_refresh"`, every 7 days, run on start.

- [ ] **Step 1: The migration**

```sql
-- +goose Up
-- List prices Crucible downloads from the free public AWS Price List (spec 2026-10-12 §2).
CREATE TABLE prices (
  service    TEXT NOT NULL,          -- aws
  region     TEXT NOT NULL,
  key        TEXT NOT NULL,          -- ec2:t3.micro, ebs:gp3, nat:hour, …
  usd        DOUBLE PRECISION NOT NULL CHECK (usd >= 0),
  unit       TEXT NOT NULL,          -- Hrs, GB-Mo, GB
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (service, region, key)
);
CREATE TABLE price_refresh (
  id       INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  ok_at    TIMESTAMPTZ,
  error    TEXT NOT NULL DEFAULT '',
  error_at TIMESTAMPTZ
);
INSERT INTO price_refresh (id) VALUES (1);

-- +goose Down
DROP TABLE price_refresh, prices;
```

- [ ] **Step 2: Write the failing tests**

`fetch_test.go`: an `httptest.Server` serving `/offers/v1.0/aws/index.json` (with `offers.AmazonEC2.currentRegionIndexUrl`, `AmazonVPC`, `AWSELB`, `AmazonS3`), each service's `region_index.json` (`regions.eu-west-1.currentVersionUrl`), and small region offer files under `testdata/offers/` holding: an EC2 `Compute Instance` t3.micro (Linux, Shared, NA, Used, No License required) plus decoys that must be skipped (Windows, Dedicated, `capacitystatus=Reserved`, a different `preInstalledSw`); EBS `Storage` gp3 (`volumeApiName`), `NAT Gateway` hour and bytes usagetypes; VPC public IPv4 `InUseAddress`; ELB `Load Balancer-Application` hour; S3 `Storage` General Purpose first tier. Each with `terms.OnDemand.<sku>.<offer>.priceDimensions.<d>.pricePerUnit.USD` and `unit`.

```go
func TestFetchKeepsOnlyWhatLabsUse(t *testing.T) {
	srv := offerServer(t) // serves testdata/offers as the public layout
	rows, err := (&Fetcher{HTTP: srv.Client(), Base: srv.URL}).AWS(context.Background(), []string{"eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, r := range rows {
		got[r.Key] = r.USD
	}
	for k, v := range map[string]float64{"ec2:t3.micro": 0.0114, "ebs:gp3": 0.088, "nat:hour": 0.048, "nat:gb": 0.048,
		"ipv4:hour": 0.005, "elb:application": 0.0252, "s3:standard": 0.023} {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != 7 {
		t.Errorf("decoys leaked in: %v", got)
	}
}

func TestFetchFailsOnABrokenOffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	if _, err := (&Fetcher{HTTP: srv.Client(), Base: srv.URL}).AWS(context.Background(), []string{"eu-west-1"}); err == nil {
		t.Fatal("a failed download is an error")
	}
}
```

`store_test.go` (Postgres via `dbtest.New`):

```go
func TestStalePricesFailClosed(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	s := &Store{DB: dbtest.New(t), Now: func() time.Time { return now }}
	if _, err := s.Snapshot(ctx); !errors.Is(err, ErrStale) {
		t.Fatalf("never refreshed: %v", err)
	}
	if err := s.Replace(ctx, []Row{{Service: "aws", Region: "eu-west-1", Key: "ec2:t3.micro", Unit: "Hrs", USD: 0.0114}}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := snap.Price("aws", "eu-west-1", "ec2:t3.micro"); !ok || p != 0.0114 {
		t.Fatalf("%v %v", p, ok)
	}
	now = now.Add(31 * 24 * time.Hour)
	if _, err := s.Snapshot(ctx); !errors.Is(err, ErrStale) {
		t.Fatalf("older than 30 days: %v", err)
	}
	if err := s.Failed(ctx, errors.New("blocked")); err != nil {
		t.Fatal(err)
	}
	// a failed refresh keeps the old rows and records why (shown to admins)
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/pricing`
Expected: FAIL (undefined Store / Fetcher).

- [ ] **Step 4: Implement**

`fetch.go`: for each service in `{"AmazonEC2", "AmazonVPC", "AWSELB", "AmazonS3"}` read `<Base>/offers/v1.0/aws/index.json` → the service's `currentRegionIndexUrl` → its `regions[region].currentVersionUrl` → the offer file. Offer files are large (EC2 is hundreds of MB): **stream** them with `json.Decoder` (`Token()` to walk `products` then `terms.OnDemand`); keep matching SKUs in a map from the products pass, then read only their on-demand price dimensions. Matching rules (one `match(product) (key string, ok bool)` per service):

| Service | Product attributes | Key |
|---|---|---|
| AmazonEC2 | `productFamily=Compute Instance`, `operatingSystem=Linux`, `tenancy=Shared`, `preInstalledSw=NA`, `capacitystatus=Used`, `licenseModel=No License required` | `ec2:<instanceType>` |
| AmazonEC2 | `productFamily=Storage`, `volumeApiName=<t>` | `ebs:<t>` |
| AmazonEC2 | `productFamily=NAT Gateway`, `usagetype` ends `NatGateway-Hours` / `NatGateway-Bytes` | `nat:hour` / `nat:gb` |
| AmazonVPC | `usagetype` ends `PublicIPv4:InUseAddress` | `ipv4:hour` |
| AWSELB | `productFamily=Load Balancer-Application` / `Load Balancer-Network`, `usagetype` ends `LoadBalancerUsage` | `elb:application` / `elb:network` |
| AmazonS3 | `productFamily=Storage`, `storageClass=General Purpose`, `volumeType=Standard` | `s3:standard` (the first price tier, `beginRange=0`) |

Bound each download (`http.Client` with a 10-minute timeout; refuse bodies above 2 GiB) and fail the whole fetch on any error. `store.go`: `Replace` deletes and inserts the `aws` rows in one transaction and sets `price_refresh.ok_at = now, error = ''`; `Failed` sets `error`, `error_at`; `Snapshot` loads rows into a map and returns `ErrStale` (`errors.New("price data is missing or older than 30 days; estimates are unavailable until the weekly refresh succeeds")`) when `ok_at` is null or older than 30 days.

`internal/labs/jobs.go`: `type PriceArgs struct{}` (Kind `"price_refresh"`), a `PriceWorker{S *Service}` whose `Work` calls a new `(*Service).RefreshPrices(ctx)` → fetch for `s.AWSRegions` then `Replace`, or `Failed` on error (and return the error). Add `Prices *pricing.Store` and `PriceFetch *pricing.Fetcher` fields on `labs.Service`. In `cmd/crucible-api/main.go` add `{Every: 7 * 24 * time.Hour, Args: labs.PriceArgs{}}` to `periodicJobs()` and register the worker next to `CostWorker` (find the `river.AddWorker` calls). In dry-run mode skip the job and seed fixture prices (Task 8).

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/pricing ./internal/labs ./cmd/crucible-api && go vet ./...`
Expected: PASS (add a `TestPriceRefreshRunsWeekly` alongside `TestReaperRunsHourlyCostExplorerEverySixHours`).

- [ ] **Step 6: Commit**

```bash
git add internal/db/migrations/00030_prices.sql internal/pricing
git commit -m "feat(pricing): weekly list prices from the free public AWS Price List; stale data fails closed

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/db/migrations/00030_prices.sql internal/pricing internal/labs/jobs.go internal/labs/service.go cmd/crucible-api
```

---

### Task 4: Estimates from a read-only `terraform plan`

**Files:**
- Modify: `internal/awscloud/awscloud.go`, `client.go`, `fake.go` (+ tests) — `AssumePlan`
- Create: `internal/labs/tf_estimate.go`, `internal/labs/tf_estimate_test.go`
- Modify: `internal/labs/aws_objects.go` (a plan pod), `internal/labs/aws.go` (`(*AWSRunner) Plan`)
- Delete: `internal/labs/aws_estimate.go`, `internal/labs/aws_estimate_test.go` (the infracost estimator)
- Modify: `internal/labs/service.go` (`ModuleLab` carries the breakdown)

**Interfaces:**
- Consumes: `pricing.FromPlan`, `pricing.PriceAWS`, `pricing.Store.Snapshot` (Tasks 1, 3), `content.AWSConfig.Prices/Usage` (Task 2).
- Produces: `awscloud.Cloud.AssumePlan(ctx context.Context) (Credentials, error)` (session name `crucible-plan`, 15-minute duration); `awscloud.Config.PlanRoleARN`; `labs.Planner` interface `Plan(ctx context.Context, lab *content.Lab) ([]byte, error)` (plan JSON); `labs.TerraformEstimator{Plan Planner; Prices func(ctx) (pricing.Lookup, error); Now func() time.Time}` implementing `Estimator` with the same caching, two-slot bound, in-flight sharing, 5-minute failure memory and panic recovery as the infracost estimator it replaces; `(*TerraformEstimator) Breakdown(ctx, lab) (pricing.Breakdown, error)`; `ModuleLab.Estimate *pricing.Breakdown` (json `estimate_breakdown`).

- [ ] **Step 1: Write the failing tests**

```go
type fakePlanner struct {
	calls int
	plan  []byte
	err   error
}

func (f *fakePlanner) Plan(context.Context, *content.Lab) ([]byte, error) { f.calls++; return f.plan, f.err }

func TestTerraformEstimatePricesThePlanOncePerVersion(t *testing.T) {
	ctx := context.Background()
	p := &fakePlanner{plan: mustRead(t, "../pricing/testdata/plan.json")}
	e := &TerraformEstimator{Plan: p, Prices: func(context.Context) (pricing.Lookup, error) { return testPrices(), nil }}
	lab := &content.Lab{ID: "l", Dir: "/content/abc/lab", Runtime: "aws", AWS: &content.AWSConfig{Region: "eu-west-1", MaxHourlyUSD: 1,
		Usage: map[string]map[string]float64{"aws_s3_bucket.data": {"storage_gb": 100}}}}
	h, err := e.HourlyUSD(ctx, lab)
	if err != nil || h <= 0 {
		t.Fatalf("%v %v", h, err)
	}
	if _, err := e.HourlyUSD(ctx, lab); err != nil || p.calls != 1 {
		t.Fatalf("cached per content version: %d calls, %v", p.calls, err)
	}
}

func TestTerraformEstimateFailsClosed(t *testing.T) {
	ctx := context.Background()
	lab := &content.Lab{ID: "l", Dir: "/c/1", Runtime: "aws", AWS: &content.AWSConfig{Region: "eu-west-1", MaxHourlyUSD: 0.01}}
	for name, e := range map[string]*TerraformEstimator{
		"plan fails":   {Plan: &fakePlanner{err: errors.New("boom")}, Prices: func(context.Context) (pricing.Lookup, error) { return testPrices(), nil }},
		"stale prices": {Plan: &fakePlanner{plan: mustRead(t, "../pricing/testdata/plan.json")}, Prices: func(context.Context) (pricing.Lookup, error) { return nil, pricing.ErrStale }},
		"over ceiling": {Plan: &fakePlanner{plan: mustRead(t, "../pricing/testdata/plan.json")}, Prices: func(context.Context) (pricing.Lookup, error) { return testPrices(), nil }},
	} {
		h, err := e.HourlyUSD(ctx, lab)
		if err == nil || h != 0 {
			t.Errorf("%s: must fail, got %v %v", name, h, err)
		}
	}
}
```

Port the infracost estimator's `Bounds` and `SurvivesPanickingRun` tests to `TerraformEstimator` (same assertions), and keep `TestAWSQuoteBlocksOverPricedAndForeignLabs` passing against it. In `internal/awscloud/client_test.go` add `TestAssumePlanUsesThePlanRole` (httptest STS stand-in asserting the role ARN, session name `crucible-plan` and 900 s duration), mirroring `TestAssumeLabTagsTheSession`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs -run 'TerraformEstimate' ./internal/awscloud -run AssumePlan`
Expected: FAIL.

- [ ] **Step 3: Implement**

1. `awscloud`: add `PlanRoleARN string` to `Config`; `AssumePlan` like `AssumeLab` without session tags (`RoleSessionName: "crucible-plan"`, `DurationSeconds: 900`); add to the `Cloud` interface; `Fake.AssumePlan` returns fixed credentials.
2. `aws_objects.go`: a plan pod spec built like the terraform apply pod (same image, NetworkPolicy, IMDS guard, `AWS_EC2_METADATA_DISABLED`), in a dedicated namespace `crucible-estimates` (created on first use, labelled for the existing NetworkPolicy pattern), running: unpack the module bundle (`ModuleBundle` with `content.LabTFVars("estimate", …, region)`), write an empty backend override so no state is touched (`terraform init -backend=false`), `terraform plan -refresh=false -lock=false -input=false -out=p && terraform show -json p`. Credentials from `AssumePlan` in a secret mounted like the lab's. The pod's stdout is the plan JSON; read it from the pod logs capped at 16 MiB; delete the pod and secret afterwards.
3. `aws.go`: `func (a *AWSRunner) Plan(ctx context.Context, lab *content.Lab) ([]byte, error)` doing the above via `a.Cluster` and `a.Cloud.AssumePlan`; in dry-run (`a.DryRun != nil`) it returns an error (dry run prices from fixed rates, Task 8).
4. `tf_estimate.go`: copy `aws_estimate.go`'s caching/slots/failure/panic machinery, with the run step = `Plan` → `pricing.FromPlan` → `Prices(ctx)` → `pricing.PriceAWS(res, lab.AWS.Region, lab.AWS.Prices, lab.AWS.Usage, lookup)`; if the hourly price exceeds `lab.AWS.MaxHourlyUSD` return `apperr.Wrap(apperr.Invalid, fmt.Sprintf("the estimate $%.4f an hour is over this lab's max_hourly_usd $%.4f", h, max))`. Keep the breakdown in the cache next to the hourly figure for `Breakdown`.
5. `service.go` `ModuleLab`: when the runtime's estimator is a `*TerraformEstimator` and the lab is aws, set `Estimate` to its breakdown (nil on error — the existing `Blocked`/message path explains the error).
6. Delete `aws_estimate.go` and its test.

- [ ] **Step 4: Run the tests**

Run: `go vet ./... && go test -race ./internal/labs ./internal/awscloud`
Expected: PASS (fix compile errors where `InfracostEstimator` was referenced — Task 8 rewires main; keep main compiling by switching its literal to `&labs.TerraformEstimator{Plan: ar, Prices: …}` here if needed).

- [ ] **Step 5: Commit**

```bash
git add internal/labs/tf_estimate.go internal/labs/tf_estimate_test.go
git rm internal/labs/aws_estimate.go internal/labs/aws_estimate_test.go
git commit -m "feat(labs): estimates from a read-only terraform plan priced by Crucible, cached per version

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/labs internal/awscloud cmd/crucible-api
```

---

### Task 5: Metered spend from Terraform state

**Files:**
- Create: `internal/db/migrations/00031_lab_resources.sql`
- Create: `internal/labs/meter.go`, `internal/labs/meter_test.go`
- Modify: `internal/awscloud` (`ReadState`), `internal/labs/aws.go` (after apply and destroy), `internal/labs/reaper.go` (reaped resources end), `internal/labs/approvals.go` (`labCostSQL`)

**Interfaces:**
- Consumes: `pricing.FromState`, `pricing.PriceAWS`, the price snapshot.
- Produces: table `lab_resources(lab_id, address, type, hourly_usd, usage_hourly_usd, started_at, ended_at, PK(lab_id, address, started_at))`; `awscloud.Cloud.ReadState(ctx, bucket, key string) ([]byte, error)` (ops role; `Fake` returns state it built in `SimulateApply`); `(*Service).Meter(ctx, inst *Instance, lab *content.Lab) error` (called after each successful or failed apply); `(*Service).EndMeter(ctx, labID string, at time.Time) error` (destroy / reap); `labCostSQL`'s `est_spent` becomes metered spend when the lab has `lab_resources` rows.

- [ ] **Step 1: The migration**

```sql
-- +goose Up
-- What each Terraform lab really created and for how long, priced at list price (spec 2026-10-12 §3).
CREATE TABLE lab_resources (
  lab_id           TEXT NOT NULL,
  address          TEXT NOT NULL,
  type             TEXT NOT NULL,
  hourly_usd       DOUBLE PRECISION NOT NULL CHECK (hourly_usd >= 0),
  usage_hourly_usd DOUBLE PRECISION NOT NULL CHECK (usage_hourly_usd >= 0),
  started_at       TIMESTAMPTZ NOT NULL,
  ended_at         TIMESTAMPTZ,
  PRIMARY KEY (lab_id, address, started_at)
);
CREATE INDEX lab_resources_open ON lab_resources (lab_id) WHERE ended_at IS NULL;

-- +goose Down
DROP TABLE lab_resources;
```

- [ ] **Step 2: Write the failing tests** (`meter_test.go`, Postgres + `awscloud.Fake`)

Use the labs fixture (`setup(t, true)`); give `f.s` an `AWSRunner` with `Cloud: fake` and `StateBucket: "state"`, and a `pricing.Store` seeded with eu-west-1 rows `ec2:t3.nano 0.0052`, `ebs:gp3 0.088`, `nat:hour 0.048`, `ipv4:hour 0.005` (`Replace`). Insert an aws instance row the way the existing aws runner tests do (`f.s.insert(ctx, &Instance{… Runtime: "aws", State: Ready, ReadyAt: &t0 …})`). `fake.SetState(labID, json)` stores a state; write states inline:

```go
func stateWith(res ...string) []byte { // res: "aws_instance.web" or "aws_nat_gateway.gw"
	var b strings.Builder
	b.WriteString(`{"version":4,"resources":[`)
	for i, r := range res {
		typ, name, _ := strings.Cut(r, ".")
		attrs := `{}`
		if typ == "aws_instance" {
			attrs = `{"instance_type":"t3.nano","root_block_device":[{"volume_size":8,"volume_type":"gp3"}]}`
		}
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"mode":"managed","type":%q,"name":%q,"instances":[{"attributes":%s}]}`, typ, name, attrs)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func openRows(t *testing.T, f *fx, labID string) map[string]float64 {
	rows, err := f.s.DB.Query(context.Background(), `SELECT address, hourly_usd FROM lab_resources WHERE lab_id = $1 AND ended_at IS NULL`, labID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	var a string
	var h float64
	if _, err := pgx.ForEachRow(rows, []any{&a, &h}, func() error { out[a] = h; return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMeterRecordsStateAndEndsOnDestroy(t *testing.T) {
	ctx := context.Background()
	f, fake, inst, lab := meterFixture(t) // builds the above; inst is a ready aws lab in eu-west-1
	fake.SetState(inst.ID, stateWith("aws_instance.web"))
	if err := f.s.Meter(ctx, inst, lab); err != nil {
		t.Fatal(err)
	}
	want := 0.0052 + 8*0.088/730
	if r := openRows(t, f, inst.ID); len(r) != 1 || math.Abs(r["aws_instance.web"]-want) > 1e-12 {
		t.Fatalf("%v", r)
	}
	if err := f.s.Meter(ctx, inst, lab); err != nil || len(openRows(t, f, inst.ID)) != 1 {
		t.Fatalf("metering twice adds nothing: %v", err)
	}
	fake.SetState(inst.ID, stateWith()) // the instance is gone from the state
	if err := f.s.Meter(ctx, inst, lab); err != nil || len(openRows(t, f, inst.ID)) != 0 {
		t.Fatalf("a resource gone from the state ends: %v", err)
	}
	fake.SetState(inst.ID, stateWith("aws_instance.web"))
	_ = f.s.Meter(ctx, inst, lab)
	if err := f.s.EndMeter(ctx, inst.ID, f.clk.Now()); err != nil || len(openRows(t, f, inst.ID)) != 0 {
		t.Fatalf("EndMeter ends everything: %v", err)
	}
}

func TestPartialApplyIsMetered(t *testing.T) {
	ctx := context.Background()
	f, fake, inst, lab := meterFixture(t)
	fake.SetState(inst.ID, stateWith("aws_nat_gateway.gw")) // the apply failed after creating the NAT gateway
	fake.FailApply(inst.ID)                                  // runTF("apply") reports failure
	_ = f.s.provisionAWSForTest(ctx, inst, lab)              // the runner path that calls Meter after apply, success or not
	if r := openRows(t, f, inst.ID); math.Abs(r["aws_nat_gateway.gw"]-(0.048+0.005)) > 1e-12 {
		t.Fatalf("what the failed apply created is metered: %v", r)
	}
}

func TestMeteredSpendReplacesTheEstimate(t *testing.T) {
	ctx := context.Background()
	f, fake, inst, lab := meterFixture(t) // inst.HourlyUSD = 1.00 (the estimate), ready at t0
	fake.SetState(inst.ID, stateWith("aws_instance.web"))
	if err := f.s.Meter(ctx, inst, lab); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(10 * time.Hour)
	sp, err := f.s.spend(ctx, f.plat, inst.Team, inst.Training)
	if err != nil {
		t.Fatal(err)
	}
	want := 10 * (0.0052 + 8*0.088/730) // metered, not 10 h × the $1 estimate
	if math.Abs(sp.SpentUSD-want) > 1e-9 {
		t.Fatalf("spent %v, want %v", sp.SpentUSD, want)
	}
}
```

`meterFixture`, `fake.SetState`, `fake.FailApply` and `provisionAWSForTest` are small helpers to add (the last is the existing `ProvisionLab` path in dry-run mode with the Fake; use the real function if it can be driven directly). Use the fixture clock's real API (`f.clk`) for advancing time.

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/labs -run 'Meter|PartialApply'`
Expected: FAIL.

- [ ] **Step 4: Implement**

1. `awscloud.ReadState`: S3 `GetObject` with the ops config (state region from `AWSRunner.StateRegion`), capped at 32 MiB; `Fake` keeps a per-lab state built from `SimulateApply`'s resources in v4 state JSON (`aws_instance` t3.nano with an 8 GiB gp3 root).
2. `meter.go`:

```go
// Meter records what the lab's state holds now: new priced resources start now, ones gone from the state end now.
func (s *Service) Meter(ctx context.Context, inst *Instance, lab *content.Lab) error {
	raw, err := s.AWS.Cloud.ReadState(ctx, s.AWS.StateBucket, "labs/"+inst.ID+".tfstate")
	if err != nil {
		return err
	}
	res, err := pricing.FromState(raw)
	if err != nil {
		return err
	}
	snap, err := s.Prices.Snapshot(ctx)
	if err != nil {
		return err
	}
	b, err := pricing.PriceAWS(res, lab.AWS.Region, lab.AWS.Prices, lab.AWS.Usage, snap)
	if err != nil {
		var u *pricing.Unpriced
		if !errors.As(err, &u) {
			return err
		}
		// ponytail: lint and the estimate already refuse unpriced types; anything else here is metered at $0 and logged
		s.Log.Warn("metering found unpriced resources", "lab", inst.ID, "types", u.Types)
	}
	now := s.Now()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	seen := []string{}
	for _, l := range b.Lines {
		seen = append(seen, l.Address)
		if _, err := tx.Exec(ctx, `INSERT INTO lab_resources (lab_id, address, type, hourly_usd, usage_hourly_usd, started_at)
			SELECT $1, $2, $3, $4, $5, $6 WHERE NOT EXISTS (
				SELECT 1 FROM lab_resources WHERE lab_id = $1 AND address = $2 AND ended_at IS NULL)`,
			inst.ID, l.Address, l.Type, l.HourlyUSD, l.UsageHourlyUSD, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE lab_resources SET ended_at = $3
		WHERE lab_id = $1 AND ended_at IS NULL AND NOT (address = ANY($2))`, inst.ID, seen, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) EndMeter(ctx context.Context, labID string, at time.Time) error {
	_, err := s.DB.Exec(ctx, `UPDATE lab_resources SET ended_at = $2 WHERE lab_id = $1 AND ended_at IS NULL`, labID, at)
	return err
}
```

(Use the real field names on `Service` for the AWS runner and its state bucket; add `Prices *pricing.Store` from Task 3.)
3. `aws.go` `ProvisionLab`: after `runTF(..., "apply")` returns — success **or** failure — call `Meter` (log a metering error; never fail the lab on it). `Destroy`: after a successful destroy, `EndMeter(inst.ID, now)`. The reaper: when it deletes a lab's leftovers, `EndMeter(labID, now)`.
4. `approvals.go` `labCostSQL`: join metered spend and prefer it over the estimate-based figure:

```go
const labCostSQL = `SELECT l.*, a.usd AS actual,
	coalesce(a.usd > 0 AND a.updated_at >= l.destroyed_at + interval '48 hours'
		AND (SELECT ingest_ok_at FROM aws_ops) >= l.destroyed_at + interval '48 hours', false) AS settled,
	coalesce(m.usd, CASE WHEN l.ready_at IS NULL THEN 0
		ELSE l.hourly_usd * extract(epoch FROM least(coalesce(l.destroyed_at, $1), $1) - l.ready_at)::float8 / 3600 END) AS est_spent
	FROM lab_instances l
	LEFT JOIN (SELECT lab_id, sum(usd)::float8 AS usd, max(updated_at) AS updated_at FROM cost_actuals GROUP BY lab_id) a ON a.lab_id = l.id
	LEFT JOIN (SELECT lab_id, sum((hourly_usd + usage_hourly_usd)
		* extract(epoch FROM least(coalesce(ended_at, $1), $1) - least(started_at, $1))::float8 / 3600) AS usd
		FROM lab_resources GROUP BY lab_id) m ON m.lab_id = l.id`
```

- [ ] **Step 5: Run the tests**

Run: `go vet ./... && go test -race ./internal/labs ./internal/awscloud`
Expected: PASS (the existing spend/budget tests still pass: labs without rows keep the old figure).

- [ ] **Step 6: Commit**

```bash
git add internal/db/migrations/00031_lab_resources.sql internal/labs/meter.go internal/labs/meter_test.go
git commit -m "feat(labs): meter what each Terraform lab created, from its state; spend uses it until billed actuals settle

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/db/migrations/00031_lab_resources.sql internal/labs internal/awscloud
```

---

### Task 6: Billed spend from the free CUR 2.0 export (Cost Explorer removed)

**Files:**
- Modify: `internal/awscloud/client.go` (`Costs` reads the export), `internal/awscloud/awscloud.go` (`Config.ExportBucket`, `ExportPrefix`, `ExportName`), `internal/awscloud/client_test.go`, `internal/awscloud/testdata/cur/…`
- Modify: `internal/labs/reaper.go` (comments, window), `internal/labs/reaper_test.go`
- Remove: the Cost Explorer client dependency from `go.mod` if nothing else uses it

**Interfaces:**
- Consumes: the existing `Cloud.Costs(ctx, from, to time.Time) ([]DailyCost, error)` contract and `Service.IngestCosts`.
- Produces: `Client.Costs` implemented over S3: lists `s3://<ExportBucket>/<ExportPrefix>/<ExportName>/data/BILLING_PERIOD=<YYYY-MM>/` for each month the window touches, reads every `*.csv.gz`, sums `line_item_unblended_cost` per (`crucible:lab-id` tag, `line_item_usage_start_date` day), skipping untagged lines; returns `[]DailyCost`.

- [ ] **Step 1: Fixture and failing tests**

`testdata/cur/` holds two gzip CSV files with the CUR 2.0 header (at least `line_item_usage_start_date`, `line_item_unblended_cost`, `resource_tags`), where `resource_tags` is a JSON object string. Lines: two for lab `0123456789ab` on 2026-10-11 (0.10 and 0.05), one on 2026-10-12 (0.02), one untagged (0.50, skipped), one with a negative credit for the lab (-0.01), one malformed cost (`abc`). The tag key may appear as `user_crucible:lab-id` or `user_crucible_lab_id` — accept both, and pin both in the fixture.

```go
func TestCostsReadTheExport(t *testing.T) {
	srv := s3Stub(t, map[string][]byte{ // an httptest S3 (ListObjectsV2 + GetObject), like the existing STS stand-ins
		"exports/crucible-labs/data/BILLING_PERIOD=2026-10/part-0.csv.gz": gz(t, "testdata/cur/part-0.csv"),
		"exports/crucible-labs/data/BILLING_PERIOD=2026-10/part-1.csv.gz": gz(t, "testdata/cur/part-1.csv"),
	})
	c := newTestClient(t, srv, Config{ExportBucket: "bills", ExportPrefix: "exports", ExportName: "crucible-labs"})
	got, err := c.Costs(context.Background(), day("2026-10-01"), day("2026-10-13"))
	if err != nil {
		t.Fatal(err)
	}
	// 10-11: 0.10 + 0.05 - 0.01 = 0.14; 10-12: 0.02; untagged and malformed lines skipped (malformed → error? see below)
}

func TestExportIngestEdges(t *testing.T) {
	ctx := context.Background()
	base := "exports/crucible-labs/data/BILLING_PERIOD="
	csvHead := "line_item_usage_start_date,line_item_unblended_cost,resource_tags\n"
	row := func(day, cost, lab string) string {
		return day + "T00:00:00Z," + cost + `,"{""user_crucible:lab-id"":""` + lab + `""}"` + "\n"
	}
	cfg := Config{ExportBucket: "bills", ExportPrefix: "exports", ExportName: "crucible-labs"}

	// (a) no files yet: no error, no costs
	c := newTestClient(t, s3Stub(t, nil), cfg)
	if got, err := c.Costs(ctx, day("2026-10-01"), day("2026-10-13")); err != nil || len(got) != 0 {
		t.Fatalf("an export that hasn't run yet is not an error: %v %v", got, err)
	}
	// (b) not gzip / missing columns: an error (a half-read bill must never settle a lab)
	for _, body := range [][]byte{[]byte("not gzip"), gzBytes(t, "a,b\n1,2\n")} {
		c := newTestClient(t, s3Stub(t, map[string][]byte{base + "2026-10/part-0.csv.gz": body}), cfg)
		if _, err := c.Costs(ctx, day("2026-10-01"), day("2026-10-13")); err == nil {
			t.Fatal("a broken export file is an error")
		}
	}
	// (c) a window across a month boundary reads both months; last month's late correction lands
	c = newTestClient(t, s3Stub(t, map[string][]byte{
		base + "2026-09/part-0.csv.gz": gzBytes(t, csvHead+row("2026-09-30", "0.30", "0123456789ab")),
		base + "2026-10/part-0.csv.gz": gzBytes(t, csvHead+row("2026-10-01", "0.10", "0123456789ab")),
	}), cfg)
	got, err := c.Costs(ctx, day("2026-09-28"), day("2026-10-02"))
	if err != nil || len(got) != 2 {
		t.Fatalf("both months: %+v %v", got, err)
	}
	// (d) a net-negative day is returned as is here and clamped to 0 by IngestCosts (TestCostActualsRejectsNegative)
	c = newTestClient(t, s3Stub(t, map[string][]byte{base + "2026-10/part-0.csv.gz": gzBytes(t, csvHead+row("2026-10-05", "-0.40", "0123456789ab"))}), cfg)
	if got, err := c.Costs(ctx, day("2026-10-01"), day("2026-10-13")); err != nil || len(got) != 1 || got[0].USD != -0.40 {
		t.Fatalf("credits pass through to the clamp: %+v %v", got, err)
	}
}
```

Add to `internal/labs/reaper_test.go` a test that a `Costs` error leaves `aws_ops.ingest_ok_at` unchanged and the Ledger reports `ActualsStale`, using the Fake with a `FailCosts()` switch (add it). Write `TestCostsReadTheExport` in full too, asserting `{2026-10-11, 0123456789ab, 0.14}` and `{2026-10-12, 0123456789ab, 0.02}` and that the fixture's malformed-cost line makes the read fail (put that line in a separate fixture file so the happy-path test has none). Rule for malformed cost cells: the whole read fails (a half-read bill must not settle a lab), recorded by the existing ingest error path.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/awscloud -run 'Costs|Export' ./internal/labs -run Export`
Expected: FAIL.

- [ ] **Step 3: Implement**

Replace the Cost Explorer call in `Client.Costs` with the S3 reader (`s3.ListObjectsV2` paginated + `GetObject`, using `c.ops`; `gzip.NewReader` + `encoding/csv` with the header row mapped to column indexes; bound each object at 512 MiB uncompressed). Keep `Costs`' signature and `DailyCost` so `IngestCosts` is unchanged apart from comments. `Fake.Costs` is unchanged. Remove the `costexplorer` import and module if unused (`go mod tidy`; review `go.mod`).

- [ ] **Step 4: Run the tests**

Run: `go vet ./... && go test -race ./internal/awscloud ./internal/labs`
Expected: PASS (`TestCostsGroupsByLabAndSkipsUntaggedSpend` is rewritten for the export).

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(awscloud): billed spend from the free CUR 2.0 export in S3; Cost Explorer removed

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/awscloud internal/labs go.mod go.sum
```

---

### Task 7: The lab account's Terraform — plan role, export, budget

**Files:**
- Modify: `deploy/aws/labs/main.tf`, `deploy/aws/labs/labs.tftest.hcl`

**Interfaces:**
- Produces: `aws_iam_role.plan` (+ policy), `aws_s3_bucket.exports` (+ policy for `bcm-data-exports.amazonaws.com` and `billingreports.amazonaws.com`), `aws_bcmdataexports_export.labs` (CUR 2.0, CSV, GZIP, daily, `OVERWRITE_REPORT`, includes resource tags), `aws_budgets_budget.labs` (monthly COST budget, notifications at 80 % and 100 % ACTUAL to `var.budget_emails`); variables `budget_monthly_usd` (number), `budget_emails` (list(string)); outputs `plan_role_arn`, `export_bucket`, `export_prefix`, `export_name`; the ops policy drops `ce:GetCostAndUsage` and gains `s3:ListBucket`/`s3:GetObject` on the export bucket and `s3:GetObject` on the state bucket's `labs/*`.

- [ ] **Step 1: Write the failing tests** (in `labs.tftest.hcl`, mock providers as the file already does)

```hcl
run "plan_role_reads_only" {
  command = apply
  assert {
    condition = alltrue([for s in jsondecode(aws_iam_role_policy.plan.policy).Statement :
      alltrue([for a in flatten([s.Action]) : can(regex("^[a-z0-9]+:(Describe|Get|List)", a))])])
    error_message = "the plan role may only describe, get and list"
  }
  assert {
    condition     = aws_iam_role.plan.max_session_duration == 3600 && aws_iam_role.plan.permissions_boundary == aws_iam_policy.boundary.arn
    error_message = "the plan role sits inside the boundary"
  }
}

run "ops_reads_the_export_not_cost_explorer" {
  command = apply
  assert {
    condition     = !strcontains(aws_iam_role_policy.ops.policy, "ce:")
    error_message = "no Cost Explorer: it is paid per call"
  }
  assert {
    condition     = strcontains(aws_iam_role_policy.ops.policy, aws_s3_bucket.exports.arn)
    error_message = "the ops role reads the export bucket"
  }
}

run "export_and_budget" {
  command = apply
  variables {
    budget_monthly_usd = 100
    budget_emails      = ["admin@example.com"]
  }
  assert {
    condition     = aws_bcmdataexports_export.labs.export[0].destination_configurations[0].s3_destination[0].s3_bucket == aws_s3_bucket.exports.bucket
    error_message = "the CUR export lands in the export bucket"
  }
  assert {
    condition     = aws_budgets_budget.labs.limit_amount == "100" && length(aws_budgets_budget.labs.notification) == 2
    error_message = "a monthly budget alerting at 80% and 100%"
  }
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd deploy/aws/labs && terraform test`
Expected: FAIL (undeclared resources).

- [ ] **Step 3: Implement**

Add the resources and variables above (`budget_emails` defaults to `[]` → no budget when empty: `count = length(var.budget_emails) > 0 ? 1 : 0`, and adjust the test to index `[0]`). The plan role's trust mirrors the ops role (`aws:PrincipalArn` = the node role); its inline policy allows `ec2:Describe*`, `elasticloadbalancing:Describe*`, `s3:GetBucket*`, `s3:ListAllMyBuckets`, `iam:GetRole` only; attach the existing boundary. The export bucket: versioning off, SSE-S3, public access blocked, TLS-only policy, a 400-day lifecycle, and the bucket policy the Data Exports service needs (`s3:PutObject`, `s3:GetBucketPolicy` for principals `bcm-data-exports.amazonaws.com` and `billingreports.amazonaws.com`, conditioned on `aws:SourceAccount`). The export uses the `COST_AND_USAGE_REPORT` table with `INCLUDE_RESOURCES = TRUE`, query `SELECT * FROM COST_AND_USAGE_REPORT`, format `TEXT_OR_CSV`, compression `GZIP`, `OVERWRITE_REPORT`, frequency `SYNCHRONOUS`.

- [ ] **Step 4: Run the tests**

Run: `cd deploy/aws/labs && terraform fmt -check && terraform validate && terraform test`
Expected: PASS (all old runs too).

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(deploy): plan role, free CUR 2.0 export and an AWS Budget backstop for the lab account; no Cost Explorer

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- deploy/aws/labs
```

---

### Task 8: Wiring, removing infracost, and the CLI

**Files:**
- Modify: `cmd/crucible-api/main.go` (+ `main_test.go`), `cmd/crucible/main.go` (+ `main_test.go`), `internal/awsops/awsops.go` (labs-init outputs → main deploy), `Dockerfile`
- Delete: `internal/infracost/`
- Modify: `deploy/aws/main/{main.tf,variables.tf,main.tftest.hcl,bootstrap.sh.tftpl,terraform.tfvars.example}`, `deploy/helm/crucible/templates/crucible.yaml`, `deploy/helm/test.sh`, `deploy/compose/cluster.yml`, `CLAUDE.md`

**Interfaces:**
- Consumes: everything above.
- Produces: env `CRUCIBLE_AWS_PLAN_ROLE_ARN`, `CRUCIBLE_AWS_EXPORT_BUCKET`, `CRUCIBLE_AWS_EXPORT_PREFIX`, `CRUCIBLE_AWS_EXPORT_NAME` (required with `CRUCIBLE_AWS_LABS=1`); `CRUCIBLE_INFRACOST` and `INFRACOST_API_KEY` gone; dry run prices aws labs from `CRUCIBLE_DEV_LAB_USD_PER_HOUR` (`FixedRates`) and seeds fixture prices so metering works; the terraform binary (and a filesystem mirror of `hashicorp/aws` 6.67.0) in the terraform image used by plan/apply pods.

- [ ] **Step 1: Write the failing tests**

In `cmd/crucible-api/main_test.go`: `TestAWSSetupNeedsThePlanRoleAndExport` (with `CRUCIBLE_AWS_LABS=1`, a missing `CRUCIBLE_AWS_PLAN_ROLE_ARN` or `CRUCIBLE_AWS_EXPORT_BUCKET` refuses to start with a message naming it); `TestDryRunPricesFromDevRates` (dryrun → the aws estimator is `FixedRates`); delete `TestInfracostOffOnlyWithDryrun`. In `cmd/crucible/main_test.go`: replace the price-check tests with `TestLintRefusesUnpricedTypesOffline` (an example lab with `aws_kinesis_stream` and no `prices:` fails `crucible lint` without network or AWS) — this is Task 2's rule surfaced through the CLI.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./cmd/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

1. `cmd/crucible-api/main.go` `awsSetup`: drop infracost lookups and `CRUCIBLE_INFRACOST`; read the new env vars into `awscloud.Config`; build `pricing.Store` and `pricing.Fetcher{Base: "https://pricing.us-east-1.amazonaws.com"}` onto `labs.Service`; return `&labs.TerraformEstimator{Plan: ar, Prices: func(ctx) (pricing.Lookup, error) { return store.Snapshot(ctx) }}`; in dryrun return the dev `FixedRates` and insert fixture price rows (`pricing.Fixture()` — a handful of eu-west-1/us-east-1 rows incl. t3.nano and gp3) with `ok_at = now`.
2. `cmd/crucible/main.go`: remove `priceCheck`, `priceRunner` and the infracost import; lint relies on Task 2's offline check. Help text: `aws.prices`/`aws.usage`.
3. `internal/awsops`: `LabsInit` reads the new outputs (`plan_role_arn`, `export_bucket`, `export_prefix`, `export_name`) and passes them on as it does `lab_role_arn`; accept `--budget-usd` and `--budget-email` flags for labs-init and pass them as `-var`s.
4. Delete `internal/infracost`; `Dockerfile`: remove the infracost install; the terraform image gets a provider filesystem mirror for `hashicorp/aws` 6.67.0 (plan pods run `terraform init -backend=false` against it).
5. `deploy/aws/main`: remove `infracost_api_key` (variable, tfvars example, bootstrap, test) and pass the new env vars; Helm template/test and `cluster.yml`: drop `CRUCIBLE_INFRACOST`/`INFRACOST_API_KEY`, add the new vars where the AWS ones are set (dryrun needs none).
6. `CLAUDE.md`: the money invariant line becomes "aws labs are priced by Crucible from the free AWS Price List (a fresh price refresh is required; stale prices fail closed)"; remove the infracost mention; Commands table unchanged.

- [ ] **Step 4: Run everything this touches**

Run:

```bash
go vet ./... && go test -race ./... \
  && (cd deploy/aws/main && terraform test) && (cd deploy/aws/labs && terraform test) \
  && bash deploy/helm/test.sh && ./bin/crucible lint examples/forge-401
```

Expected: PASS (`make build` first). `git grep -i infracost -- ':!docs'` returns nothing.

- [ ] **Step 5: Commit**

```bash
git rm -r internal/infracost
git commit -m "feat!: no infracost — Crucible prices aws labs itself; plan role and export wired; dry run uses dev rates

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- cmd internal deploy Dockerfile CLAUDE.md go.mod go.sum
```

---

### Task 9: The SPA — the estimate breakdown and metered vs billed

**Files:**
- Modify: `web/src/types.ts`, `web/src/pages/Lab.tsx` (+ test), `web/src/pages/Ledger.tsx` (+ test), `web/src/pages/AdminSettings.tsx` or the Ledger header (price data age), `web/src/theme/app.css`, `.claude/skills/crucible-ui/SKILL.md`
- Modify: `internal/labs/ledger.go` (`LedgerLab.MeteredUSD`, `Ledger.PricesAsOf`, `Ledger.PricesError`)

**Interfaces:**
- Consumes: `ModuleLab.estimate_breakdown` (Task 4), metered spend (Task 5), `price_refresh` (Task 3).
- Produces: Ledger JSON `metered_usd` per lab, `prices_as_of`, `prices_error`; the lab page shows the per-resource breakdown for aws labs; the Ledger shows estimate, metered and billed columns and a "price data from <date>" line (red when stale).

- [ ] **Step 1: Write the failing tests**

Go: in `ledger_test.go`, `TestLedgerShowsMeteredAndPriceAge` (a lab with `lab_resources` rows has `metered_usd`; `prices_as_of` comes from `price_refresh.ok_at`; stale → `prices_error` explains). Web: `Lab.test.tsx` renders the breakdown table (address, type, hourly, note "usage not stated"/"declared"/"free") for an aws lab fixture; `Ledger.test.tsx` renders the three columns and the stale-prices notice.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs -run LedgerShowsMetered && cd web && npx vitest run src/pages/Lab.test.tsx src/pages/Ledger.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement**

Follow `.claude/skills/crucible-ui/SKILL.md`: the breakdown is a `table.grid` inside a `details.lab-settings` titled "How this estimate was worked out" on the lab's request panel; the Ledger adds "Metered" between "Estimate" and "Billed" (rename "Actual" to "Billed" in the UI copy) and a muted line under the title with the price data date. Record the new pattern ("cost breakdown table") in the skill.

- [ ] **Step 4: Run the checks**

Run: `go test ./internal/labs && cd web && npm test && npx tsc -b && npm run lint && npm run build`
Expected: PASS. Screenshot the lab page and Ledger with a mocked API (as in the earlier UI tasks), Forge and High Contrast, 390 px.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(web): an aws lab shows how its estimate was worked out; the Ledger shows metered and billed spend and price age

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src internal/labs/ledger.go internal/labs/ledger_test.go .claude/skills/crucible-ui/SKILL.md
```

---

### Task 10: Docs, the example lab, and the dry-run journey

**Files:**
- Modify: `examples/forge-401` (its aws lab to `runtime: terraform` + `cloud: aws`, with a `usage:` example), `docs/user/authors/lint-and-preview.md`, `docs/user/authors/` (the aws lab page: `prices`, `usage`, `runtime: terraform`), `docs/user/leaders/schedules-budgets-and-caps.md`, `docs/runbooks/aws.md`, `docs/aws-guide.html`, `docs/test-guide.html`, `docs/terraform-vs-crossplane.html`, `docs/superpowers/specs/2026-10-05-crucible-design.md` (§4.5, §9.1, §9.3), `docs/superpowers/plans/2026-10-05-crucible-roadmap.md` (coverage row)
- Modify: `e2e/tests/aws-lab.spec.ts`

- [ ] **Step 1: Docs**

- Runbook: remove infracost and Cost Explorer steps; add: activating the `crucible:lab-id` cost-allocation tag (free, Billing console); `crucible aws labs-init --budget-usd … --budget-email …`; the first CUR export arrives within 24 h; the weekly price refresh and what "price data missing or older than 30 days" means; troubleshooting for an estimate failing (plan errors are shown on the lab page).
- Authors: the lab format (`runtime: terraform`, `cloud: aws`, `region` required, `prices`, `usage`), the list of resource types Crucible prices and the free ones, the per-resource breakdown on the lab page.
- Leaders: budgets use metered spend while a lab runs and billed spend once settled.
- Spec §9.1/§9.3: point to the 2026-10-12 spec; §4.5: `max_hourly_usd` is checked against Crucible's own price.
- `aws-guide.html`, `test-guide.html`, `terraform-vs-crossplane.html`: replace infracost/Cost Explorer wording.
- Roadmap row listing the tests of Tasks 1–9.

- [ ] **Step 2: Example and e2e**

`examples/forge-401`'s aws lab: switch to `runtime: terraform` + `cloud: aws`; add `usage:` if it has an S3 bucket; `./bin/crucible lint examples/forge-401` passes. `e2e/tests/aws-lab.spec.ts` (dry run): keep the journey (estimate → approve → provision → check → destroy → swept → Ledger); assert the Ledger shows a metered figure for the lab after it ran.

- [ ] **Step 3: Run**

Run: `go test ./internal/docs ./internal/content/... && make build && ./bin/crucible lint examples/forge-401`
Then the full check with the user's go-ahead (it wipes the local stack): `KEYCLOAK_PORT=8082 KEEP=1 make cluster-check`.
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git commit -m "docs: free FinOps and runtime terraform for authors, leaders and operators; Forge 401 on runtime terraform

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- docs examples/forge-401 e2e/tests/aws-lab.spec.ts
```
