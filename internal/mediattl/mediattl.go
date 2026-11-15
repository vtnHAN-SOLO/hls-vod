// Package mediattl holds how long signed media URLs live: the api signs
// with these lifetimes, and the transcoder derives from them how long an
// old version of a movie must stay on disk after a retranscode. One source,
// so changing a lifetime cannot leave a URL pointing at removed files.
package mediattl

import "time"

// Lifetimes of signed media URLs. A playback URL must outlast one sitting
// (the player fetches segments for as long as the movie plays); a poster
// URL only has to outlast a catalog page left open.
const (
	PlaybackURLTTL = 6 * time.Hour
	PosterURLTTL   = 24 * time.Hour
)

// ExpiryBuckets says how finely an expiry is rounded: up to ttl/ExpiryBuckets, so every
// response in that window carries the same URL and the browser cache keeps
// working. A URL is therefore valid for at least ttl and at most
// ttl + Step(ttl): 24 h to 25 h for posters, 6 h to 6 h 15 min for playback.
const ExpiryBuckets = 24

// Effective is the lifetime actually used for ttl: devTTL replaces every
// lifetime when it is not 0 (DEV_MEDIA_URL_TTL, local development only).
func Effective(ttl, devTTL time.Duration) time.Duration {
	if devTTL != 0 {
		return devTTL
	}
	return ttl
}

// Step is the rounding of an expiry for a URL of lifetime ttl: ttl /
// ExpiryBuckets in whole seconds, at least one second.
func Step(ttl time.Duration) time.Duration {
	return max((ttl / ExpiryBuckets).Truncate(time.Second), time.Second)
}

// MaxURLLifetime is the longest any signed media URL stays valid: the
// longest lifetime plus its rounding step (25 h with the defaults).
func MaxURLLifetime(devTTL time.Duration) time.Duration {
	var longest time.Duration
	for _, ttl := range []time.Duration{PlaybackURLTTL, PosterURLTTL} {
		e := Effective(ttl, devTTL)
		longest = max(longest, e+Step(e))
	}
	return longest
}

// OldVersionGrace is how long the files of a replaced version stay after a
// retranscode published the new one: every URL signed for the old version
// has expired by then.
func OldVersionGrace(devTTL time.Duration) time.Duration { return MaxURLLifetime(devTTL) }
