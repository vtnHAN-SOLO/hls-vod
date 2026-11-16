package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"testing"
)

// The PR's verify steps against real Postgres and the real wiring: an
// existing movie is hidden from users; the admin uploads a poster (a PNG),
// sees a 2:3 preview, writes a description and publishes; the user then
// sees it with its poster and can fetch the image without a token. A movie
// without a poster is refused with the reason.
func TestPostersEndToEnd(t *testing.T) {
	e, _, st := googleE2E(t, true)
	base, ctx := e.base, t.Context()
	adminPW, userPW := randomSecret(18), randomSecret(18)
	admin := account(t, st, "curator", "curator@example.com", adminPW, true)
	account(t, st, "viewer1", "viewer1@example.com", userPW, true)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE users SET role = 'admin' WHERE id = $1`, admin.ID); err != nil {
		t.Fatal(err)
	}
	var movie, bare string
	if err := e.db.Pool.QueryRow(ctx, `INSERT INTO movies (title, source_path, status) VALUES ('Existing', 'sources/e.mkv', 'ready') RETURNING id::text`).
		Scan(&movie); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `INSERT INTO movies (title, source_path, status, overview) VALUES ('No poster', 'sources/n.mkv', 'ready', 'Text') RETURNING id::text`).
		Scan(&bare); err != nil {
		t.Fatal(err)
	}
	h, _ := loginAs(t, base, "curator", adminPW, "Firefox")
	u, _ := loginAs(t, base, "viewer1", userPW, "Chrome")

	if status, _ := getJSON(t, base+"/api/v1/movies/"+movie, u); status != http.StatusNotFound {
		t.Fatalf("unpublished movie for a user = %d", status)
	}
	if r := adminCall(t, base, http.MethodPost, "/api/v1/admin/movies/"+bare+"/publish", h, ""); r.status != http.StatusConflict || errCode(r) != "missing_poster" {
		t.Fatalf("publish without a poster = %d %s", r.status, r.body)
	}

	img := image.NewRGBA(image.Rect(0, 0, 1600, 1600)) // square: cropped to 2:3
	for y := range 1600 {
		for x := range 1600 {
			img.Set(x, y, color.RGBA{R: uint8(x / 7), G: uint8(y / 7), B: 120, A: 255}) // #nosec G115 -- below 256
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, base+"/api/v1/admin/movies/"+movie+"/poster", &buf)
	req.Header.Set("Authorization", "Bearer "+h)
	req.Header.Set("Content-Type", "image/png")
	if r := do(t, req); r.status != http.StatusOK {
		t.Fatalf("upload = %d %s", r.status, r.body)
	}
	if r := adminCall(t, base, http.MethodPatch, "/api/v1/admin/movies/"+movie, h, `{"overview":"Now with a description."}`); r.status != http.StatusOK {
		t.Fatalf("overview = %d %s", r.status, r.body)
	}
	if r := adminCall(t, base, http.MethodPost, "/api/v1/admin/movies/"+movie+"/publish", h, ""); r.status != http.StatusNoContent {
		t.Fatalf("publish = %d %s", r.status, r.body)
	}

	status, m := getJSON(t, base+"/api/v1/movies/"+movie, u)
	if status != http.StatusOK {
		t.Fatalf("published movie for a user = %d", status)
	}
	p, _ := m["poster"].(map[string]any)
	variants, _ := p["variants"].([]any)
	if len(variants) != 3 {
		t.Fatalf("poster = %v", m["poster"])
	}
	largest := variants[2].(map[string]any)
	if largest["width"] != float64(1000) || largest["height"] != float64(1500) {
		t.Fatalf("largest variant = %v", largest)
	}
	// No token: the signature is the permission.
	get, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+largest["url"].(string), nil)
	resp, err := http.DefaultClient.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("poster image = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	jpg, err := jpeg.Decode(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b := jpg.Bounds(); b.Dx() != 1000 || b.Dy() != 1500 {
		t.Fatalf("served poster is %v, want 1000x1500", b)
	}
	if status, list := getJSON(t, base+"/api/v1/movies", u); status != http.StatusOK || list["total"] != float64(1) {
		t.Fatalf("user list = %d %v, want the published movie only", status, list["total"])
	}
}
