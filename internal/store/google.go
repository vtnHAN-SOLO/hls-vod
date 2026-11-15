package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

// Sign in with Google. A Google account is identified only by its
// subject ("sub"), never by its email.

// ProviderGoogle is the one provider user_identities allows.
const ProviderGoogle = "google"

// Intents of an oauth_states row.
const (
	IntentLogin = "login"
	IntentLink  = "link"
)

var (
	// ErrOAuthStateInvalid means the state is unknown, already used or
	// expired (indistinguishable on purpose).
	ErrOAuthStateInvalid = errors.New("unknown, used or expired sign-in state")
	// ErrGoogleTaken means the Google account is linked to another user.
	ErrGoogleTaken = errors.New("the Google account is linked to another user")
	// ErrGoogleAlreadyLinked means the user already has a Google account.
	ErrGoogleAlreadyLinked = errors.New("the user already has a Google account linked")
	// ErrLastLoginMethod means the user has no password, so Google is the
	// only way they can log in and cannot be removed.
	ErrLastLoginMethod = errors.New("the user has no password; Google is their only login method")
	// ErrSignupClosed means a new account would be needed while registration
	// is closed.
	ErrSignupClosed = errors.New("a new account would be needed and registration is closed")
)

// OAuthState is one started sign-in or link: what the callback needs.
type OAuthState struct {
	Nonce    string
	Verifier string // PKCE code verifier
	Intent   string // IntentLogin or IntentLink
	UserID   *string
}

// CreateOAuthState stores a state (only its hash) for ttl, and deletes the
// expired ones on the way.
func (s *Store) CreateOAuthState(ctx context.Context, stateHash []byte, st OAuthState, ttl time.Duration) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM oauth_states WHERE expires_at <= now()`); err != nil {
			return fmt.Errorf("deleting expired sign-in states: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO oauth_states (state_hash, nonce, pkce_verifier, intent, user_id, expires_at)
			VALUES ($1, $2, $3, $4, $5, now() + make_interval(secs => $6))`,
			stateHash, st.Nonce, st.Verifier, st.Intent, st.UserID, ttl.Seconds()); err != nil {
			return fmt.Errorf("storing sign-in state: %w", err)
		}
		return nil
	})
}

// ConsumeOAuthState deletes the state with that hash and returns it: single
// use. Unknown or expired (an expired row is deleted too) is
// ErrOAuthStateInvalid.
func (s *Store) ConsumeOAuthState(ctx context.Context, stateHash []byte) (OAuthState, error) {
	var (
		st   OAuthState
		live bool
	)
	err := s.pool.QueryRow(ctx, `
		DELETE FROM oauth_states WHERE state_hash = $1
		RETURNING nonce, pkce_verifier, intent, user_id::text, expires_at > now()`, stateHash).
		Scan(&st.Nonce, &st.Verifier, &st.Intent, &st.UserID, &live)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !live) {
		return OAuthState{}, ErrOAuthStateInvalid
	}
	if err != nil {
		return OAuthState{}, fmt.Errorf("consuming sign-in state: %w", err)
	}
	return st, nil
}

// GoogleOutcome is which rule GoogleSignIn applied.
type GoogleOutcome int

// The rules of GoogleSignIn, in the order they are tried.
const (
	GoogleKnown         GoogleOutcome = iota // a: the subject was linked already
	GoogleLinkedByEmail                      // b: linked to the verified account with that email
	GoogleCreated                            // d: a new account
)

func (o GoogleOutcome) String() string {
	switch o {
	case GoogleKnown:
		return "known"
	case GoogleLinkedByEmail:
		return "linked by email"
	case GoogleCreated:
		return "created"
	default:
		return fmt.Sprintf("GoogleOutcome(%d)", int(o))
	}
}

// GoogleSignIn is a verified Google identity signing in.
type GoogleSignIn struct {
	Subject string
	Email   string // lowercase, verified by Google
	// AllowCreate is REGISTRATION_OPEN: false refuses rule d only.
	AllowCreate bool
	// NextUsername proposes a username for a new account; it is called
	// again while the proposal is taken.
	NextUsername func() string
	Refresh      NewRefresh
}

// GoogleResult is who signed in, the new session's family, and how.
type GoogleResult struct {
	User    model.User
	Family  string
	Outcome GoogleOutcome
	// DeletedUnverified: an unverified account held the email and was
	// deleted (rule c).
	DeletedUnverified bool
}

// usernameAttempts bounds the NextUsername proposals for one new account.
const usernameAttempts = 5

// GoogleSignIn finds or creates the user for a Google identity and starts
// a session, in one transaction:
//
//	a. the subject is linked → that user
//	b. a VERIFIED account has the email → link the subject to it
//	c. an UNVERIFIED account has the email → delete it (it never proved
//	   the inbox, Google just did; whoever registered it knows its
//	   password, so it is never linked), then d
//	d. a new user: role user, email verified, no password, a username
//	   from NextUsername. ErrSignupClosed when !AllowCreate
//
// b on an account that already has another Google account is
// ErrGoogleAlreadyLinked. A disabled account found by a or by email is
// ErrAccountDisabled: nothing is linked, deleted or created.
func (s *Store) GoogleSignIn(ctx context.Context, in GoogleSignIn) (GoogleResult, error) {
	var out GoogleResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		u, err := scanUser(tx.QueryRow(ctx, `
			SELECT `+userColumns+` FROM users
			WHERE id = (SELECT user_id FROM user_identities WHERE provider = 'google' AND subject = $1)`, in.Subject))
		switch {
		case err == nil && u.Disabled():
			return ErrAccountDisabled
		case err == nil:
			out.User, out.Outcome = u, GoogleKnown
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("finding the Google identity: %w", err)
		default:
			if out.User, out.Outcome, out.DeletedUnverified, err = signInByEmail(ctx, tx, in); err != nil {
				return err
			}
		}
		out.Family, err = insertFamily(ctx, tx, out.User.ID, in.Refresh)
		return err
	})
	if err != nil {
		return GoogleResult{}, identityConflict(err)
	}
	return out, nil
}

// signInByEmail is rules b to d of GoogleSignIn.
func signInByEmail(ctx context.Context, tx pgx.Tx, in GoogleSignIn) (model.User, GoogleOutcome, bool, error) {
	deleted := false
	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1 FOR UPDATE`, in.Email))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return model.User{}, 0, false, fmt.Errorf("finding the account with that email: %w", err)
	case u.Disabled():
		// Never linked, and never deleted (rule c): an admin's decision
		// is not undone by signing in.
		return model.User{}, 0, false, ErrAccountDisabled
	case !u.Unverified():
		if err := insertIdentity(ctx, tx, u.ID, in.Subject, in.Email); err != nil {
			return model.User{}, 0, false, err
		}
		return u, GoogleLinkedByEmail, false, nil
	default:
		if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			return model.User{}, 0, false, fmt.Errorf("deleting the unverified account: %w", err)
		}
		deleted = true
	}
	if !in.AllowCreate {
		return model.User{}, 0, false, ErrSignupClosed
	}
	for range usernameAttempts {
		u, err = scanUser(tx.QueryRow(ctx, `
			INSERT INTO users (username, role, email, email_verified_at) VALUES ($1, 'user', $2, now())
			ON CONFLICT (username) DO NOTHING
			RETURNING `+userColumns, in.NextUsername(), in.Email))
		if errors.Is(err, pgx.ErrNoRows) {
			continue // username taken: the next proposal
		}
		if err != nil {
			return model.User{}, 0, false, fmt.Errorf("creating the account: %w", err)
		}
		if err := insertIdentity(ctx, tx, u.ID, in.Subject, in.Email); err != nil {
			return model.User{}, 0, false, err
		}
		return u, GoogleCreated, deleted, nil
	}
	return model.User{}, 0, false, fmt.Errorf("no free username after %d attempts", usernameAttempts)
}

func insertIdentity(ctx context.Context, tx pgx.Tx, userID, subject, email string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_identities (user_id, provider, subject, email_at_link) VALUES ($1, 'google', $2, NULLIF($3, ''))`,
		userID, subject, email); err != nil {
		return fmt.Errorf("linking the Google account: %w", err)
	}
	return nil
}

// identityConflict maps the unique violations of user_identities.
func identityConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "user_identities_provider_subject_key":
			return ErrGoogleTaken
		case "user_identities_user_id_provider_key":
			return ErrGoogleAlreadyLinked
		}
	}
	return err
}

// LinkGoogle links a Google account to the user. ErrGoogleTaken when it is
// linked to another user, ErrGoogleAlreadyLinked when the user has one,
// ErrAccountDisabled when an admin disabled the user since the link began.
func (s *Store) LinkGoogle(ctx context.Context, userID, subject, email string) error {
	return identityConflict(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockEnabledUser(ctx, tx, userID); err != nil {
			return err
		}
		return insertIdentity(ctx, tx, userID, subject, email)
	}))
}

// UnlinkGoogle removes the user's Google account. ErrLastLoginMethod when
// the user has no password; ErrNotFound when there is nothing to remove.
func (s *Store) UnlinkGoogle(ctx context.Context, userID string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var hasPassword bool
		err := tx.QueryRow(ctx, `SELECT password_hash IS NOT NULL FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&hasPassword)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("loading user: %w", err)
		}
		if !hasPassword {
			return ErrLastLoginMethod
		}
		tag, err := tx.Exec(ctx, `DELETE FROM user_identities WHERE user_id = $1 AND provider = 'google'`, userID)
		if err != nil {
			return fmt.Errorf("unlinking Google: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// GoogleIdentity is the user's Google account; ok is false when there is none.
func (s *Store) GoogleIdentity(ctx context.Context, userID string) (model.Identity, bool, error) {
	id := model.Identity{Provider: ProviderGoogle}
	err := s.pool.QueryRow(ctx, `
		SELECT subject, email_at_link, created_at FROM user_identities WHERE user_id = $1 AND provider = 'google'`, userID).
		Scan(&id.Subject, &id.EmailAtLink, &id.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Identity{}, false, nil
	}
	if err != nil {
		return model.Identity{}, false, fmt.Errorf("loading Google identity: %w", err)
	}
	return id, true, nil
}
