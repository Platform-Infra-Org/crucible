package labs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"crucible/internal/jobs"
	"crucible/internal/pricing"
)

const miniEC2 = `{"products":{"E1":{"productFamily":"Compute Instance","attributes":{"instanceType":"t3.micro","operatingSystem":"Linux",` +
	`"tenancy":"Shared","preInstalledSw":"NA","capacitystatus":"Used","licenseModel":"No License required","locationType":"AWS Region"}}},` +
	`"terms":{"OnDemand":{"E1":{"E1.T":{"priceDimensions":{"d":{"unit":"Hrs","beginRange":"0","pricePerUnit":{"USD":"0.0114"}}}}}}}}`

// priceServer: with ec2OK the first service downloads and the later ones return 500; otherwise everything does.
func priceServer(t *testing.T, ec2OK bool) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	idx := `{"offers":{`
	for i, svc := range []string{"AmazonEC2", "AmazonVPC", "AWSELB", "AmazonS3"} {
		if i > 0 {
			idx += ","
		}
		idx += `"` + svc + `":{"currentRegionIndexUrl":"/` + svc + `/ri.json"}`
		mux.HandleFunc("/"+svc+"/ri.json", func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"regions":{"eu-west-1":{"currentVersionUrl":"/` + svc + `/offer.json"}}}`))
		})
		mux.HandleFunc("/"+svc+"/offer.json", func(w http.ResponseWriter, _ *http.Request) {
			if svc == "AmazonEC2" && ec2OK {
				w.Write([]byte(miniEC2))
				return
			}
			w.WriteHeader(500)
		})
	}
	mux.HandleFunc("/offers/v1.0/aws/index.json", func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.Write([]byte(idx + `}}`)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func seedPrices(t *testing.T, f *fx) (okAt time.Time) {
	t.Helper()
	f.s.Prices = &pricing.Store{DB: f.s.DB, Now: f.s.Now}
	f.s.AWSRegions = []string{"eu-west-1"}
	if err := f.s.Prices.Replace(context.Background(), []pricing.Row{{Service: "aws", Region: "eu-west-1", Key: "nat:hour", Unit: "Hrs", USD: 0.048}}); err != nil {
		t.Fatal(err)
	}
	return f.clk.Now()
}

func checkKept(t *testing.T, f *fx, okAt time.Time) {
	t.Helper()
	var n int
	var got time.Time
	var msg string
	if err := f.s.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM prices WHERE key = 'nat:hour'), ok_at, error FROM price_refresh`).Scan(&n, &got, &msg); err != nil {
		t.Fatal(err)
	}
	if n != 1 || !got.Equal(okAt) || msg == "" {
		t.Fatalf("old rows must stay and the failure be recorded: rows=%d ok_at=%v want %v error=%q", n, got, okAt, msg)
	}
}

func TestRefreshPricesRecordsAFailureAndKeepsOldRows(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	if err := f.s.RefreshPrices(ctx); err != nil { // unset: nothing to do
		t.Fatal(err)
	}
	okAt := seedPrices(t, f)
	srv, _ := priceServer(t, false)
	f.s.PriceFetch = &pricing.Fetcher{HTTP: srv.Client(), Base: srv.URL}
	f.clk.Add(7 * 24 * time.Hour)
	if err := f.s.RefreshPrices(ctx); err == nil {
		t.Fatal("a failed download is an error")
	}
	checkKept(t, f, okAt)
}

func TestRefreshPricesKeepsOldRowsWhenALaterServiceFails(t *testing.T) {
	f := setup(t, true)
	okAt := seedPrices(t, f)
	srv, _ := priceServer(t, true)
	f.s.PriceFetch = &pricing.Fetcher{HTTP: srv.Client(), Base: srv.URL}
	f.clk.Add(7 * 24 * time.Hour)
	if err := f.s.RefreshPrices(context.Background()); err == nil {
		t.Fatal("a partial download is an error")
	}
	checkKept(t, f, okAt)
}

func TestRefreshPricesSkipsWhenFresh(t *testing.T) {
	f := setup(t, true)
	seedPrices(t, f)
	srv, hits := priceServer(t, false)
	f.s.PriceFetch = &pricing.Fetcher{HTTP: srv.Client(), Base: srv.URL}
	f.clk.Add(5 * 24 * time.Hour)
	if err := f.s.RefreshPrices(context.Background()); err != nil || hits.Load() != 0 {
		t.Fatalf("a restart within 6 days must not download again: %v hits=%d", err, hits.Load())
	}
	f.clk.Add(2 * 24 * time.Hour)
	if err := f.s.RefreshPrices(context.Background()); err == nil || hits.Load() == 0 {
		t.Fatalf("after 6 days it downloads: %v hits=%d", err, hits.Load())
	}
}

// A database a dry run seeded with fixture prices, switched to real mode: the refresh downloads at once.
func TestRefreshPricesReplacesDryRunFixture(t *testing.T) {
	f := setup(t, true)
	dry := &pricing.Store{DB: f.s.DB, Now: f.s.Now, Source: "fixture"}
	if err := dry.Replace(context.Background(), pricing.Fixture()); err != nil {
		t.Fatal(err)
	}
	f.s.Prices = &pricing.Store{DB: f.s.DB, Now: f.s.Now}
	f.s.AWSRegions = []string{"eu-west-1"}
	srv, hits := priceServer(t, false)
	f.s.PriceFetch = &pricing.Fetcher{HTTP: srv.Client(), Base: srv.URL}
	if err := f.s.RefreshPrices(context.Background()); err == nil || hits.Load() == 0 {
		t.Fatalf("fixture prices must not pass for a fresh real refresh: %v hits=%d", err, hits.Load())
	}
}

func TestThePriceJobIsNotRescuedMidRun(t *testing.T) {
	if jobs.RescueStuckAfter <= (&PriceWorker{}).Timeout(nil) {
		t.Fatalf("River rescues a job running %v while the price download may run %v", jobs.RescueStuckAfter, (&PriceWorker{}).Timeout(nil))
	}
}
