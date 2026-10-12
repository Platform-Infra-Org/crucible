package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// offerServer serves testdata/offers in the public Price List layout.
func offerServer(t *testing.T) *httptest.Server {
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
		mux.HandleFunc("/offers/v1.0/aws/"+svc+"/current/eu-west-1/index.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(body) })
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
