package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxOffer bounds one offer download; real regional EC2 files are several GB for us-east-1.
const maxOffer = 8 << 30

// Fetcher downloads on-demand list prices from the free public AWS Price List (no keys).
type Fetcher struct {
	HTTP *http.Client // nil: a client with a 30-minute timeout
	Base string       // https://pricing.us-east-1.amazonaws.com
}

type product struct {
	ProductFamily string            `json:"productFamily"`
	Attributes    map[string]string `json:"attributes"`
}

type matcher func(p product) (key string, ok bool)

func matchEC2(p product) (string, bool) {
	a := p.Attributes
	if a["locationType"] != "AWS Region" { // not Local Zones, Wavelength or Outposts
		return "", false
	}
	switch p.ProductFamily {
	case "Compute Instance":
		if a["operatingSystem"] == "Linux" && a["tenancy"] == "Shared" && a["preInstalledSw"] == "NA" &&
			a["capacitystatus"] == "Used" && a["licenseModel"] == "No License required" && a["instanceType"] != "" {
			return "ec2:" + a["instanceType"], true
		}
	case "Storage":
		if a["volumeApiName"] != "" {
			return "ebs:" + a["volumeApiName"], true
		}
	case "NAT Gateway":
		switch {
		case strings.HasSuffix(a["usagetype"], "NatGateway-Hours"):
			return "nat:hour", true
		case strings.HasSuffix(a["usagetype"], "NatGateway-Bytes"):
			return "nat:gb", true
		}
	}
	return "", false
}

func matchVPC(p product) (string, bool) {
	return "ipv4:hour", strings.HasSuffix(p.Attributes["usagetype"], "PublicIPv4:InUseAddress")
}

func matchELB(p product) (string, bool) {
	if p.Attributes["locationType"] != "AWS Region" {
		return "", false
	}
	if !strings.HasSuffix(p.Attributes["usagetype"], "LoadBalancerUsage") {
		return "", false
	}
	switch p.ProductFamily {
	case "Load Balancer-Application":
		return "elb:application", true
	case "Load Balancer-Network":
		return "elb:network", true
	}
	return "", false
}

func matchS3(p product) (string, bool) {
	a := p.Attributes
	return "s3:standard", a["locationType"] == "AWS Region" && p.ProductFamily == "Storage" && a["storageClass"] == "General Purpose" && a["volumeType"] == "Standard"
}

var services = []struct {
	name  string
	match matcher
}{{"AmazonEC2", matchEC2}, {"AmazonVPC", matchVPC}, {"AWSELB", matchELB}, {"AmazonS3", matchS3}}

// AWS returns the prices labs use for each region. Any failed download or unknown region fails the whole fetch.
func (f *Fetcher) AWS(ctx context.Context, regions []string) ([]Row, error) {
	f = &Fetcher{HTTP: f.HTTP, Base: f.Base} // never write to the shared fetcher
	if f.HTTP == nil {
		f.HTTP = &http.Client{Timeout: 30 * time.Minute}
	}
	var idx struct {
		Offers map[string]struct {
			URL string `json:"currentRegionIndexUrl"`
		} `json:"offers"`
	}
	if err := f.getJSON(ctx, "/offers/v1.0/aws/index.json", &idx); err != nil {
		return nil, err
	}
	var out []Row
	for _, svc := range services {
		o, ok := idx.Offers[svc.name]
		if !ok {
			return nil, fmt.Errorf("price list has no %s offer", svc.name)
		}
		var ri struct {
			Regions map[string]struct {
				URL string `json:"currentVersionUrl"`
			} `json:"regions"`
		}
		if err := f.getJSON(ctx, o.URL, &ri); err != nil {
			return nil, err
		}
		for _, region := range regions {
			r, ok := ri.Regions[region]
			if !ok {
				return nil, fmt.Errorf("price list %s has no region %s", svc.name, region)
			}
			rows, err := f.offer(ctx, r.URL, svc.match)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", svc.name, region, err)
			}
			for _, row := range rows {
				out = append(out, Row{Service: "aws", Region: region, Key: row.key, Unit: row.unit, USD: row.usd})
			}
		}
	}
	return out, nil
}

func (f *Fetcher) get(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(f.Base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return resp.Body, nil
}

func (f *Fetcher) getJSON(ctx context.Context, path string, v any) error {
	b, err := f.get(ctx, path)
	if err != nil {
		return err
	}
	defer b.Close()
	return json.NewDecoder(io.LimitReader(b, 64<<20)).Decode(v)
}

func (f *Fetcher) offer(ctx context.Context, path string, m matcher) ([]offerRow, error) {
	b, err := f.get(ctx, path)
	if err != nil {
		return nil, err
	}
	defer b.Close()
	return parseOffer(&capReader{r: b, left: maxOffer}, m)
}

type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errors.New("offer file larger than 8 GiB")
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

type offerRow struct {
	key, unit string
	usd       float64
}

type dimension struct {
	Unit       string            `json:"unit"`
	BeginRange string            `json:"beginRange"`
	Price      map[string]string `json:"pricePerUnit"`
}

// parseOffer streams an offer file: the products pass keeps the matching SKUs, then terms.OnDemand yields their
// first-tier price. Nothing else is held in memory (Reserved terms are skipped token by token).
func parseOffer(r io.Reader, m matcher) ([]offerRow, error) {
	dec := json.NewDecoder(r)
	want := map[string]string{} // sku -> key
	var rows []offerRow
	if err := enter(dec, '{'); err != nil {
		return nil, err
	}
	for dec.More() {
		name, err := keyName(dec)
		if err != nil {
			return nil, err
		}
		switch name {
		case "products":
			err = each(dec, func(sku string) error {
				var p product
				if err := dec.Decode(&p); err != nil {
					return err
				}
				if k, ok := m(p); ok {
					want[sku] = k
				}
				return nil
			})
		case "terms":
			err = each(dec, func(kind string) error {
				if kind != "OnDemand" {
					return skip(dec)
				}
				return each(dec, func(sku string) error {
					key, ok := want[sku]
					if !ok {
						return skip(dec)
					}
					var offers map[string]struct {
						Dims map[string]dimension `json:"priceDimensions"`
					}
					if err := dec.Decode(&offers); err != nil {
						return err
					}
					for _, o := range offers {
						for _, d := range o.Dims {
							if d.BeginRange != "0" && d.BeginRange != "" {
								continue
							}
							usd, err := strconv.ParseFloat(d.Price["USD"], 64)
							if err != nil || !(usd > 0) || math.IsInf(usd, 0) || !unitOK(key, d.Unit) { // !(>0) also drops NaN
								continue
							}
							rows = append(rows, offerRow{key, d.Unit, usd})
						}
					}
					return nil
				})
			})
		default:
			err = skip(dec)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := enter(dec, '}'); err != nil {
		return nil, err
	}
	// ponytail: a SKU duplicated across product variants keeps the highest price (the conservative estimate)
	best := map[string]offerRow{}
	for _, r := range rows {
		if b, ok := best[r.key]; !ok || r.usd > b.usd {
			best[r.key] = r
		}
	}
	if len(best) == 0 {
		return nil, errors.New("offer file holds no usable prices")
	}
	out := make([]offerRow, 0, len(best))
	for _, r := range best {
		out = append(out, r)
	}
	return out, nil
}

func enter(dec *json.Decoder, want json.Delim) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != want {
		return fmt.Errorf("unexpected %v in offer file", t)
	}
	return nil
}

func keyName(dec *json.Decoder) (string, error) {
	t, err := dec.Token()
	if err != nil {
		return "", err
	}
	s, ok := t.(string)
	if !ok {
		return "", fmt.Errorf("unexpected %v in offer file", t)
	}
	return s, nil
}

// each walks an object, calling fn after each key with the decoder at the value (fn must consume it).
func each(dec *json.Decoder, fn func(key string) error) error {
	if err := enter(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		k, err := keyName(dec)
		if err != nil {
			return err
		}
		if err := fn(k); err != nil {
			return err
		}
	}
	return enter(dec, '}')
}

// skip consumes one value without keeping it.
func skip(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := t.(json.Delim)
	if !ok || d == '}' || d == ']' {
		return nil
	}
	for depth := 1; depth > 0; {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

// unitOK: each key has one unit; a dimension in another (an upfront fee's "Quantity", "LCU-Hrs") is not its price.
func unitOK(key, unit string) bool {
	switch {
	case key == "nat:gb":
		return unit == "GB"
	case strings.HasPrefix(key, "ebs:") || key == "s3:standard":
		return unit == "GB-Mo"
	}
	return unit == "Hrs"
}
