package pricing

// Fixture is a small set of list prices for dry runs (CRUCIBLE_AWS_LABS=dryrun), which never download the price list:
// enough for what the fake apply leaves in state (awscloud.Fake.SimulateApply: a t3.nano with a gp3 root and a
// bucket) and the common lab resources, in eu-west-1 and us-east-1. Approximate on-demand prices; development only.
func Fixture() []Row {
	var rows []Row
	for region, p := range map[string][10]float64{
		// t3.nano, t3.micro, gp3, gp2, ipv4, nat hour, nat GB, ALB, NLB, S3 standard
		"eu-west-1": {0.0057, 0.0114, 0.088, 0.11, 0.005, 0.048, 0.048, 0.0252, 0.0252, 0.023},
		"us-east-1": {0.0052, 0.0104, 0.08, 0.10, 0.005, 0.045, 0.045, 0.0225, 0.0225, 0.023},
	} {
		for i, k := range [...]struct{ key, unit string }{{"ec2:t3.nano", "Hrs"}, {"ec2:t3.micro", "Hrs"}, {"ebs:gp3", "GB-Mo"},
			{"ebs:gp2", "GB-Mo"}, {"ipv4:hour", "Hrs"}, {"nat:hour", "Hrs"}, {"nat:gb", "GB"}, {"elb:application", "Hrs"},
			{"elb:network", "Hrs"}, {"s3:standard", "GB-Mo"}} {
			rows = append(rows, Row{Service: "aws", Region: region, Key: k.key, Unit: k.unit, USD: p[i]})
		}
	}
	return rows
}
