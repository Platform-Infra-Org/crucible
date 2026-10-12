package pricing

import (
	"errors"
	"fmt"
	"slices"
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
	"aws_network_interface": true, "aws_eip_association": true, "aws_volume_attachment": true,
	"random_id": true, "random_string": true, "random_password": true, "random_pet": true, "random_integer": true,
	"tls_private_key": true, "terraform_data": true, "null_resource": true, "time_sleep": true,
}

// pricedAWS are the types a pricer below handles.
var pricedAWS = []string{"aws_instance", "aws_ebs_volume", "aws_nat_gateway", "aws_eip", "aws_lb", "aws_alb", "aws_s3_bucket"}

// KnownAWS: Crucible can price this type (or it's free) without a declaration.
func KnownAWS(t string) bool { return freeAWS[t] || slices.Contains(pricedAWS, t) }

// PriceAWS prices resources in one region. A declared price never lowers Crucible's own: a type Crucible prices (or
// lists as free) costs the higher of the two, and the declared price alone only where Crucible's pricer fails closed on
// a setting (provisioned IOPS or throughput, dedicated tenancy, a size or type unknown at plan time). A price missing
// from the list (a region or size the refresh didn't fetch) is never covered: a declared aws_instance would stand in for
// the instance, its volumes and its IPv4. A type Crucible can't price costs its declared price; anything else unknown
// is an error.
func PriceAWS(res []Resource, region string, declared map[string]float64, usage map[string]map[string]float64, l Lookup) (Breakdown, error) {
	var b Breakdown
	var missing []string
	get := func(key string) (float64, error) {
		if v, ok := l.Price("aws", region, key); ok {
			return v, nil
		}
		return 0, fmt.Errorf("no aws price for %s in %s (%w)", key, region, errNoListPrice)
	}
	for _, r := range res {
		// instances in a dedicated VPC are dedicated; only a declared aws_instance price covers that (aws_vpc's own never does)
		if _, ok := declared["aws_instance"]; r.Type == "aws_vpc" && !ok && str(r.Values, "instance_tenancy") != "" && str(r.Values, "instance_tenancy") != "default" {
			return b, fmt.Errorf("%s: only shared tenancy is priced (dedicated VPC); declare aws_instance under aws.prices", r.Address)
		}
		line, err := priceOwn(get, r, usageFor(usage, r.Address))
		if p, ok := declared[r.Type]; ok && !errors.Is(err, errNoListPrice) && (err != nil || p > line.HourlyUSD+line.UsageHourlyUSD) {
			line, err = Line{Address: r.Address, Type: r.Type, HourlyUSD: p, Note: "declared"}, nil
		}
		if errors.Is(err, errNoPricer) {
			missing = append(missing, r.Type)
			continue
		}
		if err != nil {
			return b, err
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

var (
	errNoPricer    = errors.New("no pricer")
	errNoListPrice = errors.New("price data missing or out of date")
)

// priceOwn prices one resource from the list prices, or fails closed.
func priceOwn(get func(string) (float64, error), r Resource, u map[string]float64) (Line, error) {
	line := Line{Address: r.Address, Type: r.Type}
	switch {
	case freeAWS[r.Type]:
		line.Note = "free"
	case r.Type == "aws_instance":
		if t := str(r.Values, "tenancy"); (t != "" && t != "default") || str(r.Values, "host_id") != "" {
			return line, fmt.Errorf("%s: only shared tenancy is priced; declare aws_instance under aws.prices", r.Address)
		}
		it := str(r.Values, "instance_type")
		if it == "" {
			return line, fmt.Errorf("%s: instance_type unknown at plan time; set it literally or declare a price for aws_instance", r.Address)
		}
		p, err := get("ec2:" + it)
		if err != nil {
			return line, err
		}
		line.HourlyUSD = p
		rb := first(r.Values, "root_block_device")
		if rb == nil {
			rb = map[string]any{} // the root volume defaults to 8 GiB gp3; metering corrects it from the state
		}
		size := num(rb, "volume_size")
		if size == 0 {
			size = 8
		}
		gbp, err := ebsGB(get, r.Address, rb, "aws_instance")
		if err != nil {
			return line, err
		}
		line.HourlyUSD += size * gbp / HoursPerMonth
		if r.Unknown["ebs_block_device"] == true {
			return line, fmt.Errorf("%s: ebs_block_device is unknown until apply; set it literally or declare a price for aws_instance", r.Address)
		}
		ebs, _ := r.Values["ebs_block_device"].([]any)
		for _, e := range ebs { // inline volumes: no defaults, an unknown size or type fails closed
			d, _ := e.(map[string]any)
			if num(d, "volume_size") <= 0 || str(d, "volume_type") == "" {
				return line, fmt.Errorf("%s: an ebs_block_device's volume_size or volume_type is unknown at plan time; set both or declare a price for aws_instance", r.Address)
			}
			gbp, err := ebsGB(get, r.Address, d, "aws_instance")
			if err != nil {
				return line, err
			}
			line.HourlyUSD += num(d, "volume_size") * gbp / HoursPerMonth
		}
		if v, ok := r.Values["associate_public_ip_address"].(bool); v || !ok { // null or unknown counts: over-estimating is the safe side
			ip, err := get("ipv4:hour")
			if err != nil {
				return line, err
			}
			line.HourlyUSD += ip
		}
	case r.Type == "aws_ebs_volume":
		size := num(r.Values, "size")
		if size <= 0 {
			return line, fmt.Errorf("%s: size unknown at plan time; set size or declare a price for aws_ebs_volume", r.Address)
		}
		gb, err := ebsGB(get, r.Address, r.Values, "aws_ebs_volume")
		if err != nil {
			return line, err
		}
		line.HourlyUSD = size * gb / HoursPerMonth
	case r.Type == "aws_nat_gateway":
		h, err := get("nat:hour")
		if err != nil {
			return line, err
		}
		line.HourlyUSD = h
		if str(r.Values, "connectivity_type") != "private" && str(r.Values, "allocation_id") == "" && r.Unknown["allocation_id"] != true { // with an allocation_id the aws_eip line counts the IP
			ip, err := get("ipv4:hour")
			if err != nil {
				return line, err
			}
			line.HourlyUSD += ip
		}
		if gbs, ok := u["gb_processed"]; ok {
			gb, err := get("nat:gb")
			if err != nil {
				return line, err
			}
			line.UsageHourlyUSD = gbs * gb / HoursPerMonth
		} else {
			line.Note = "usage not stated"
		}
	case r.Type == "aws_eip":
		ip, err := get("ipv4:hour")
		if err != nil {
			return line, err
		}
		line.HourlyUSD = ip
	case r.Type == "aws_lb" || r.Type == "aws_alb":
		kind := str(r.Values, "load_balancer_type")
		if kind == "" {
			kind = "application"
		}
		h, err := get("elb:" + kind)
		if err != nil {
			return line, err
		}
		line.HourlyUSD = h
		line.Note = "LCU usage not priced"
		if internal, _ := r.Values["internal"].(bool); !internal {
			ip, err := get("ipv4:hour")
			if err != nil {
				return line, err
			}
			subnets := 2 // at least two when unknown
			for _, k := range []string{"subnets", "subnet_mapping"} {
				if l, ok := r.Values[k].([]any); ok && len(l) > subnets {
					subnets = len(l)
				}
			}
			line.HourlyUSD += float64(subnets) * ip
		}
	case r.Type == "aws_s3_bucket":
		if gbs, ok := u["storage_gb"]; ok {
			gb, err := get("s3:standard")
			if err != nil {
				return line, err
			}
			line.UsageHourlyUSD = gbs * gb / HoursPerMonth
		} else {
			line.Note = "usage not stated"
		}
	default:
		return line, errNoPricer
	}
	return line, nil
}

// ebsGB returns the GB-month price of a volume (or root_block_device) block. Provisioned performance isn't priced,
// so io1/io2 and gp3 above its included 3000 IOPS / 125 MB/s fail closed unless the lab declares a price.
func ebsGB(get func(string) (float64, error), addr string, v map[string]any, declare string) (float64, error) {
	vt := str(v, "type")
	if vt == "" {
		vt = str(v, "volume_type")
	}
	if vt == "" {
		vt = "gp3"
	}
	if vt == "io1" || vt == "io2" || (vt == "gp3" && (num(v, "iops") > 3000 || num(v, "throughput") > 125)) {
		return 0, fmt.Errorf("%s: provisioned IOPS or throughput on %s isn't priced; declare %s under aws.prices", addr, vt, declare)
	}
	return get("ebs:" + vt)
}
