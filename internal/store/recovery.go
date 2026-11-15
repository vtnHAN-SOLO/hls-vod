package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

// Forgot / reset password, on the email_tokens of registration. An
// account's email never changes after registration: it is the recovery
// channel. Accounts made with useradmin have none, so no reset by mail.

// ResetOutcome is what IssueResetToken did.
type ResetOutcome int

// The outcomes of IssueResetToken. Only ResetIssued sends a mail.
const (
	ResetIssued     ResetOutcome = iota // a reset token was stored
	ResetNoAccount                      // no account has that address
	ResetUnverified                     // the address was never confirmed
	ResetDisabled                       // an admin disabled the account
)

func (o ResetOutcome) String() string {
	switch o {
	case ResetIssued:
		return "issued"
	case ResetNoAccount:
		return "no account"
	case ResetUnverified:
		return "address not verified"
	case ResetDisabled:
		return "account disabled"
	default:
		return fmt.Sprintf("ResetOutcome(%d)", int(o))
	}
}

// IssueResetToken stores a password-reset token (hash, valid for ttl) for
// the verified account with that address, and returns the account. The
// account's earlier unused reset tokens are deleted, so only the newest
// link works. An unknown or unverified address stores nothing. Accounts
// without an email (useradmin) have no address to match.
func (s *Store) IssueResetToken(ctx context.Context, email string, hash []byte, ttl time.Duration) (model.User, ResetOutcome, error) {
	var (
		u       model.User
		outcome ResetOutcome
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1 FOR UPDATE`, email))
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = ResetNoAccount
			return nil
		}
		if err != nil {
			return fmt.Errorf("loading user: %w", err)
		}
		if u.EmailVerifiedAt == nil {
			outcome = ResetUnverified
			return nil
		}
		if u.Disabled() {
			outcome = ResetDisabled
			return nil
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM email_tokens WHERE user_id = $1 AND purpose = 'reset' AND used_at IS NULL`, u.ID); err != nil {
			return fmt.Errorf("deleting old reset tokens: %w", err)
		}
		outcome = ResetIssued
		return insertEmailToken(ctx, tx, u.ID, PurposeReset, hash, ttl)
	})
	if err != nil {
		return model.User{}, 0, err
	}
	return u, outcome, nil
}

// EmailTokenUsable reports whether a token of purpose with that hash exists,
// is unused and has not expired. A cheap check before work that costs (an
// argon2id hash); the transaction that applies the token checks again.
func (s *Store) EmailTokenUsable(ctx context.Context, hash []byte, purpose string) (bool, error) {
	var ok bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM email_tokens
		               WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > now())`,
		hash, purpose).Scan(&ok); err != nil {
		return false, fmt.Errorf("checking email token: %w", err)
	}
	return ok, nil
}

// lockEmailToken loads the token of purpose with that hash FOR UPDATE and
// returns its user id. Unknown, used or expired is ErrEmailTokenInvalid.
func lockEmailToken(ctx context.Context, tx pgx.Tx, hash []byte, purpose string) (string, error) {
	var (
		userID string
		usable bool
	)
	err := tx.QueryRow(ctx, `
		SELECT user_id::text, used_at IS NULL AND expires_at > now()
		FROM email_tokens WHERE token_hash = $1 AND purpose = $2 FOR UPDATE`, hash, purpose).Scan(&userID, &usable)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !usable) {
		return "", ErrEmailTokenInvalid
	}
	if err != nil {
		return "", fmt.Errorf("loading email token: %w", err)
	}
	return userID, nil
}

// markTokensUsed marks every open token of purpose of the user used.
func markTokensUsed(ctx context.Context, tx pgx.Tx, userID, purpose string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE email_tokens SET used_at = now()
		WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL`, userID, purpose); err != nil {
		return fmt.Errorf("marking %s tokens used: %w", purpose, err)
	}
	return nil
}

// ResetPassword applies a reset token, in one transaction: the new password
// hash, every refresh-token family of the user revoked (as a password change
// does), and the token marked used. It starts no session. It returns the
// user. Unknown, expired or used is ErrEmailTokenInvalid.
func (s *Store) ResetPassword(ctx context.Context, hash []byte, passwordHash string) (model.User, error) {
	var u model.User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		userID, err := lockEmailToken(ctx, tx, hash, PurposeReset)
		if err != nil {
			return err
		}
		if err := markTokensUsed(ctx, tx, userID, PurposeReset); err != nil {
			return err
		}
		if u, err = scanUser(tx.QueryRow(ctx,
			`UPDATE users SET password_hash = $2 WHERE id = $1 RETURNING `+userColumns, userID, passwordHash)); err != nil {
			return fmt.Errorf("updating password: %w", err)
		}
		return revokeAllSessions(ctx, tx, userID)
	})
	if err != nil {
		return model.User{}, err
	}
	return u, nil
}

// IssueResetTokenForUser stores a reset token for the user, as
// IssueResetToken does for an address: the "Set password" of a Google-only
// account, whose link goes to its own (verified) email. ErrAccountDisabled,
// storing nothing, for a disabled account.
func (s *Store) IssueResetTokenForUser(ctx context.Context, userID string, hash []byte, ttl time.Duration) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockEnabledUser(ctx, tx, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM email_tokens WHERE user_id = $1 AND purpose = 'reset' AND used_at IS NULL`, userID); err != nil {
			return fmt.Errorf("deleting old reset tokens: %w", err)
		}
		return insertEmailToken(ctx, tx, userID, PurposeReset, hash, ttl)
	})
}

// lockEnabledUser locks the user's row: ErrNotFound for an unknown user,
// ErrAccountDisabled for a disabled one.
func lockEnabledUser(ctx context.Context, tx pgx.Tx, userID string) error {
	var disabled bool
	err := tx.QueryRow(ctx, `SELECT disabled_at IS NOT NULL FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("loading user: %w", err)
	}
	if disabled {
		return ErrAccountDisabled
	}
	return nil
}
