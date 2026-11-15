package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func randomPassword() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func TestUserAdmin(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	hasher := auth.NewHasher(1)

	type result struct {
		code     int
		out, err string
	}
	exec := func(t *testing.T, password string, args ...string) result {
		t.Helper()
		calls := 0
		src := func() (string, error) {
			calls++
			if password == "" {
				return "", errors.New("no password on stdin")
			}
			return password, nil
		}
		var out, errOut bytes.Buffer
		code := run(t.Context(), args, st, hasher, src, &out, &errOut)
		if calls > 1 {
			t.Fatalf("the password was asked for %d times", calls)
		}
		return result{code, out.String(), errOut.String()}
	}
	verify := func(t *testing.T, username, password string) bool {
		t.Helper()
		u, err := st.UserByUsername(t.Context(), username)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := hasher.Verify(t.Context(), u.PasswordHash, password)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	pw := randomPassword()
	t.Run("add a user and an admin", func(t *testing.T) {
		if r := exec(t, pw, "add", "Alice"); r.code != 0 || !strings.Contains(r.out, "created alice (role user") {
			t.Fatalf("add = %+v", r)
		}
		if !verify(t, "alice", pw) {
			t.Fatal("the stored hash does not verify the password")
		}
		if r := exec(t, randomPassword(), "add", "--role", "admin", "root"); r.code != 0 {
			t.Fatalf("add admin = %+v", r)
		}
		if u, _ := st.UserByUsername(t.Context(), "root"); u.Role != model.RoleAdmin {
			t.Fatalf("role = %s", u.Role)
		}
		u, _ := st.UserByUsername(t.Context(), "alice")
		if !strings.HasPrefix(u.PasswordHash, "$argon2id$v=19$m=19456,t=2,p=1$") || strings.Contains(u.PasswordHash, pw) {
			t.Fatalf("stored hash = %q", u.PasswordHash)
		}
	})

	t.Run("refused", func(t *testing.T) {
		for name, tt := range map[string]struct {
			password string
			args     []string
			code     int
			msg      string
		}{
			"duplicate":                 {randomPassword(), []string{"add", "alice"}, 1, "already exists"},
			"password as an argument":   {randomPassword(), []string{"add", "bob", "hunter2hunter2"}, 2, "never an argument"},
			"password flag":             {randomPassword(), []string{"add", "--password", "x", "bob"}, 2, "flag provided but not defined"},
			"short password":            {"short", []string{"add", "bob"}, 1, "12 to 1024 characters"},
			"no password on stdin":      {"", []string{"add", "bob"}, 1, "no password"},
			"bad username":              {randomPassword(), []string{"add", "bob smith"}, 2, "username"},
			"username with a slash":     {randomPassword(), []string{"add", "../bob"}, 2, "username"},
			"unknown role":              {randomPassword(), []string{"add", "--role", "root", "bob"}, 2, "role must be"},
			"set-password unknown user": {randomPassword(), []string{"set-password", "nobody"}, 1, `no user "nobody"`},
			"set-role unknown role":     {"", []string{"set-role", "alice", "superuser"}, 2, "role must be"},
			"set-role missing argument": {"", []string{"set-role", "alice"}, 2, "set-role takes"},
			"list with arguments":       {"", []string{"list", "x"}, 2, "no arguments"},
			"no command":                {"", nil, 2, "no command"},
			"unknown command":           {"", []string{"delete", "alice"}, 2, "unknown command"},
		} {
			r := exec(t, tt.password, tt.args...)
			if r.code != tt.code || !strings.Contains(r.err, tt.msg) {
				t.Errorf("%s: code %d, stderr %q; want %d with %q", name, r.code, r.err, tt.code, tt.msg)
			}
		}
		if _, err := st.UserByUsername(t.Context(), "bob"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("a refused add created the user")
		}
	})

	t.Run("set-password replaces it and ends every session", func(t *testing.T) {
		u, _ := st.UserByUsername(t.Context(), "alice")
		tokHash := make([]byte, 32)
		_, _ = rand.Read(tokHash)
		if _, err := st.CreateRefreshToken(t.Context(), u.ID, store.NewRefresh{Hash: tokHash, TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
		next := randomPassword()
		if r := exec(t, next, "set-password", "alice"); r.code != 0 || !strings.Contains(r.out, "revoked") {
			t.Fatalf("set-password = %+v", r)
		}
		if verify(t, "alice", pw) || !verify(t, "alice", next) {
			t.Fatal("the password did not change")
		}
		if _, _, err := st.RotateRefreshToken(t.Context(), tokHash, store.NewRefresh{Hash: make([]byte, 32), TTL: time.Hour}); err == nil {
			t.Fatal("a session survived the password change")
		}
	})

	t.Run("set-role and list", func(t *testing.T) {
		if r := exec(t, "", "set-role", "alice", "admin"); r.code != 0 {
			t.Fatalf("set-role = %+v", r)
		}
		r := exec(t, "", "list")
		if r.code != 0 {
			t.Fatalf("list = %+v", r)
		}
		lines := strings.Split(strings.TrimSpace(r.out), "\n")
		if len(lines) != 3 || !strings.HasPrefix(lines[0], "USERNAME") ||
			!strings.HasPrefix(lines[1], "alice") || !strings.Contains(lines[1], "admin") || !strings.HasPrefix(lines[2], "root") {
			t.Fatalf("list output:\n%s", r.out)
		}
		if strings.Contains(r.out, "argon2id") {
			t.Fatal("list prints password hashes")
		}
	})
}

// Not a terminal: the first line of stdin is the password.
func TestStdinPassword(t *testing.T) {
	for name, tt := range map[string]struct {
		input, want string
		ok          bool
	}{
		"one line":           {"secret line one\n", "secret line one", true},
		"crlf":               {"secret line one\r\n", "secret line one", true},
		"no newline":         {"secret line one", "secret line one", true},
		"only the first one": {"first line here\nsecond\n", "first line here", true},
		"empty":              {"", "", false},
		"empty line":         {"\n", "", false},
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.WriteString(tt.input)
		_ = w.Close()
		got, err := stdinPassword(r, &bytes.Buffer{})()
		_ = r.Close()
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("%s: %q, %v; want %q ok=%v", name, got, err, tt.want, tt.ok)
		}
	}
}
