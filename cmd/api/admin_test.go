package main

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/oidctest"
)

// adminCall sends method path with token and an optional JSON body.
func adminCall(t *testing.T, base, method, path, token, body string) httpResult {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return do(t, req)
}

func errCode(r httpResult) string {
	e, _ := r.json["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// The PR's verify steps 1, 2 and the hiding half of 3, against real
// Postgres and the real wiring: roles and the database check, disabling,
// the last-admin rule under a race, retry and delete.
func TestAdminEndToEnd(t *testing.T) {
	e, p, st := googleE2E(t, true)
	base, ctx := e.base, t.Context()
	adaPW, testerPW, annPW := randomSecret(18), randomSecret(18), randomSecret(18)
	ada := account(t, st, "ada", "ada@example.com", adaPW, true)
	tester := account(t, st, "tester1", "tester1@example.com", testerPW, true)
	ann := account(t, st, "ann", "ann@example.com", annPW, true)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE users SET role = 'admin' WHERE id = $1`, ada.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.LinkGoogle(ctx, tester.ID, "sub-tester1", "tester1@example.com"); err != nil {
		t.Fatal(err)
	}
	h, _ := loginAs(t, base, "ada", adaPW, "Firefox")
	tk, testerCookie := loginAs(t, base, "tester1", testerPW, "Chrome")
	users := "/api/v1/admin/users"
	role := func(token, id, r string) httpResult {
		return adminCall(t, base, http.MethodPut, users+"/"+id+"/role", token, `{"role":"`+r+`"}`)
	}

	// 1. A user is refused; made admin, the database says so before their
	// token does; demoted again, a token that still says admin is refused.
	if r := adminCall(t, base, http.MethodGet, users, tk, ""); r.status != http.StatusForbidden {
		t.Fatalf("user on /admin = %d", r.status)
	}
	if r := role(h, tester.ID, "admin"); r.status != http.StatusOK || r.json["role"] != "admin" {
		t.Fatalf("make admin = %d %s", r.status, r.body)
	}
	if r := adminCall(t, base, http.MethodGet, users+"?q=tester", tk, ""); r.status != http.StatusOK || r.json["total"] != 1.0 {
		t.Fatalf("new admin, old token = %d %s", r.status, r.body)
	}
	rf := post(t, base+"/api/v1/auth/refresh", "", []*http.Cookie{testerCookie})
	if rf.status != http.StatusOK {
		t.Fatalf("refresh = %d", rf.status)
	}
	testerCookie = refreshCookie(t, rf)
	adminToken := rf.json["access_token"].(string)
	if rf.json["user"].(map[string]any)["role"] != "admin" {
		t.Fatalf("refreshed role = %v", rf.json["user"])
	}
	if r := role(h, tester.ID, "user"); r.status != http.StatusOK {
		t.Fatalf("make user = %d %s", r.status, r.body)
	}
	if r := adminCall(t, base, http.MethodGet, users, adminToken, ""); r.status != http.StatusForbidden {
		t.Fatalf("demoted admin with a token that says admin = %d", r.status)
	}
	if r := role(h, ada.ID, "user"); r.status != http.StatusConflict || errCode(r) != "self_change" {
		t.Fatalf("demote yourself = %d %s", r.status, r.body)
	}
	if r := adminCall(t, base, http.MethodPost, users+"/"+ada.ID+"/disable", h, ""); r.status != http.StatusConflict || errCode(r) != "self_change" {
		t.Fatalf("disable yourself = %d %s", r.status, r.body)
	}

	// 2. Disable: the other browser's session ends at its next refresh;
	// login refused after the right password; Google refused; enable.
	if r := adminCall(t, base, http.MethodPost, users+"/"+tester.ID+"/disable", h, ""); r.status != http.StatusOK || r.json["disabled_at"] == nil {
		t.Fatalf("disable = %d %s", r.status, r.body)
	}
	if r := post(t, base+"/api/v1/auth/refresh", "", []*http.Cookie{testerCookie}); r.status != http.StatusUnauthorized {
		t.Fatalf("refresh of a disabled user = %d", r.status)
	}
	if r := post(t, base+"/api/v1/auth/login", `{"username":"tester1","password":"`+testerPW+`"}`, nil); r.status != http.StatusForbidden ||
		errCode(r) != "account_disabled" {
		t.Fatalf("login = %d %s", r.status, r.body)
	}
	if _, status := login(t, e, "tester1", randomSecret(18)); status != http.StatusUnauthorized {
		t.Fatalf("wrong password of a disabled user = %d", status)
	}
	p.Next(oidctest.Identity{Subject: "sub-tester1", Email: "tester1@example.com", EmailVerified: true}, oidctest.SpoilNone)
	if page := newBrowser(t, e).signIn(); page != "/login?google_error=account_disabled" {
		t.Fatalf("Google sign-in of a disabled user ends on %s", page)
	}
	if r := post(t, base+"/api/v1/auth/forgot-password", `{"email":"tester1@example.com","turnstile_token":"good"}`, nil); r.status != http.StatusAccepted {
		t.Fatalf("forgot = %d", r.status)
	}
	e.resend.none(t)
	if r := adminCall(t, base, http.MethodPost, users+"/"+tester.ID+"/enable", h, ""); r.status != http.StatusOK || r.json["disabled_at"] != nil {
		t.Fatalf("enable = %d %s", r.status, r.body)
	}
	if _, status := login(t, e, "tester1", testerPW); status != http.StatusOK {
		t.Fatalf("login after enable = %d", status)
	}

	// A disabled admin is refused, whatever the token says.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE users SET role = 'admin' WHERE id = $1`, ann.ID); err != nil {
		t.Fatal(err)
	}
	a, _ := loginAs(t, base, "ann", annPW, "Safari")
	if r := adminCall(t, base, http.MethodPost, users+"/"+ann.ID+"/disable", h, ""); r.status != http.StatusOK {
		t.Fatalf("disable ann = %d %s", r.status, r.body)
	}
	if r := adminCall(t, base, http.MethodGet, users, a, ""); r.status != http.StatusForbidden {
		t.Fatalf("disabled admin = %d", r.status)
	}
	if r := adminCall(t, base, http.MethodPost, users+"/"+ann.ID+"/enable", h, ""); r.status != http.StatusOK {
		t.Fatal(r.status)
	}

	// Two admins demoting each other at the same moment: one must fail and
	// one admin is left. When the two transactions overlap the second is
	// last_admin; when one commits before the other request is checked,
	// that admin is already demoted: 403.
	for range 5 {
		if _, err := e.db.Pool.Exec(ctx, `UPDATE users SET role = 'admin' WHERE id = ANY($1)`, []string{ada.ID, ann.ID}); err != nil {
			t.Fatal(err)
		}
		a, _ = loginAs(t, base, "ann", annPW, "Safari")
		var wg sync.WaitGroup
		res := make([]httpResult, 2)
		wg.Go(func() { res[0] = role(h, ann.ID, "user") })
		wg.Go(func() { res[1] = role(a, ada.ID, "user") })
		wg.Wait()
		ok, refused := 0, 0
		for _, r := range res {
			switch {
			case r.status == http.StatusOK:
				ok++
			case r.status == http.StatusConflict && errCode(r) == "last_admin", r.status == http.StatusForbidden:
				refused++
			default:
				t.Fatalf("unexpected %d %s", r.status, r.body)
			}
		}
		var admins int
		if err := e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin'`).Scan(&admins); err != nil {
			t.Fatal(err)
		}
		if ok != 1 || refused != 1 || admins != 1 {
			t.Fatalf("results %v; want one success and one refusal", res)
		}
	}
	// Whoever is left is the admin; make it ada for what follows.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE users SET role = CASE WHEN id = $1 THEN 'admin' ELSE 'user' END`, ada.ID); err != nil {
		t.Fatal(err)
	}

	// 3, 4. Movies: deleting hides a movie from users at once; retry only a
	// failed one, once.
	var ready, failed string
	if err := e.db.Pool.QueryRow(ctx, `INSERT INTO movies (title, source_path, status) VALUES ('Ready', 'sources/r.mkv', 'ready') RETURNING id::text`).
		Scan(&ready); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `
		WITH m AS (INSERT INTO movies (title, source_path, status) VALUES ('Broken', 'sources/b.mkv', 'failed') RETURNING id)
		INSERT INTO transcode_jobs (movie_id, status, error_msg) SELECT id, 'failed', 'ffprobe: invalid data' FROM m RETURNING movie_id::text`).
		Scan(&failed); err != nil {
		t.Fatal(err)
	}
	tk, _ = loginAs(t, base, "tester1", testerPW, "Chrome")
	if status := postAuthed(t, base+"/api/v1/progress", tk, `{"movie_id":"`+ready+`","position_sec":30}`); status != http.StatusNoContent {
		t.Fatalf("progress = %d", status)
	}
	if r := adminCall(t, base, http.MethodDelete, "/api/v1/admin/movies/"+ready, h, ""); r.status != http.StatusAccepted {
		t.Fatalf("delete = %d %s", r.status, r.body)
	}
	if _, list := getJSON(t, base+"/api/v1/movies", tk); list["total"] != 0.0 {
		t.Fatalf("users still see %v", list)
	}
	for _, path := range []string{"/api/v1/movies/" + ready, "/api/v1/movies/" + ready + "/playback"} {
		if status, _ := getJSON(t, base+path, tk); status != http.StatusNotFound {
			t.Fatalf("%s for a user = %d", path, status)
		}
	}
	if _, cw := getJSON(t, base+"/api/v1/continue-watching", tk); len(cw["items"].([]any)) != 0 {
		t.Fatalf("continue watching = %v", cw)
	}
	if status, _ := getJSON(t, base+"/api/v1/movies/"+ready+"/playback", h); status != http.StatusConflict {
		t.Fatalf("playback of a deleting movie for an admin = %d", status)
	}
	if _, list := getJSON(t, base+"/api/v1/admin/movies?status=deleting", h); list["total"] != 1.0 {
		t.Fatalf("admin deleting list = %v", list)
	}

	retry := "/api/v1/admin/movies/" + failed + "/retry"
	if r := adminCall(t, base, http.MethodPost, "/api/v1/admin/movies/"+ready+"/retry", h, ""); r.status != http.StatusConflict {
		t.Fatalf("retry a deleting movie = %d", r.status)
	}
	if r := adminCall(t, base, http.MethodPost, retry, h, ""); r.status != http.StatusAccepted {
		t.Fatalf("retry = %d %s", r.status, r.body)
	}
	if r := adminCall(t, base, http.MethodPost, retry, h, ""); r.status != http.StatusConflict {
		t.Fatalf("second retry = %d %s", r.status, r.body)
	}
	if _, jobs := getJSON(t, base+"/api/v1/admin/jobs?status=queued", h); jobs["total"] != 1.0 {
		t.Fatalf("queued jobs = %v", jobs)
	}
	if r := adminCall(t, base, http.MethodDelete, "/api/v1/admin/movies/"+failed, h, ""); r.status != http.StatusConflict || errCode(r) != "job_active" {
		t.Fatalf("delete with a queued job = %d %s", r.status, r.body)
	}

	// Retranscode a ready movie: it keeps playing its version.
	var playing, version string
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO movies (title, source_path, status, library_path)
		VALUES ('Playing', 'sources/p.mkv', 'ready', 'library/x') RETURNING id::text, gen_random_uuid()::text`).
		Scan(&playing, &version); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `UPDATE movies SET library_path = 'library/' || id::text || '/' || $2 WHERE id = $1`,
		playing, version); err != nil {
		t.Fatal(err)
	}
	playlist := func() string {
		t.Helper()
		status, pb := getJSON(t, base+"/api/v1/movies/"+playing+"/playback", tk)
		if status != http.StatusOK {
			t.Fatalf("playback = %d %v", status, pb)
		}
		return pb["playlist_url"].(string)
	}
	if p := playlist(); !strings.HasSuffix(p, "/"+playing+"/"+version+"/master.m3u8") {
		t.Fatalf("playlist = %s, want the stored version", p)
	}
	retranscode := "/api/v1/admin/movies/" + playing + "/retranscode"
	if r := adminCall(t, base, http.MethodPost, retranscode, tk, ""); r.status != http.StatusForbidden {
		t.Fatalf("retranscode as a user = %d", r.status)
	}
	if r := adminCall(t, base, http.MethodPost, retranscode, h, ""); r.status != http.StatusAccepted {
		t.Fatalf("retranscode = %d %s", r.status, r.body)
	}
	if r := adminCall(t, base, http.MethodPost, retranscode, h, ""); r.status != http.StatusConflict || errCode(r) != "job_active" {
		t.Fatalf("second retranscode = %d %s", r.status, r.body)
	}
	if r := adminCall(t, base, http.MethodPost, "/api/v1/admin/movies/"+failed+"/retranscode", h, ""); r.status != http.StatusConflict ||
		errCode(r) != "not_ready" {
		t.Fatalf("retranscode a pending movie = %d %s", r.status, r.body)
	}
	if p := playlist(); !strings.HasSuffix(p, "/"+version+"/master.m3u8") {
		t.Fatalf("playlist during the job = %s, want the current version still", p)
	}
	if _, jobs := getJSON(t, base+"/api/v1/admin/jobs?status=queued", h); jobs["total"] != 2.0 {
		t.Fatalf("queued jobs = %v", jobs)
	}
}
