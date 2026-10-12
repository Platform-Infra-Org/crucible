package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
)

// offerServer serves testdata/offers in the public Price List layout.
func offerServer(t *testing.T, failOffer ...string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	idx := `{"offers":{`
	for i, svc := range []string{"AmazonEC2", "AmazonVPC", "AWSELB", "AmazonS3"} {
		if i > 0 {
			idx += ","
		}
		idx += `"` + svc + `":{"currentRegionIndexUrl":"/offers/v1.0/aws/` + svc + `/current/region_index.json"}`
		body, err := os.ReadFile("testdata/offers/" + svc + ".json")
		if err != nil {
			t.Fatal(err)
		}
		mux.HandleFunc("/offers/v1.0/aws/"+svc+"/current/region_index.json", func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"regions":{"eu-west-1":{"currentVersionUrl":"/offers/v1.0/aws/` + svc + `/current/eu-west-1/index.json"}}}`))
		})
		mux.HandleFunc("/offers/v1.0/aws/"+svc+"/current/eu-west-1/index.json", func(w http.ResponseWriter, _ *http.Request) {
			if slices.Contains(failOffer, svc) {
				w.WriteHeader(500)
				return
			}
			w.Write(body)
		})
	}
	idx += `}}`
	mux.HandleFunc("/offers/v1.0/aws/index.json", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(idx)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchKeepsOnlyWhatLabsUse(t *testing.T) {
	srv := offerServer(t)
	rows, err := (&Fetcher{HTTP: srv.Client(), Base: srv.URL}).AWS(context.Background(), []string{"eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, r := range rows {
		got[r.Key] = r.USD
		if r.Service != "aws" || r.Region != "eu-west-1" || r.Unit == "" {
			t.Errorf("bad row %+v", r)
		}
	}
	for k, v := range map[string]float64{"ec2:t3.micro": 0.0114, "ebs:gp3": 0.088, "nat:hour": 0.048, "nat:gb": 0.048,
		"ipv4:hour": 0.005, "elb:application": 0.0252, "elb:network": 0.0252, "s3:standard": 0.023} {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != 8 {
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

func TestFetchFailsOnATruncatedOfferAndAnUnknownRegion(t *testing.T) {
	srv := offerServer(t)
	f := &Fetcher{HTTP: srv.Client(), Base: srv.URL}
	if _, err := f.AWS(context.Background(), []string{"mars-1"}); err == nil || !strings.Contains(err.Error(), "mars-1") {
		t.Fatalf("unknown region: %v", err)
	}
	if _, err := parseOffer(strings.NewReader(`{"products":{"E1":{"productFamily":"Comp`), matchEC2); err == nil {
		t.Fatal("truncated offer must be an error")
	}
}

func TestFetchFailsWhenALaterServiceFails(t *testing.T) {
	srv := offerServer(t, "AWSELB")
	if rows, err := (&Fetcher{HTTP: srv.Client(), Base: srv.URL}).AWS(context.Background(), []string{"eu-west-1"}); err == nil {
		t.Fatalf("a partial fetch must not be returned: %v", rows)
	}
}

func offerDoc(usd, unit string) string {
	return `{"products":{"E1":{"productFamily":"Compute Instance","attributes":{"instanceType":"t3.micro","operatingSystem":"Linux",` +
		`"tenancy":"Shared","preInstalledSw":"NA","capacitystatus":"Used","licenseModel":"No License required","locationType":"AWS Region"}}},` +
		`"terms":{"OnDemand":{"E1":{"E1.T":{"priceDimensions":{"d":{"unit":"` + unit + `","beginRange":"0","pricePerUnit":{"USD":"` + usd + `"}}}}}}}}`
}

func TestUnusablePricesAreDropped(t *testing.T) {
	for _, usd := range []string{"0.0000000000", "NaN", "Inf", "-1"} {
		if rows, err := parseOffer(strings.NewReader(offerDoc(usd, "Hrs")), matchEC2); err == nil {
			t.Errorf("%q stored: %v", usd, rows)
		}
	}
	if rows, err := parseOffer(strings.NewReader(offerDoc("0.0114", "Hrs")), matchEC2); err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
}

func TestOnlyTheKeysUnitIsKept(t *testing.T) {
	if rows, err := parseOffer(strings.NewReader(offerDoc("0.0114", "Quantity")), matchEC2); err == nil {
		t.Fatalf("a Quantity price is not an hourly price: %v", rows)
	}
	for key, unit := range map[string]string{"ec2:x": "Hrs", "nat:hour": "Hrs", "ipv4:hour": "Hrs", "elb:network": "Hrs",
		"ebs:gp3": "GB-Mo", "s3:standard": "GB-Mo", "nat:gb": "GB"} {
		if !unitOK(key, unit) || unitOK(key, "Quantity") {
			t.Errorf("%s %s", key, unit)
		}
	}
}
