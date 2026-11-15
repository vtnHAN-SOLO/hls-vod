package auth

import (
	"sync"
	"time"
)

// Login throttle settings: 5 failures within the window block the key for
// the backoff.
const (
	MaxLoginFailures = 5
	LoginBackoff     = 15 * time.Minute
	// maxThrottleKeys bounds memory: past it, expired entries are dropped,
	// and if none have expired an arbitrary idle entry is evicted.
	maxThrottleKeys = 100_000
)

// InFlightRetry is the Retry-After for a key refused because its in-flight
// attempts already fill what is left of its limit: one of them finishing
// decides whether the key is blocked or free again, so the wait is short.
const InFlightRetry = time.Second

// Throttle counts failures per key (username + client IP for logins), in
// memory. It is per process: with 2 api replicas an attacker gets up to 2x
// the limit, which is accepted at this scale.
//
// Every attempt is reserved by Allow before the work that decides it (the
// password hash) and counts against the limit while it runs, so a burst of
// parallel wrong passwords cannot all pass Allow before the first failure is
// recorded: at most limit − failures attempts are in flight at once.
type Throttle struct {
	mu      sync.Mutex
	entries map[string]*throttleEntry
	now     func() time.Time
	max     int
	window  time.Duration
}

type throttleEntry struct {
	failures     int
	inFlight     int
	firstFailure time.Time
	blockedUntil time.Time
}

// NewThrottle returns an empty login throttle: MaxLoginFailures within
// LoginBackoff block a key for LoginBackoff.
func NewThrottle() *Throttle { return NewLimiter(MaxLoginFailures, LoginBackoff) }

// NewLimiter returns an empty throttle that blocks a key for window once it
// has limit failures (or, used as a rate limit, attempts) within window.
func NewLimiter(limit int, window time.Duration) *Throttle {
	return &Throttle{entries: map[string]*throttleEntry{}, now: time.Now, max: limit, window: window}
}

// Failures is how many failures key has in its current window.
func (t *Throttle) Failures(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entries[key]
	if e == nil || t.expired(e, t.now()) {
		return 0
	}
	return e.failures
}

// Attempt is one attempt Allow reserved. Exactly one of Fail, Succeed or
// Release ends it; the later calls are no-ops, so `defer a.Release()` right
// after Allow covers every early return.
type Attempt struct {
	t    *Throttle
	key  string
	done bool
	// Prior is what was already counted against the key when this attempt
	// was reserved: its failures in the window plus the other attempts in
	// flight. Thresholds below the limit (a challenge from N failures)
	// compare against it, so parallel attempts cannot all slip under them.
	Prior int
}

// Allow reserves an attempt for key. It is refused (ok false, with how long
// to wait for Retry-After) while the key is blocked, or while its failures
// plus the attempts in flight already reach the limit.
func (t *Throttle) Allow(key string) (a *Attempt, retryAfter time.Duration, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.entries[key]
	if e != nil && now.Before(e.blockedUntil) {
		return nil, e.blockedUntil.Sub(now), false
	}
	if e != nil && t.expired(e, now) {
		e.failures, e.blockedUntil = 0, time.Time{}
	}
	if e == nil {
		t.makeRoom(now)
		e = &throttleEntry{}
		t.entries[key] = e
	}
	prior := e.failures + e.inFlight
	if prior >= t.max {
		return nil, InFlightRetry, false
	}
	e.inFlight++
	return &Attempt{t: t, key: key, Prior: prior}, 0, true
}

// Fail ends the attempt as a failure and returns the key's failures in the
// window, this one included. The limit-th failure within the window blocks
// the key for the window.
func (a *Attempt) Fail() int {
	if a == nil || a.done {
		return 0
	}
	a.done = true
	t := a.t
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.entries[a.key]
	if e == nil { // evicted meanwhile
		t.makeRoom(now)
		e = &throttleEntry{inFlight: 1}
		t.entries[a.key] = e
	}
	e.inFlight--
	if e.failures == 0 || t.expired(e, now) {
		e.failures, e.firstFailure, e.blockedUntil = 0, now, time.Time{}
	}
	e.failures++
	if e.failures >= t.max {
		e.blockedUntil = now.Add(t.window)
	}
	return e.failures
}

// Succeed ends the attempt as a success: the key's failures are forgotten.
func (a *Attempt) Succeed() {
	if a == nil || a.done {
		return
	}
	a.done = true
	t := a.t
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entries[a.key]
	if e == nil {
		return
	}
	e.inFlight--
	e.failures, e.blockedUntil = 0, time.Time{}
	t.dropIdle(a.key, e)
}

// Release ends the attempt without counting it either way (it never reached
// the check, or the check could not be made): the reservation is freed, the
// failures stay.
func (a *Attempt) Release() {
	if a == nil || a.done {
		return
	}
	a.done = true
	t := a.t
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entries[a.key]
	if e == nil {
		return
	}
	e.inFlight--
	t.dropIdle(a.key, e)
}

func (t *Throttle) dropIdle(key string, e *throttleEntry) {
	if e.inFlight <= 0 && (e.failures == 0 || t.expired(e, t.now())) {
		delete(t.entries, key)
	}
}

// expired: the failure window has passed and the key is not blocked.
func (t *Throttle) expired(e *throttleEntry, now time.Time) bool {
	return !now.Before(e.blockedUntil) && now.Sub(e.firstFailure) >= t.window
}

// makeRoom keeps the map under maxThrottleKeys. Entries with an attempt in
// flight are never dropped: their reservation must still count.
func (t *Throttle) makeRoom(now time.Time) {
	if len(t.entries) < maxThrottleKeys {
		return
	}
	for k, e := range t.entries {
		if e.inFlight == 0 && (e.failures == 0 || t.expired(e, now)) {
			delete(t.entries, k)
		}
	}
	for k, e := range t.entries {
		if len(t.entries) < maxThrottleKeys {
			return
		}
		if e.inFlight == 0 {
			delete(t.entries, k)
		}
	}
}
