package store_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/poster"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// fakePoster is a processed poster with made-up bytes: the store never
// looks inside them.
func fakePoster(seed string, widths ...int) poster.Poster {
	p := poster.Poster{}
	for _, w := range widths {
		p.Variants = append(p.Variants, poster.Variant{Width: w, Height: poster.HeightFor(w), Bytes: []byte(fmt.Sprintf("%s jpeg %d", seed, w))})
	}
	p.Hash = sha256.Sum256(p.Variants[len(p.Variants)-1].Bytes)
	return p
}

// Posters, publishing and the rule the database itself enforces.
func TestPostersAndPublishing(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ctx := t.Context()
	posters := st.Posters()
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	movie := func(title, source string, status model.MovieStatus) string {
		return insertMovie(t, db.Pool, title, source, status, at)
	}
	publish := func(id string) { db.Publish(ctx, t, id) }

	t.Run("the CHECK constraint refuses an incomplete publish in SQL itself", func(t *testing.T) {
		pending := movie("pending", "sources/c1.mkv", model.StatusPending)
		bare := movie("bare", "sources/c2.mkv", model.StatusReady)
		withPoster := movie("poster only", "sources/c3.mkv", model.StatusReady)
		if err := posters.Put(ctx, withPoster, fakePoster("c3", 300)); err != nil {
			t.Fatal(err)
		}
		if err := posters.Put(ctx, pending, fakePoster("c1", 300)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE movies SET overview = 'Text' WHERE id = $1`, pending); err != nil {
			t.Fatal(err)
		}
		for _, tt := range []struct {
			name, id, set string
		}{
			{"no poster, no overview", bare, ""},
			{"poster, no overview", withPoster, ""},
			{"poster, blank overview", withPoster, `overview = '   ',`},
			{"poster and overview, not ready", pending, ""},
		} {
			_, err := db.Pool.Exec(ctx, `UPDATE movies SET `+tt.set+` published_at = now() WHERE id = $1`, tt.id)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "movies_published_check" {
				t.Errorf("%s: err = %v, want the movies_published_check violation", tt.name, err)
			}
		}
		// With all three, the same statement is accepted.
		if _, err := db.Pool.Exec(ctx, `UPDATE movies SET overview = 'Text', published_at = now() WHERE id = $1`, withPoster); err != nil {
			t.Fatalf("complete movie: %v", err)
		}
		// And a published movie cannot lose them behind the store's back.
		for _, q := range []string{
			`UPDATE movies SET poster_hash = NULL WHERE id = $1`,
			`UPDATE movies SET overview = NULL WHERE id = $1`,
			`UPDATE movies SET status = 'failed' WHERE id = $1`,
		} {
			if _, err := db.Pool.Exec(ctx, q, withPoster); err == nil {
				t.Errorf("%s on a published movie was accepted", q)
			}
		}
	})

	t.Run("publish reasons, in order", func(t *testing.T) {
		failed := movie("failed", "sources/p1.mkv", model.StatusFailed)
		ready := movie("ready", "sources/p2.mkv", model.StatusReady)
		refused := func(id string) store.PublishRefusal {
			t.Helper()
			var e *store.PublishRefusedError
			if err := st.PublishMovie(ctx, id); !errors.As(err, &e) {
				t.Fatalf("publish: %v, want a refusal", err)
			}
			return e.Reason
		}
		// A failed movie with a poster and overview is still not_ready.
		if err := posters.Put(ctx, failed, fakePoster("p1", 300)); err != nil {
			t.Fatal(err)
		}
		if got := refused(failed); got != store.RefusedNotReady {
			t.Errorf("failed movie: %s", got)
		}
		if got := refused(ready); got != store.RefusedMissingPoster {
			t.Errorf("no poster: %s", got)
		}
		if err := posters.Put(ctx, ready, fakePoster("p2", 300, 600)); err != nil {
			t.Fatal(err)
		}
		if got := refused(ready); got != store.RefusedMissingOverview {
			t.Errorf("no overview: %s", got)
		}
		blank := "  "
		if _, err := st.UpdateMovieMetadata(ctx, ready, model.MovieMetadata{Overview: &blank}); err != nil {
			t.Fatal(err)
		}
		if got := refused(ready); got != store.RefusedMissingOverview {
			t.Errorf("blank overview: %s", got)
		}
		text := "Now it has one."
		if _, err := st.UpdateMovieMetadata(ctx, ready, model.MovieMetadata{Overview: &text}); err != nil {
			t.Fatal(err)
		}
		if err := st.PublishMovie(ctx, ready); err != nil {
			t.Fatal(err)
		}
		m, err := st.GetMovie(ctx, ready)
		if err != nil || m.PublishedAt == nil {
			t.Fatalf("after publish: %+v, %v", m, err)
		}
		first := *m.PublishedAt
		if err := st.PublishMovie(ctx, ready); err != nil {
			t.Fatal(err)
		}
		if m, _ := st.GetMovie(ctx, ready); !m.PublishedAt.Equal(first) {
			t.Error("publishing again moved published_at")
		}
		if err := st.PublishMovie(ctx, model.NewID()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("unknown: %v", err)
		}
	})

	t.Run("posters: hash, widths, stale URLs, delete refused while published, cascade", func(t *testing.T) {
		id := movie("postered", "sources/q1.mkv", model.StatusReady)
		p1 := fakePoster("first", 300, 600, 1000)
		if err := posters.Put(ctx, id, p1); err != nil {
			t.Fatal(err)
		}
		m, err := st.GetMovie(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(m.PosterHash, p1.Hash[:]) || !slices.Equal(m.PosterWidths, []int{300, 600, 1000}) {
			t.Fatalf("hash %x widths %v", m.PosterHash, m.PosterWidths)
		}
		b, err := posters.Get(ctx, id, 600, p1.Hash[:])
		if err != nil || string(b) != string(p1.Variants[1].Bytes) {
			t.Fatalf("get 600 = %q, %v", b, err)
		}
		// A new upload replaces every row: fewer widths, a new hash.
		p2 := fakePoster("second", 300)
		if err := posters.Put(ctx, id, p2); err != nil {
			t.Fatal(err)
		}
		if _, err := posters.Get(ctx, id, 300, p1.Hash[:]); !errors.Is(err, poster.ErrNotFound) {
			t.Errorf("the old hash still serves: %v", err)
		}
		if _, err := posters.Get(ctx, id, 600, p2.Hash[:]); !errors.Is(err, poster.ErrNotFound) {
			t.Errorf("a width the new poster lacks: %v", err)
		}
		if m, _ := st.GetMovie(ctx, id); !slices.Equal(m.PosterWidths, []int{300}) {
			t.Errorf("widths after re-upload = %v", m.PosterWidths)
		}
		if err := posters.Put(ctx, model.NewID(), p2); !errors.Is(err, poster.ErrNotFound) {
			t.Errorf("put on an unknown movie: %v", err)
		}

		text := "Overview."
		if _, err := st.UpdateMovieMetadata(ctx, id, model.MovieMetadata{Overview: &text}); err != nil {
			t.Fatal(err)
		}
		if err := st.PublishMovie(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := posters.Delete(ctx, id); !errors.Is(err, poster.ErrPublished) {
			t.Fatalf("delete while published: %v", err)
		}
		if err := st.UnpublishMovie(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := posters.Delete(ctx, id); err != nil {
			t.Fatal(err)
		}
		if m, _ := st.GetMovie(ctx, id); m.PosterHash != nil || len(m.PosterWidths) != 0 {
			t.Errorf("after delete: hash %x widths %v", m.PosterHash, m.PosterWidths)
		}
		if err := posters.Delete(ctx, id); err != nil {
			t.Errorf("deleting no poster: %v", err)
		}

		// The rows go with the movie.
		if err := posters.Put(ctx, id, p1); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(ctx, `DELETE FROM movies WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM movie_posters WHERE movie_id = $1`, id).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%d poster rows left after the movie, %v", n, err)
		}
	})

	t.Run("viewer queries hide unpublished movies; admin lists see them", func(t *testing.T) {
		u, err := st.CreateUser(ctx, "watcher", "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U", model.RoleUser)
		if err != nil {
			t.Fatal(err)
		}
		shown := movie("zz shown", "sources/v1.mkv", model.StatusReady)
		hidden := movie("zz hidden", "sources/v2.mkv", model.StatusReady)
		publish(shown)
		publish(hidden)
		for _, id := range []string{shown, hidden} {
			if err := st.SaveProgress(ctx, u.ID, id, 10); err != nil {
				t.Fatal(err)
			}
			if err := st.SetFeatured(ctx, id, true); err != nil {
				t.Fatal(err)
			}
		}
		// Unpublishing takes it out of the spotlight too.
		if err := st.UnpublishMovie(ctx, hidden); err != nil {
			t.Fatal(err)
		}
		if m, _ := st.GetMovie(ctx, hidden); m.FeaturedAt != nil || m.PublishedAt != nil {
			t.Fatalf("after unpublish: featured %v published %v", m.FeaturedAt, m.PublishedAt)
		}
		if err := st.SetFeatured(ctx, hidden, true); !errors.Is(err, store.ErrMovieNotPublished) {
			t.Fatalf("featuring an unpublished movie: %v", err)
		}

		q := "zz "
		viewer, _, err := st.ListMovies(ctx, store.MovieFilter{Title: &q, PublishedOnly: true, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		admin, _, err := st.ListMovies(ctx, store.MovieFilter{Title: &q, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		featured, err := st.FeaturedMovies(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		popular, err := st.PopularMovies(ctx, nil, 20)
		if err != nil {
			t.Fatal(err)
		}
		cw, err := st.ContinueWatching(ctx, u.ID, 95, 20)
		if err != nil {
			t.Fatal(err)
		}
		var pop, cont []model.Movie
		for _, p := range popular {
			pop = append(pop, p.Movie)
		}
		for _, p := range cw {
			cont = append(cont, p.Movie)
		}
		for name, ms := range map[string][]model.Movie{"list": viewer, "featured": featured, "popular": pop, "continue watching": cont} {
			if !slices.Contains(titles(ms), "zz shown") || slices.Contains(titles(ms), "zz hidden") {
				t.Errorf("%s = %v: want the published movie only", name, titles(ms))
			}
		}
		if !slices.Equal(titles(admin), []string{"zz shown", "zz hidden"}) && !slices.Equal(titles(admin), []string{"zz hidden", "zz shown"}) {
			t.Errorf("admin list = %v, want both", titles(admin))
		}
		admins, _, err := st.AdminMovies(ctx, store.MovieFilter{Title: &q, Limit: 10})
		if err != nil || len(admins) != 2 {
			t.Errorf("admin movies = %d, %v", len(admins), err)
		}
	})

	t.Run("deleting unpublishes in the same statement", func(t *testing.T) {
		id := movie("to delete", "sources/d1.mkv", model.StatusReady)
		publish(id)
		if err := st.DeleteMovie(ctx, id); err != nil {
			t.Fatal(err)
		}
		m, err := st.GetMovie(ctx, id)
		if err != nil || m.Status != model.StatusDeleting || m.PublishedAt != nil {
			t.Fatalf("after delete: %s published %v, %v", m.Status, m.PublishedAt, err)
		}
	})

	t.Run("CreateMovie stores the poster and publishes in one transaction", func(t *testing.T) {
		id, text := model.NewID(), "A demo."
		p := fakePoster("create", 300, 600)
		m := store.NewMovie{ID: id, Title: "Created", SourcePath: "sources/" + id + ".mkv", LibraryPath: "library/" + id,
			BackdropPath: "poster.jpg", RuntimeSec: 30, Status: model.StatusReady, Overview: &text, Poster: &p, Publish: true}
		if err := st.CreateMovie(ctx, m, nil); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetMovie(ctx, id)
		if err != nil || got.PublishedAt == nil || !slices.Equal(got.PosterWidths, []int{300, 600}) {
			t.Fatalf("created: %+v, %v", got, err)
		}
		// Without an overview the constraint refuses the whole movie.
		bad := model.NewID()
		m = store.NewMovie{ID: bad, Title: "No overview", SourcePath: "sources/" + bad + ".mkv", LibraryPath: "library/" + bad,
			BackdropPath: "poster.jpg", RuntimeSec: 30, Status: model.StatusReady, Poster: &p, Publish: true}
		if err := st.CreateMovie(ctx, m, nil); err == nil {
			t.Fatal("published without an overview")
		}
		if _, err := st.GetMovie(ctx, bad); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a refused create left a row: %v", err)
		}
	})
}
