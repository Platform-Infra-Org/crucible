package pricing

import (
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
