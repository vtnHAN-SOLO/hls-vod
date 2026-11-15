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

// ErrUserExists means the username is taken.
var ErrUserExists = errors.New("username already exists")

// ErrAccountDisabled means an admin has disabled the account: no login,
// refresh, Google sign-in, email verification or reset mail.
var ErrAccountDisabled = errors.New("the account is disabled")

// password_hash is NULL for a Google-only account; it reads as "".
const userColumns = `id::text, username, coalesce(password_hash, ''), role, email, email_verified_at, disabled_at, created_at, updated_at`

func scanUser(row pgx.Row) (model.User, error) {
	var u model.User
	var role string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &u.Email, &u.EmailVerifiedAt, &u.DisabledAt, &u.CreatedAt, &u.UpdatedAt)
	u.Role = model.Role(role)
	return u, err
}

// CreateUser inserts a user. The database refuses a username that is not
// lowercase; a taken one is ErrUserExists.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string, role model.Role) (model.User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role) VALUES ($1, $2, $3)
		RETURNING `+userColumns, username, passwordHash, string(role)))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return model.User{}, ErrUserExists
	}
	if err != nil {
		return model.User{}, fmt.Errorf("inserting user: %w", err)
	}
	return u, nil
}

// UserByUsername returns the user, or ErrNotFound.
func (s *Store) UserByUsername(ctx context.Context, username string) (model.User, error) {
	return s.oneUser(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1`, username)
}

// UserByID returns the user, or ErrNotFound.
func (s *Store) UserByID(ctx context.Context, id string) (model.User, error) {
	return s.oneUser(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
}

func (s *Store) oneUser(ctx context.Context, sql string, arg string) (model.User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, sql, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("loading user: %w", err)
	}
	return u, nil
}

// ListUsers returns every user, by username.
func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userColumns+` FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	users, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.User, error) { return scanUser(r) })
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	return users, nil
}

// SetPasswordHash replaces the user's password and, in the same transaction,
// revokes every refresh token they hold: a password change ends all
// sessions. ErrNotFound for an unknown username. cmd/useradmin uses it; the
// account page uses ChangePassword, which also starts the caller's new
// session.
func (s *Store) SetPasswordHash(ctx context.Context, username, passwordHash string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `UPDATE users SET password_hash = $2 WHERE username = $1 RETURNING id::text`,
			username, passwordHash).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("updating password: %w", err)
		}
		return revokeAllSessions(ctx, tx, id)
	})
}

// ChangePassword is a password change from the account page, in one
// transaction: the new hash, every refresh-token family of the user revoked
// (as SetPasswordHash does), and a new family started with refreshHash for
// the caller, so the tab that changed the password stays logged in while
// every other session dies at its next refresh. It returns the new family
// id. ErrNotFound for an unknown user id.
func (s *Store) ChangePassword(ctx context.Context, userID, passwordHash string, refresh NewRefresh) (string, error) {
	var family string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, passwordHash)
		if err != nil {
			return fmt.Errorf("updating password: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if err := revokeAllSessions(ctx, tx, userID); err != nil {
			return err
		}
		family, err = insertFamily(ctx, tx, userID, refresh)
		return err
	})
	if err != nil {
		return "", err
	}
	return family, nil
}

// revokeAllSessions revokes every live refresh token of the user: all their
// sessions end at the next refresh.
func revokeAllSessions(ctx context.Context, tx pgx.Tx, userID string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
		return fmt.Errorf("revoking sessions: %w", err)
	}
	return nil
}

// SetRole changes the user's role. Access tokens already issued keep the old
// role until they expire (at most 15 min); the next refresh picks it up.
func (s *Store) SetRole(ctx context.Context, username string, role model.Role) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET role = $2 WHERE username = $1`, username, string(role))
	if err != nil {
		return fmt.Errorf("updating role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

var (
	// ErrRefreshInvalid means an unknown, expired or already-dead refresh token.
	ErrRefreshInvalid = errors.New("invalid refresh token")
	// ErrRefreshReused means a token that was already rotated was presented
	// again. Its whole family has been revoked.
	ErrRefreshReused = errors.New("refresh token reused; family revoked")
)

// NewRefresh is a refresh token about to be stored: its hash (never the
// token), lifetime and, for a new login, the client's User-Agent (already
// truncated to 200 characters; "" for none). Rotation copies the user agent
// from the token it replaces.
type NewRefresh struct {
	Hash      []byte
	TTL       time.Duration
	UserAgent string
}

// querier is what both the pool and a transaction offer.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// CreateRefreshToken stores a new login's first refresh token in a new
// family, and returns the family id (the access tokens' sid).
func (s *Store) CreateRefreshToken(ctx context.Context, userID string, refresh NewRefresh) (string, error) {
	return insertFamily(ctx, s.pool, userID, refresh)
}

func insertFamily(ctx context.Context, q querier, userID string, refresh NewRefresh) (string, error) {
	var family string
	if err := q.QueryRow(ctx, `
		INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at, user_agent)
		VALUES ($1, gen_random_uuid(), $2, now() + make_interval(secs => $3), NULLIF($4, ''))
		RETURNING family_id::text`,
		userID, refresh.Hash, refresh.TTL.Seconds(), refresh.UserAgent).Scan(&family); err != nil {
		return "", fmt.Errorf("storing refresh token: %w", err)
	}
	return family, nil
}

// RotateRefreshToken exchanges the token with hash oldHash for a new one
// (newHash, valid for ttl) in the same family, in one transaction: the old
// one is revoked and points at its replacement, whose user agent it copies.
// It returns the user id and the family id.
//
// Presenting a token that is already revoked is reuse: someone holds a copy.
// Then every token of its family is revoked (committed), and the result is
// ErrRefreshReused. An unknown or expired token, or one of a disabled user,
// is ErrRefreshInvalid.
func (s *Store) RotateRefreshToken(ctx context.Context, oldHash []byte, next NewRefresh) (userID, familyID string, err error) {
	reused := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		var userAgent *string
		var revoked, expired bool
		err := tx.QueryRow(ctx, `
			SELECT r.id::text, r.user_id::text, r.family_id::text, r.user_agent, r.revoked_at IS NOT NULL,
			       r.expires_at <= now() OR u.disabled_at IS NOT NULL
			FROM refresh_tokens r JOIN users u ON u.id = r.user_id
			WHERE r.token_hash = $1 FOR UPDATE OF r`, oldHash).
			Scan(&id, &userID, &familyID, &userAgent, &revoked, &expired)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRefreshInvalid
		}
		if err != nil {
			return fmt.Errorf("loading refresh token: %w", err)
		}
		if revoked {
			if _, err := tx.Exec(ctx,
				`UPDATE refresh_tokens SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL`, familyID); err != nil {
				return fmt.Errorf("revoking family: %w", err)
			}
			reused = true
			return nil // commit the revocation
		}
		if expired {
			return ErrRefreshInvalid
		}
		var newID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at, user_agent)
			VALUES ($1, $2, $3, now() + make_interval(secs => $4), $5) RETURNING id::text`,
			userID, familyID, next.Hash, next.TTL.Seconds(), userAgent).Scan(&newID); err != nil {
			return fmt.Errorf("storing rotated refresh token: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE refresh_tokens SET revoked_at = now(), replaced_by = $2 WHERE id = $1`, id, newID); err != nil {
			return fmt.Errorf("revoking rotated refresh token: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	if reused {
		return "", "", ErrRefreshReused
	}
	return userID, familyID, nil
}

// RevokeRefreshFamily revokes the whole login the token belongs to (logout).
// An unknown token is not an error: logging out twice is fine.
func (s *Store) RevokeRefreshFamily(ctx context.Context, hash []byte) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = now()
		WHERE family_id = (SELECT family_id FROM refresh_tokens WHERE token_hash = $1) AND revoked_at IS NULL`, hash); err != nil {
		return fmt.Errorf("revoking refresh tokens: %w", err)
	}
	return nil
}

// canRefresh: the condition on a refresh_tokens row that can still be used.
const canRefresh = `revoked_at IS NULL AND expires_at > now()`

// ListSessions returns the user's sessions: one per refresh-token family
// that still has a live token, most recently used first. CreatedAt is the
// family's oldest row (the login), LastUsedAt its newest (every refresh
// inserts one), UserAgent the newest row's (rotation copies it).
func (s *Store) ListSessions(ctx context.Context, userID string) ([]model.LoginSession, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT family_id::text,
		       (array_agg(user_agent ORDER BY created_at DESC))[1],
		       min(created_at), max(created_at)
		FROM refresh_tokens
		WHERE user_id = $1
		  AND family_id IN (SELECT family_id FROM refresh_tokens WHERE user_id = $1 AND `+canRefresh+`)
		GROUP BY family_id
		ORDER BY max(created_at) DESC, family_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	sessions, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.LoginSession, error) {
		var ls model.LoginSession
		err := r.Scan(&ls.ID, &ls.UserAgent, &ls.CreatedAt, &ls.LastUsedAt)
		return ls, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	return sessions, nil
}

// SessionLive reports whether the user's session (refresh-token family)
// familyID can still refresh: it has a token that is neither revoked nor
// expired. The account endpoints ask it before acting, so a revoked
// session cannot use them during its last access token's lifetime.
func (s *Store) SessionLive(ctx context.Context, userID, familyID string) (bool, error) {
	var live bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM refresh_tokens WHERE user_id = $1 AND family_id = $2 AND `+canRefresh+`)`,
		userID, familyID).Scan(&live); err != nil {
		return false, fmt.Errorf("checking session: %w", err)
	}
	return live, nil
}

// RevokeSession revokes one of the user's sessions (refresh-token family).
// ErrNotFound when the user has no live session with that id: unknown,
// already ended, or another user's (indistinguishable on purpose).
func (s *Store) RevokeSession(ctx context.Context, userID, familyID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = now()
		WHERE user_id = $1 AND family_id = $2 AND `+canRefresh, userID, familyID)
	if err != nil {
		return fmt.Errorf("revoking session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeOtherSessions revokes every session of the user except keepFamily
// (the caller's own), and returns how many it ended.
func (s *Store) RevokeOtherSessions(ctx context.Context, userID, keepFamily string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `
		WITH revoked AS (
			UPDATE refresh_tokens SET revoked_at = now()
			WHERE user_id = $1 AND family_id <> $2 AND `+canRefresh+`
			RETURNING family_id
		)
		SELECT count(DISTINCT family_id) FROM revoked`, userID, keepFamily).Scan(&n); err != nil {
		return 0, fmt.Errorf("revoking other sessions: %w", err)
	}
	return n, nil
}
