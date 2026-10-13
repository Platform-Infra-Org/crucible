package labs

import (
	"context"
	"time"
)

// RefreshPrices downloads the allowed regions' list prices and replaces the stored ones. A failure keeps the old rows,
// records why for admins, and is returned so the job retries; stale data fails estimates closed. A no-op when unset.
func (s *Service) RefreshPrices(ctx context.Context) error {
	if s.Prices == nil || s.PriceFetch == nil {
		return nil
	}
	// The job runs on every start; the download is large, so a restart within the week leaves fresh prices alone.
	if fresh, err := s.Prices.FreshWithin(ctx, 6*24*time.Hour); err != nil || fresh {
		return err
	}
	rows, err := s.PriceFetch.AWS(ctx, s.AWSRegions)
	if err == nil {
		err = s.Prices.Replace(ctx, rows)
	}
	if err != nil {
		if ferr := s.Prices.Failed(ctx, err); ferr != nil {
			s.Log.Error("recording a price refresh failure", "err", ferr)
		}
		return err
	}
	return nil
}
