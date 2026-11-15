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

// Email token purposes, as the CHECK on email_tokens.purpose allows them.
const (
	PurposeVerify = "verify"
	PurposeReset  = "reset"
)

// ErrEmailTokenInvalid means a mailed link's token is unknown, expired or
// already used (indistinguishable on purpose).
var ErrEmailTokenInvalid = errors.New("invalid, expired or used email token")

// NewAccount is a self-registered user about to be stored, with the hash of
// the first verify token (never the token).
type NewAccount struct {
	Username     string // lowercase, valid
	Email        string // lowercase, valid
	PasswordHash string
	VerifyHash   []byte
	VerifyTTL    time.Duration
}

// Registration is what Register did: created a new user (Existing nil), or
// found the address already taken by Existing and created nothing.
type Registration struct {
	User     model.User
	Existing *model.User
}

// Register stores a new, unverified account with role user and its first
// verify token, in one transaction. First, in the same transaction, it
// deletes squatters: unverified accounts holding the same email or username
// whose verify tokens have all expired (an unverified account lives at most
// VerifyTTL, see ResendVerification).
//
// A taken username is ErrUserExists, checked before the email, so the
// answer never depends on whether the address is registered. A taken email
// is not an error: the result carries the account that owns it.
func (s *Store) Register(ctx context.Context, a NewAccount) (Registration, error) {
	var out Registration
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := deleteExpiredUnverified(ctx, tx, a.Email, a.Username); err != nil {
			return err
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE username = $1)`, a.Username).Scan(&taken); err != nil {
			return fmt.Errorf("checking username: %w", err)
		}
		if taken {
			return ErrUserExists
		}
		existing, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, a.Email))
		if err == nil {
			out.Existing = &existing
			return nil // commit the cleanup; nothing else to do
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("checking email: %w", err)
		}
		if out.User, err = scanUser(tx.QueryRow(ctx, `
			INSERT INTO users (username, password_hash, role, email) VALUES ($1, $2, 'user', $3)
			RETURNING `+userColumns, a.Username, a.PasswordHash, a.Email)); err != nil {
			return fmt.Errorf("inserting user: %w", err)
		}
		return insertEmailToken(ctx, tx, out.User.ID, PurposeVerify, a.VerifyHash, a.VerifyTTL)
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// Lost a race with a concurrent registration.
		switch pgErr.ConstraintName {
		case "users_username_key":
			return Registration{}, ErrUserExists
		case "users_email_key":
			existing, err := s.oneUser(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, a.Email)
			if err != nil {
				return Registration{}, err
			}
			return Registration{Existing: &existing}, nil
		}
	}
	if err != nil {
		return Registration{}, err
	}
	return out, nil
}

// deleteExpiredUnverified deletes the squatters on email or username:
// unverified accounts whose verify tokens have all expired.
func deleteExpiredUnverified(ctx context.Context, tx pgx.Tx, email, username string) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM users u
		WHERE (u.email = $1 OR u.username = $2)
		  AND u.email IS NOT NULL AND u.email_verified_at IS NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM email_tokens t
		      WHERE t.user_id = u.id AND t.purpose = 'verify' AND t.used_at IS NULL AND t.expires_at > now())`,
		email, username); err != nil {
		return fmt.Errorf("removing expired unverified accounts: %w", err)
	}
	return nil
}

func insertEmailToken(ctx context.Context, tx pgx.Tx, userID, purpose string, hash []byte, ttl time.Duration) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO email_tokens (user_id, purpose, token_hash, expires_at)
		VALUES ($1, $2, $3, now() + make_interval(secs => $4))`,
		userID, purpose, hash, ttl.Seconds()); err != nil {
		return fmt.Errorf("storing email token: %w", err)
	}
	return nil
}

// ResendOutcome is what ResendVerification did.
type ResendOutcome int

// The outcomes of ResendVerification. Only ResendIssued sends a mail.
const (
	ResendIssued          ResendOutcome = iota // a new verify token was stored
	ResendNoAccount                            // no account has that address
	ResendAlreadyVerified                      // nothing to verify
	ResendExpired                              // the account's verify window is over
	ResendDisabled                             // an admin disabled the account
)

func (o ResendOutcome) String() string {
	switch o {
	case ResendIssued:
		return "issued"
	case ResendNoAccount:
		return "no account"
	case ResendAlreadyVerified:
		return "already verified"
	case ResendExpired:
		return "verification window over"
	case ResendDisabled:
		return "account disabled"
	default:
		return fmt.Sprintf("ResendOutcome(%d)", int(o))
	}
}

// ResendVerification replaces the verify token of the unverified account
// with that address by a new one (hash), and returns the account. The
// earlier unused tokens are deleted, so only the newest link works.
//
// The new token expires when the first one did, verifyTTL after the
// account was created: resending never extends an unverified account's
// life. Otherwise anyone could keep a squatted address alive for ever by
// asking for a new link every day; after the window the next registration
// with that address or username deletes the account (Register).
func (s *Store) ResendVerification(ctx context.Context, email string, hash []byte, verifyTTL time.Duration) (model.User, ResendOutcome, error) {
	var (
		u       model.User
		outcome ResendOutcome
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1 FOR UPDATE`, email))
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = ResendNoAccount
			return nil
		}
		if err != nil {
			return fmt.Errorf("loading user: %w", err)
		}
		if !u.Unverified() {
			outcome = ResendAlreadyVerified
			return nil
		}
		if u.Disabled() {
			outcome = ResendDisabled
			return nil
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM email_tokens WHERE user_id = $1 AND purpose = 'verify' AND used_at IS NULL`, u.ID); err != nil {
			return fmt.Errorf("deleting old verify tokens: %w", err)
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO email_tokens (user_id, purpose, token_hash, expires_at)
			SELECT id, 'verify', $2, created_at + make_interval(secs => $3)
			FROM users WHERE id = $1 AND created_at + make_interval(secs => $3) > now()`,
			u.ID, hash, verifyTTL.Seconds())
		if err != nil {
			return fmt.Errorf("storing email token: %w", err)
		}
		outcome = ResendIssued
		if tag.RowsAffected() == 0 {
			outcome = ResendExpired
		}
		return nil
	})
	if err != nil {
		return model.User{}, 0, err
	}
	return u, outcome, nil
}

// VerifyEmail applies a verify token, in one transaction: the account's
// email is marked verified, every open verify token of the account is
// marked used, and a new session (refresh-token family) starts with
// refresh. It returns the user and the family id. A token that is unknown,
// expired or already used is ErrEmailTokenInvalid; a disabled account's is
// ErrAccountDisabled, and the token is left as it was.
func (s *Store) VerifyEmail(ctx context.Context, hash []byte, refresh NewRefresh) (model.User, string, error) {
	var (
		u      model.User
		family string
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var userID string
		var usable bool
		err := tx.QueryRow(ctx, `
			SELECT user_id::text, used_at IS NULL AND expires_at > now()
			FROM email_tokens WHERE token_hash = $1 AND purpose = 'verify' FOR UPDATE`, hash).Scan(&userID, &usable)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !usable) {
			return ErrEmailTokenInvalid
		}
		if err != nil {
			return fmt.Errorf("loading email token: %w", err)
		}
		// The token stays unused: enabling the account makes it work again.
		var disabled bool
		if err := tx.QueryRow(ctx, `SELECT disabled_at IS NOT NULL FROM users WHERE id = $1 FOR UPDATE`, userID).
			Scan(&disabled); err != nil {
			return fmt.Errorf("loading user: %w", err)
		}
		if disabled {
			return ErrAccountDisabled
		}
		if _, err := tx.Exec(ctx, `
			UPDATE email_tokens SET used_at = now()
			WHERE user_id = $1 AND purpose = 'verify' AND used_at IS NULL`, userID); err != nil {
			return fmt.Errorf("marking verify tokens used: %w", err)
		}
		if u, err = scanUser(tx.QueryRow(ctx, `
			UPDATE users SET email_verified_at = coalesce(email_verified_at, now()) WHERE id = $1
			RETURNING `+userColumns, userID)); err != nil {
			return fmt.Errorf("marking email verified: %w", err)
		}
		family, err = insertFamily(ctx, tx, u.ID, refresh)
		return err
	})
	if err != nil {
		return model.User{}, "", err
	}
	return u, family, nil
}

// MailVerdict is ReserveMail's answer.
type MailVerdict int

// The verdicts of ReserveMail. Only MailAllowed may be sent.
const (
	MailAllowed      MailVerdict = iota // recorded; send it
	MailAddressLimit                    // this address had its mails for the hour
	MailDailyCap                        // the api had its mails for the day
)

func (v MailVerdict) String() string {
	switch v {
	case MailAllowed:
		return "allowed"
	case MailAddressLimit:
		return "per-address limit reached"
	case MailDailyCap:
		return "daily cap reached"
	default:
		return fmt.Sprintf("MailVerdict(%d)", int(v))
	}
}

// ReserveMail decides whether one more mail of kind to the address with
// hash toHash may go out, and if so records it (before it is sent, so a
// failed send still counts: the cap must hold whatever Resend did). At most
// perHour mails per address in the last hour, at most dailyCap in the last
// 24 hours overall. One transaction under an advisory lock, so api
// replicas never both take the last slot. Rows older than two days, which
// no limit looks at, are deleted on the way.
func (s *Store) ReserveMail(ctx context.Context, toHash []byte, kind string, perHour, dailyCap int) (MailVerdict, error) {
	verdict := MailAllowed
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('outgoing_mail'))`); err != nil {
			return fmt.Errorf("locking outgoing_mail: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM outgoing_mail WHERE created_at < now() - interval '2 days'`); err != nil {
			return fmt.Errorf("pruning outgoing_mail: %w", err)
		}
		var day, address int
		if err := tx.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE to_hash = $1 AND created_at > now() - interval '1 hour')
			FROM outgoing_mail WHERE created_at > now() - interval '24 hours'`, toHash).Scan(&day, &address); err != nil {
			return fmt.Errorf("counting outgoing mail: %w", err)
		}
		switch {
		case address >= perHour:
			verdict = MailAddressLimit
			return nil
		case day >= dailyCap:
			verdict = MailDailyCap
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO outgoing_mail (to_hash, kind) VALUES ($1, $2)`, toHash, kind); err != nil {
			return fmt.Errorf("recording outgoing mail: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return verdict, nil
}
