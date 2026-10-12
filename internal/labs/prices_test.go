package labs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"crucible/internal/pricing"
)

func TestRefreshPricesRecordsAFailureAndKeepsOldRows(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	if err := f.s.RefreshPrices(ctx); err != nil { // unset: nothing to do
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	f.s.Prices = &pricing.Store{DB: f.s.DB, Now: f.s.Now}
	f.s.PriceFetch = &pricing.Fetcher{HTTP: srv.Client(), Base: srv.URL}
	f.s.AWSRegions = []string{"eu-west-1"}
	if err := f.s.RefreshPrices(ctx); err == nil {
		t.Fatal("a failed download is an error")
	}
	var msg string
	if err := f.s.DB.QueryRow(ctx, `SELECT error FROM price_refresh`).Scan(&msg); err != nil || msg == "" {
		t.Fatalf("failure not recorded: %q %v", msg, err)
	}
}
