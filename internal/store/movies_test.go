package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// One container, real migrations, then read-only subtests over seeded rows.
func TestMovieQueries(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	// 7 movies; the three "tie" movies share one created_at, so only the id
	// tiebreak keeps pagination stable.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seed := []struct {
		title  string
		status model.MovieStatus
		at     time.Time
	}{
		{"oldest", model.StatusReady, base},
		{"failed one", model.StatusFailed, base.Add(1 * time.Hour)},
		{"tie a", model.StatusReady, base.Add(2 * time.Hour)},
		{"tie b", model.StatusPending, base.Add(2 * time.Hour)},
		{"tie c", model.StatusReady, base.Add(2 * time.Hour)},
		{"processing", model.StatusProcessing, base.Add(3 * time.Hour)},
		{"newest", model.StatusReady, base.Add(4 * time.Hour)},
	}
	ids := map[string]string{}
	for i, s := range seed {
		ids[s.title] = insertMovie(t, db.Pool, s.title, fmt.Sprintf("/inbox/%d.mkv", i), s.status, s.at)
	}

	all := listAll(t, st, nil)

	t.Run("newest first, id breaks ties", func(t *testing.T) {
		if len(all) != len(seed) {
			t.Fatalf("got %d movies, want %d", len(all), len(seed))
		}
		if all[0].Title != "newest" || all[len(all)-1].Title != "oldest" {
			t.Fatalf("order = %v", titles(all))
		}
		for i := 1; i < len(all); i++ {
			a, b := all[i-1], all[i]
			if a.CreatedAt.Before(b.CreatedAt) || (a.CreatedAt.Equal(b.CreatedAt) && a.ID < b.ID) {
				t.Fatalf("not ordered by created_at DESC, id DESC at %d: %v", i, titles(all))
			}
		}
	})

	t.Run("pages partition the list exactly", func(t *testing.T) {
		var paged []model.Movie
		for off := int64(0); ; off += 2 {
			page, total, err := st.ListMovies(t.Context(), store.MovieFilter{Limit: 2, Offset: off})
			if err != nil {
				t.Fatal(err)
			}
			if total != int64(len(seed)) {
				t.Fatalf("total = %d at offset %d, want %d", total, off, len(seed))
			}
			if len(page) == 0 {
				break
			}
			paged = append(paged, page...)
		}
		if !slices.Equal(titles(paged), titles(all)) {
			t.Fatalf("paged = %v\nwant    %v", titles(paged), titles(all))
		}
	})

	t.Run("offset past the end keeps the total", func(t *testing.T) {
		page, total, err := st.ListMovies(t.Context(), store.MovieFilter{Limit: 20, Offset: 100})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != 0 || total != int64(len(seed)) {
			t.Fatalf("page %d rows, total %d; want 0 rows, total %d", len(page), total, len(seed))
		}
	})

	t.Run("status filter", func(t *testing.T) {
		tests := []struct {
			status model.MovieStatus
			want   []string
		}{
			{model.StatusReady, []string{"newest", "tie a", "tie c", "oldest"}},
			{model.StatusPending, []string{"tie b"}},
			{model.StatusProcessing, []string{"processing"}},
			{model.StatusFailed, []string{"failed one"}},
		}
		for _, tt := range tests {
			t.Run(string(tt.status), func(t *testing.T) {
				got := listAll(t, st, &tt.status)
				slices.Sort(tt.want)
				names := titles(got)
				slices.Sort(names)
				if !slices.Equal(names, tt.want) {
					t.Fatalf("movies = %v, want %v", names, tt.want)
				}
				_, total, err := st.ListMovies(t.Context(), store.MovieFilter{Status: &tt.status, Limit: 1})
				if err != nil {
					t.Fatal(err)
				}
				if total != int64(len(tt.want)) {
					t.Fatalf("total = %d, want %d (the filter must apply to the count too)", total, len(tt.want))
				}
			})
		}
	})

	t.Run("get movie maps every column", func(t *testing.T) {
		id := ids["newest"]
		if _, err := db.Pool.Exec(t.Context(), `
			UPDATE movies SET year = 1999, tmdb_id = 603, overview = 'o', runtime_sec = 8160,
			       library_path = '/library/x', poster_path = 'poster.jpg' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		m, err := st.GetMovie(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if m.ID != id || m.Title != "newest" || m.Status != model.StatusReady ||
			*m.Year != 1999 || *m.TMDBID != 603 || *m.Overview != "o" || *m.RuntimeSec != 8160 ||
			*m.LibraryPath != "/library/x" || *m.PosterPath != "poster.jpg" || m.SourcePath == "" ||
			!m.UpdatedAt.After(m.CreatedAt) {
			t.Fatalf("movie = %+v", m)
		}

		null, err := st.GetMovie(t.Context(), ids["oldest"])
		if err != nil {
			t.Fatal(err)
		}
		if null.Year != nil || null.Overview != nil || null.PosterPath != nil || null.LibraryPath != nil {
			t.Fatalf("NULL columns should scan to nil: %+v", null)
		}
	})

	t.Run("unknown id is ErrNotFound", func(t *testing.T) {
		_, err := st.GetMovie(t.Context(), "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a")
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	// Runs against real Postgres: ILIKE and its ESCAPE clause are the point.
	t.Run("title search is case-insensitive and literal", func(t *testing.T) {
		for i, m := range []struct {
			title  string
			status model.MovieStatus
		}{
			{"100% Pure", model.StatusReady},
			{"1000 Ways", model.StatusReady},
			{"snake_case", model.StatusReady},
			{"snakeXcase", model.StatusPending},
			{`Back\slash`, model.StatusReady},
			{"UPPER lower Ünïcode", model.StatusReady},
		} {
			insertMovie(t, db.Pool, m.title, fmt.Sprintf("/inbox/search-%d.mkv", i), m.status, base.Add(time.Duration(10+i)*time.Hour))
		}
		ready := model.StatusReady
		tests := []struct {
			name   string
			q      string
			status *model.MovieStatus
			want   []string
		}{
			{"substring, any case", "PURE", nil, []string{"100% Pure"}},
			{"percent is literal, not a wildcard", "100%", nil, []string{"100% Pure"}},
			{"percent alone", "%", nil, []string{"100% Pure"}},
			{"underscore is literal, not one character", "e_c", nil, []string{"snake_case"}},
			{"underscore alone", "_", nil, []string{"snake_case"}},
			{"backslash is literal", `k\s`, nil, []string{`Back\slash`}},
			{"non-ASCII, any case", "ünïCODE", nil, []string{"UPPER lower Ünïcode"}},
			{"wildcard-looking text matches nothing", "1_0%", nil, nil},
			{"combined with status", "snake", &ready, []string{"snake_case"}},
			{"no match", "zzz", nil, nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				q := tt.q
				got, total, err := st.ListMovies(t.Context(), store.MovieFilter{Title: &q, Status: tt.status, Limit: 100})
				if err != nil {
					t.Fatal(err)
				}
				names := titles(got)
				slices.Sort(names)
				want := slices.Clone(tt.want)
				slices.Sort(want)
				if !slices.Equal(names, want) {
					t.Fatalf("q=%q: titles = %q, want %q", tt.q, names, want)
				}
				if total != int64(len(tt.want)) {
					t.Fatalf("q=%q: total = %d, want %d (the count must use the same filter)", tt.q, total, len(tt.want))
				}
			})
		}
	})

	t.Run("renditions tallest first", func(t *testing.T) {
		id := ids["newest"]
		for _, h := range []int{480, 1080, 720} {
			if _, err := db.Pool.Exec(t.Context(), `
				INSERT INTO renditions (movie_id, height, bitrate_kbps, playlist_path, size_bytes)
				VALUES ($1, $2, $3, $4, $5)`, id, h, h*4, fmt.Sprintf("%dp/index.m3u8", h), int64(h)*1_000_000); err != nil {
				t.Fatal(err)
			}
		}
		// Another movie's rendition must not show up.
		if _, err := db.Pool.Exec(t.Context(), `
			INSERT INTO renditions (movie_id, height, playlist_path) VALUES ($1, 2160, '2160p/index.m3u8')`,
			ids["oldest"]); err != nil {
			t.Fatal(err)
		}

		rs, err := st.ListRenditions(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		var heights []int
		for _, r := range rs {
			heights = append(heights, r.Height)
			if r.MovieID != id || r.PlaylistPath == "" || *r.BitrateKbps != r.Height*4 || *r.SizeBytes != int64(r.Height)*1_000_000 {
				t.Fatalf("rendition = %+v", r)
			}
		}
		if !slices.Equal(heights, []int{1080, 720, 480}) {
			t.Fatalf("heights = %v, want [1080 720 480]", heights)
		}

		none, err := st.ListRenditions(t.Context(), ids["tie b"])
		if err != nil || len(none) != 0 {
			t.Fatalf("movie without renditions: %v, %v", none, err)
		}
	})
}

func insertMovie(t *testing.T, pool *pgxpool.Pool, title, source string, status model.MovieStatus, at time.Time) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO movies (title, source_path, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4) RETURNING id::text`, title, source, string(status), at).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func listAll(t *testing.T, st *store.Store, status *model.MovieStatus) []model.Movie {
	t.Helper()
	ms, _, err := st.ListMovies(t.Context(), store.MovieFilter{Status: status, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func titles(ms []model.Movie) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Title
	}
	return out
}

// Sorts and year filters, on movies with missing years and with rows that
// tie on every sort key except the id.
func TestMovieSortAndYear(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	year := func(y int) *int { return &y }
	seed := []struct {
		title string
		year  *int
	}{
		{"beta", year(2001)},
		{"Alpha", year(1999)},
		{"Gamma", nil},
		{"Twin", year(2010)},
		{"delta", nil},
		{"Twin", year(2010)}, // same title, same year: only the id orders the pair
		{"Epsilon", year(1880)},
		{"Zeta", year(2100)},
	}
	var twins []string
	for i, s := range seed {
		id := insertMovie(t, db.Pool, s.title, fmt.Sprintf("/inbox/%d.mkv", i), model.StatusReady, base.Add(time.Duration(i)*time.Hour))
		if _, err := db.Pool.Exec(t.Context(), `UPDATE movies SET year = $2 WHERE id = $1`, id, s.year); err != nil {
			t.Fatal(err)
		}
		if s.title == "Twin" {
			twins = append(twins, id)
		}
	}
	slices.Sort(twins) // uuid text order is uuid order

	list := func(t *testing.T, f store.MovieFilter) ([]model.Movie, int64) {
		t.Helper()
		if f.Limit == 0 {
			f.Limit = 100
		}
		ms, total, err := st.ListMovies(t.Context(), f)
		if err != nil {
			t.Fatal(err)
		}
		return ms, total
	}
	twinOrder := func(t *testing.T, ms []model.Movie, want []string) {
		t.Helper()
		var got []string
		for _, m := range ms {
			if m.Title == "Twin" {
				got = append(got, m.ID)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("the two Twin rows came back as %v, want %v (id tiebreak)", got, want)
		}
	}

	t.Run("sorts", func(t *testing.T) {
		tests := []struct {
			sort store.MovieSort
			want []string
		}{
			{"", []string{"Zeta", "Epsilon", "Twin", "delta", "Twin", "Gamma", "Alpha", "beta"}},
			{store.SortAddedDesc, []string{"Zeta", "Epsilon", "Twin", "delta", "Twin", "Gamma", "Alpha", "beta"}},
			// Case-insensitive: "Alpha" before "beta" before "delta" before "Epsilon".
			{store.SortTitleAsc, []string{"Alpha", "beta", "delta", "Epsilon", "Gamma", "Twin", "Twin", "Zeta"}},
			// No year last in both directions; same year by title.
			{store.SortYearDesc, []string{"Zeta", "Twin", "Twin", "beta", "Alpha", "Epsilon", "delta", "Gamma"}},
			{store.SortYearAsc, []string{"Epsilon", "Alpha", "beta", "Twin", "Twin", "Zeta", "delta", "Gamma"}},
		}
		for _, tt := range tests {
			t.Run(string(tt.sort), func(t *testing.T) {
				ms, total := list(t, store.MovieFilter{Sort: tt.sort})
				if got := titles(ms); !slices.Equal(got, tt.want) {
					t.Fatalf("order = %q, want %q", got, tt.want)
				}
				if total != int64(len(seed)) {
					t.Fatalf("total = %d", total)
				}
				if tt.sort == store.SortTitleAsc || tt.sort == store.SortYearDesc || tt.sort == store.SortYearAsc {
					twinOrder(t, ms, twins)
				}
			})
		}
	})

	t.Run("every sort pages without overlap or gaps", func(t *testing.T) {
		for _, sort := range []store.MovieSort{store.SortAddedDesc, store.SortTitleAsc, store.SortYearDesc, store.SortYearAsc} {
			full, _ := list(t, store.MovieFilter{Sort: sort})
			var paged []model.Movie
			for off := int64(0); off < int64(len(seed)); off += 3 {
				ms, _ := list(t, store.MovieFilter{Sort: sort, Limit: 3, Offset: off})
				paged = append(paged, ms...)
			}
			if len(paged) != len(full) {
				t.Fatalf("%s: %d rows across pages, want %d", sort, len(paged), len(full))
			}
			for i := range full {
				if paged[i].ID != full[i].ID {
					t.Fatalf("%s: pages differ from the full list at %d", sort, i)
				}
			}
		}
	})

	t.Run("year filters are inclusive and drop movies without a year", func(t *testing.T) {
		tests := []struct {
			name     string
			from, to *int
			want     []string
		}{
			{"range", year(1999), year(2010), []string{"Alpha", "beta", "Twin", "Twin"}},
			{"from only", year(2050), nil, []string{"Zeta"}},
			{"to only", nil, year(1999), []string{"Epsilon", "Alpha"}},
			{"one year", year(2010), year(2010), []string{"Twin", "Twin"}},
			{"bounds of the allowed range", year(1880), year(2100), []string{"Epsilon", "Alpha", "beta", "Twin", "Twin", "Zeta"}},
			{"nothing in range", year(1900), year(1950), nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				ms, total := list(t, store.MovieFilter{YearFrom: tt.from, YearTo: tt.to, Sort: store.SortYearAsc})
				if got := titles(ms); !slices.Equal(got, tt.want) {
					t.Fatalf("titles = %q, want %q", got, tt.want)
				}
				if total != int64(len(tt.want)) {
					t.Fatalf("total = %d, want %d (the count must use the same filter)", total, len(tt.want))
				}
			})
		}
	})

	t.Run("year combines with search", func(t *testing.T) {
		q := "a" // Alpha, beta, Gamma, delta, Zeta
		ms, total := list(t, store.MovieFilter{Title: &q, YearFrom: year(2000), Sort: store.SortTitleAsc})
		if got := titles(ms); !slices.Equal(got, []string{"beta", "Zeta"}) || total != 2 {
			t.Fatalf("titles = %q, total %d", got, total)
		}
	})

	t.Run("unknown sort is an error, not a default", func(t *testing.T) {
		if _, _, err := st.ListMovies(t.Context(), store.MovieFilter{Sort: "id; DROP TABLE movies", Limit: 1}); err == nil {
			t.Fatal("unknown sort accepted")
		}
	})
}
