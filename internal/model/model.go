// Package model holds the domain types shared by store, service and handler.
package model

import (
	"crypto/rand"
	"fmt"
	"time"
)

// MovieStatus mirrors the CHECK constraint on movies.status.
type MovieStatus string

// The movie states, in pipeline order. Deleting: an admin deleted it; the
// watcher removes its files and then the row.
const (
	StatusPending    MovieStatus = "pending"
	StatusProcessing MovieStatus = "processing"
	StatusReady      MovieStatus = "ready"
	StatusFailed     MovieStatus = "failed"
	StatusDeleting   MovieStatus = "deleting"
)

// Valid reports whether s is one of the statuses the database accepts.
func (s MovieStatus) Valid() bool {
	switch s {
	case StatusPending, StatusProcessing, StatusReady, StatusFailed, StatusDeleting:
		return true
	}
	return false
}

// Movie is one row of movies. Nullable columns are pointers.
type Movie struct {
	ID          string
	Title       string
	Year        *int
	TMDBID      *int
	Overview    *string
	RuntimeSec  *int
	SourcePath  string  // inbox path; internal, never sent to clients
	LibraryPath *string // "library/{id}/{version}" (or "library/{id}" for the old layout); internal
	PosterPath  *string // relative to library/{id}/, e.g. "{version}/poster.jpg"
	Status      MovieStatus
	// LibraryCleanupAfter: once past, the watcher removes every entry of
	// library/{id}/ but the current version. Set when a retranscode
	// publishes (old versions stay until no URL for them can be valid).
	LibraryCleanupAfter *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Rendition is one HLS rung of a movie.
type Rendition struct {
	ID           string
	MovieID      string
	Height       int
	BitrateKbps  *int
	PlaylistPath string // relative to the movie's library dir
	SizeBytes    *int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ValidID reports whether s is a canonical lowercase UUID
// (8-4-4-4-12 hex), the only form ids take in URLs and paths.
//
// Checking before querying turns "not-a-uuid" into a clean 400 instead of a
// Postgres cast error, and guarantees an id is safe inside a path.
func ValidID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}

// NewID returns a random (version 4) UUID in canonical lowercase form, for
// rows whose id must be known before the INSERT (a movie's library dir is
// named after it).
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])  // crypto/rand.Read never returns an error
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 9562 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Role mirrors the CHECK constraint on users.role.
type Role string

// The two roles.
const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// Valid reports whether r is a role the database accepts.
func (r Role) Valid() bool { return r == RoleUser || r == RoleAdmin }

// User is one row of users. PasswordHash is an argon2id encoded string and
// never leaves the api.
//
// Email is nil for accounts made with cmd/useradmin. An account that has an
// email logs in only once it is verified (EmailVerifiedAt set).
type User struct {
	ID              string
	Username        string
	PasswordHash    string // "" when the account has no password (Google only)
	Role            Role
	Email           *string // lowercase
	EmailVerifiedAt *time.Time
	// DisabledAt is set while an admin has disabled the account: no login,
	// refresh, Google sign-in or email verification.
	DisabledAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Disabled reports whether an admin has disabled the account.
func (u User) Disabled() bool { return u.DisabledAt != nil }

// Unverified reports whether the user registered with an email address and
// has not confirmed it yet: such an account cannot log in.
func (u User) Unverified() bool { return u.Email != nil && u.EmailVerifiedAt == nil }

// HasPassword reports whether the user can log in with a password. A
// Google-only account has none (PasswordHash "").
func (u User) HasPassword() bool { return u.PasswordHash != "" }

// Identity is a sign-in provider account linked to a user. Subject (Google's
// "sub") is what identifies it; EmailAtLink is only shown to the user.
type Identity struct {
	Provider    string
	Subject     string
	EmailAtLink *string
	CreatedAt   time.Time
}

// LoginSession is one login of a user: a refresh-token family that still has
// a live (unrevoked, unexpired) token. ID is the family id, which access
// tokens carry as their "sid" claim. Never holds a token or a hash.
type LoginSession struct {
	ID         string
	UserAgent  *string   // as the browser sent it at login, at most 200 characters
	CreatedAt  time.Time // the login: the family's oldest row
	LastUsedAt time.Time // the newest row: every refresh inserts one
}

// Progress is where a user stopped in a movie (one row of watch_progress),
// with the movie itself.
type Progress struct {
	Movie       Movie
	PositionSec int
	UpdatedAt   time.Time
}

// UserSummary is a user as the admin area lists it. Never a hash or token.
type UserSummary struct {
	User
	GoogleLinked bool
}

// JobStatus mirrors the CHECK constraint on transcode_jobs.status.
type JobStatus string

// The four job states.
const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
)

// Valid reports whether s is a job status the database accepts.
func (s JobStatus) Valid() bool {
	switch s {
	case JobQueued, JobRunning, JobSucceeded, JobFailed:
		return true
	}
	return false
}

// JobPurpose mirrors the CHECK constraint on transcode_jobs.purpose.
type JobPurpose string

// Ingest: a new (or failed) movie, pending → processing → ready/failed.
// Retranscode: a ready movie gets a new version and stays ready throughout.
const (
	PurposeIngest      JobPurpose = "ingest"
	PurposeRetranscode JobPurpose = "retranscode"
)

// Job is one transcode job as the admin area lists it, with its movie's
// title. The backend detail is K8sJobName (kubernetes) or LocalPID (local).
type Job struct {
	ID         string
	MovieID    string
	MovieTitle string
	Status     JobStatus
	Purpose    JobPurpose
	StartedAt  *time.Time
	FinishedAt *time.Time
	ErrorMsg   *string // as stored: the transcoder bounds it
	K8sJobName *string
	LocalPID   *int
	CreatedAt  time.Time
}

// AdminMovie is a movie as the admin area lists it: any status, and the
// status of its active (queued or running) job, nil when it has none.
type AdminMovie struct {
	Movie
	ActiveJob        *JobStatus
	ActiveJobPurpose *JobPurpose
}
