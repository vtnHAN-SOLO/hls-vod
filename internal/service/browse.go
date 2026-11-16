package service

import (
	"context"
	"fmt"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// Browsing: the home page's spotlight and the most-popular ranking.
const (
	// DefaultPopularLimit and MaxPopularLimit bound ?limit= of the ranking.
	DefaultPopularLimit = 10
	MaxPopularLimit     = 20
	// FeaturedLimit is how many featured movies the spotlight rotates.
	FeaturedLimit = 10
	// FeaturedFallback is how many of the newest ready movies stand in when
	// none is featured.
	FeaturedFallback = 5
)

// popularWindows maps ?window= to how far back a progress save counts; 0 is
// ever.
var popularWindows = map[string]time.Duration{
	"week":  7 * 24 * time.Hour,
	"month": 30 * 24 * time.Hour,
	"all":   0,
}

// Popular ranks published movies by the distinct users who saved watch progress
// in them within the window ("week" (the default), "month" or "all"), at most
// limit (1..MaxPopularLimit). Movies nobody watched in the window are not
// ranked.
func (c *Catalog) Popular(ctx context.Context, _ Principal, window string, limit int64) ([]model.Popular, error) {
	if window == "" {
		window = "week"
	}
	back, ok := popularWindows[window]
	if !ok {
		return nil, &ValidationError{"window", "must be one of week, month, all"}
	}
	if limit < 1 || limit > MaxPopularLimit {
		return nil, &ValidationError{"limit", fmt.Sprintf("must be between 1 and %d", MaxPopularLimit)}
	}
	var since *time.Time
	if back > 0 {
		t := c.now().Add(-back)
		since = &t
	}
	return c.store.PopularMovies(ctx, since, int(limit))
}

// Featured is what the home page's spotlight rotates: the featured (so
// published) movies, newest featured first; with none, the FeaturedFallback
// newest published movies. featured reports which of the two it is.
func (c *Catalog) Featured(ctx context.Context, _ Principal) (movies []model.Movie, featured bool, err error) {
	ms, err := c.store.FeaturedMovies(ctx, FeaturedLimit)
	if err != nil || len(ms) > 0 {
		return ms, len(ms) > 0, err
	}
	ready := model.StatusReady
	ms, _, err = c.store.ListMovies(ctx, store.MovieFilter{
		Status: &ready, PublishedOnly: true, Sort: store.SortAddedDesc, Limit: FeaturedFallback,
	})
	return ms, false, err
}
