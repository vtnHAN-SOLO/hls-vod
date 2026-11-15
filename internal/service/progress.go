package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// Watch progress rules.
const (
	// FinishedPercent: a movie watched to at least this share of its
	// runtime is finished. It leaves continue-watching and playback starts
	// it from the beginning again.
	FinishedPercent = 95
	// ContinueWatchingLimit caps the continue-watching row.
	ContinueWatchingLimit = 20
	// MaxPositionSec bounds a position when the movie's runtime is unknown
	// (one week), so it always fits the integer column.
	MaxPositionSec = 7 * 24 * 3600
)

// SaveProgress records where the caller is in a movie. The user is always
// the caller (v), never a value from the request. Only a ready movie takes
// progress: for a non-admin any other movie does not exist (ErrNotFound),
// as in GetMovie; an admin gets a *NotReadyError. The position must lie
// within the movie's runtime.
func (c *Catalog) SaveProgress(ctx context.Context, v Principal, movieID string, positionSec int64) error {
	if v.UserID == "" {
		return ErrUnauthenticated
	}
	m, err := c.visibleMovie(ctx, v, movieID)
	if err != nil {
		return err
	}
	if m.Status != model.StatusReady {
		return &NotReadyError{Status: m.Status}
	}
	limit := int64(MaxPositionSec)
	if m.RuntimeSec != nil {
		limit = int64(*m.RuntimeSec)
	}
	if positionSec < 0 || positionSec > limit {
		return &ValidationError{"position_sec", fmt.Sprintf("must be between 0 and %d", limit)}
	}
	err = c.store.SaveProgress(ctx, v.UserID, m.ID, int(positionSec))
	if errors.Is(err, store.ErrProgressRefused) {
		// The movie stopped being ready since visibleMovie looked.
		return ErrNotFound
	}
	return err
}

// ContinueWatching returns the caller's unfinished ready movies, most
// recently watched first.
func (c *Catalog) ContinueWatching(ctx context.Context, v Principal) ([]model.Progress, error) {
	if v.UserID == "" {
		return nil, ErrUnauthenticated
	}
	return c.store.ContinueWatching(ctx, v.UserID, FinishedPercent, ContinueWatchingLimit)
}

// resumeAt is the caller's saved position in m, or nil when there is none
// or the movie is finished.
func (c *Catalog) resumeAt(ctx context.Context, v Principal, m model.Movie) (*int, error) {
	if v.UserID == "" {
		return nil, nil
	}
	pos, ok, err := c.store.GetProgress(ctx, v.UserID, m.ID)
	if err != nil || !ok || pos <= 0 || Finished(pos, m.RuntimeSec) {
		return nil, err
	}
	return &pos, nil
}

// Finished reports whether positionSec is at least FinishedPercent of
// runtimeSec. An unknown runtime is never finished.
func Finished(positionSec int, runtimeSec *int) bool {
	return runtimeSec != nil && int64(positionSec)*100 >= int64(*runtimeSec)*FinishedPercent
}
