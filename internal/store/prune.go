package store

import (
	"context"
	"fmt"
	"time"
)

// RowRetention is how long rows no longer in use are kept before
// PruneAuthRows deletes them: long enough to look into a recent incident,
// short enough that the tables do not grow without bound.
const RowRetention = 7 * 24 * time.Hour

// Pruned counts the rows one PruneAuthRows call deleted.
type Pruned struct {
	RefreshTokens int64
	EmailTokens   int64
	OAuthStates   int64
}

// pruneQueries delete at most $1 rows each; $2, where used, is
// RowRetention in seconds. The watcher runs them every 5 minutes, so small
// batches keep up.
var pruneQueries = []struct {
	table     string
	retention bool // takes $2
	sql       string
}{
	// Only families with NO live token for RowRetention: every row expired
	// or revoked longer ago than that. A live family keeps all its rows: the
	// sessions list reads its oldest and newest, and presenting any rotated
	// token must still be recognised as reuse and revoke it.
	{"refresh_tokens", true, `
		DELETE FROM refresh_tokens WHERE id IN (
		    SELECT r.id FROM refresh_tokens r
		    WHERE NOT EXISTS (
		        SELECT 1 FROM refresh_tokens l
		        WHERE l.family_id = r.family_id
		          AND l.expires_at > now() - make_interval(secs => $2)
		          AND (l.revoked_at IS NULL OR l.revoked_at > now() - make_interval(secs => $2)))
		    LIMIT $1)`},
	// Used or expired longer ago than RowRetention: a link that can never
	// work again.
	{"email_tokens", true, `
		DELETE FROM email_tokens WHERE id IN (
		    SELECT id FROM email_tokens
		    WHERE used_at < now() - make_interval(secs => $2) OR expires_at < now() - make_interval(secs => $2)
		    LIMIT $1)`},
	// Expired sign-ins (10 min): nothing can consume them any more.
	{"oauth_states", false, `
		DELETE FROM oauth_states WHERE id IN (SELECT id FROM oauth_states WHERE expires_at < now() LIMIT $1)`},
}

// PruneAuthRows deletes refresh tokens, email tokens and sign-in states that
// can never be used again (see pruneQueries), in batches of batch rows, at
// most maxBatches per table, so one run never holds long locks or does
// unbounded work; what is left goes on the next run. outgoing_mail prunes
// itself on every send.
func (s *Store) PruneAuthRows(ctx context.Context, batch, maxBatches int) (Pruned, error) {
	var p Pruned
	counts := []*int64{&p.RefreshTokens, &p.EmailTokens, &p.OAuthStates}
	for i, q := range pruneQueries {
		for range maxBatches {
			args := []any{batch}
			if q.retention {
				args = append(args, RowRetention.Seconds())
			}
			tag, err := s.pool.Exec(ctx, q.sql, args...)
			if err != nil {
				return p, fmt.Errorf("pruning %s: %w", q.table, err)
			}
			*counts[i] += tag.RowsAffected()
			if tag.RowsAffected() < int64(batch) {
				break
			}
		}
	}
	return p, nil
}
