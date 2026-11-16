package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func randomSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func TestResend(t *testing.T) {
	key := randomSecret()
	var got resendRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"name":"missing_api_key","message":"bad key"}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got.To[0] == "reject@example.com" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"statusCode":422,"name":"validation_error","message":"reject@example.com is invalid"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	t.Cleanup(srv.Close)

	m := VerifyMessage("", "dana@example.com", "dana", "http://127.0.0.1:5173/verify-email#token=abc")
	if err := NewResend(key, "Media <noreply@example.com>", srv.URL).Send(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if got.From != "Media <noreply@example.com>" || len(got.To) != 1 || got.To[0] != "dana@example.com" ||
		got.Subject != m.Subject || !strings.Contains(got.Text, "#token=abc") || !strings.Contains(got.HTML, "#token=abc") {
		t.Fatalf("request = %+v", got)
	}

	m.To = "reject@example.com"
	err := NewResend(key, "noreply@example.com", srv.URL).Send(t.Context(), m)
	if err == nil || !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "validation_error") {
		t.Fatalf("422 = %v", err)
	}
	if s := err.Error(); strings.Contains(s, "reject@") || strings.Contains(s, "abc") || strings.Contains(s, key) {
		t.Fatalf("the error leaks the address, the token or the key: %v", err)
	}
	if err := NewResend(randomSecret(), "noreply@example.com", srv.URL).Send(t.Context(), m); err == nil {
		t.Fatal("a wrong key was accepted")
	}
}

func TestTemplatesEscape(t *testing.T) {
	m := VerifyMessage("", "x@example.com", "<b>", `https://example.com/verify-email#token="><script>`)
	if strings.Contains(m.HTML, "<script>") || strings.Contains(m.HTML, "<b>") {
		t.Fatalf("HTML not escaped: %s", m.HTML)
	}
	if e := AccountExistsMessage("", "x@example.com", "https://example.com/login"); e.Kind != KindAccountExists || !strings.Contains(e.Text, "/login") {
		t.Fatalf("account-exists mail = %+v", e)
	}
}

type fakeLedger struct {
	mu      sync.Mutex
	verdict store.MailVerdict
	err     error
	seen    [][]byte
}

func (l *fakeLedger) ReserveMail(_ context.Context, toHash []byte, _ string, perHour, _ int) (store.MailVerdict, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if perHour != PerAddressPerHour {
		return 0, errors.New("wrong per-address limit")
	}
	l.seen = append(l.seen, toHash)
	return l.verdict, l.err
}

type fakeSender struct {
	mu   sync.Mutex
	sent []Message
	err  error
	gate chan struct{}
}

func (s *fakeSender) Send(_ context.Context, m Message) error {
	if s.gate != nil {
		<-s.gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	return s.err
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestOutbox(t *testing.T) {
	const token = "SECRET-TOKEN-VALUE"
	msg := VerifyMessage("", "dana@example.com", "dana", "https://example.com/verify-email#token="+token)
	closeNow := func(t *testing.T, o *Outbox) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := o.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		name      string
		verdict   store.MailVerdict
		ledgerErr error
		sendErr   error
		wantSent  int
		wantLog   string
	}{
		{"allowed", store.MailAllowed, nil, nil, 1, "mail sent"},
		{"per-address limit", store.MailAddressLimit, nil, nil, 0, "per-address limit reached"},
		{"daily cap", store.MailDailyCap, nil, nil, 0, "daily cap reached"},
		{"ledger down", store.MailAllowed, errors.New("db down"), nil, 0, "checking the limits failed"},
		{"send fails", store.MailAllowed, nil, errors.New("resend answered 500 (internal)"), 1, "mail send failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs syncBuffer
			ledger := &fakeLedger{verdict: tt.verdict, err: tt.ledgerErr}
			sender := &fakeSender{err: tt.sendErr}
			o := NewOutbox(t.Context(), sender, ledger, 90, slog.New(slog.NewJSONHandler(&logs, nil)))
			o.Enqueue(msg)
			closeNow(t, o)
			if len(sender.sent) != tt.wantSent {
				t.Fatalf("sent %d, want %d", len(sender.sent), tt.wantSent)
			}
			if len(ledger.seen) != 1 || !bytes.Equal(ledger.seen[0], AddressHash("DANA@example.com")) {
				t.Fatalf("ledger saw %x; want the hash of the lowercase address", ledger.seen)
			}
			out := logs.String()
			if !strings.Contains(out, tt.wantLog) {
				t.Fatalf("log %q lacks %q", out, tt.wantLog)
			}
			if strings.Contains(out, token) || strings.Contains(out, "dana@") {
				t.Fatalf("the log leaks the token or the address: %s", out)
			}
		})
	}

	t.Run("a full queue drops instead of blocking; Close drains what was queued", func(t *testing.T) {
		var logs syncBuffer
		sender := &fakeSender{gate: make(chan struct{})}
		o := NewOutbox(t.Context(), sender, &fakeLedger{}, 90, slog.New(slog.NewJSONHandler(&logs, nil)))
		done := make(chan struct{})
		go func() {
			for range QueueSize + 5 { // one in the worker, QueueSize queued, the rest dropped
				o.Enqueue(msg)
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Enqueue blocked")
		}
		close(sender.gate)
		closeNow(t, o)
		if n := len(sender.sent); n < QueueSize || n > QueueSize+1 {
			t.Fatalf("sent %d", n)
		}
		if !strings.Contains(logs.String(), "queue full") {
			t.Fatal("drops not logged")
		}
		o.Enqueue(msg) // after Close: dropped, no panic
		if !strings.Contains(logs.String(), "shutting down") {
			t.Fatal("drop after Close not logged")
		}
	})

	t.Run("the log sender prints the whole mail", func(t *testing.T) {
		var logs syncBuffer
		if err := NewLog(slog.New(slog.NewJSONHandler(&logs, nil))).Send(t.Context(), msg); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(logs.String(), token) || !strings.Contains(logs.String(), "MAIL_BACKEND=log") {
			t.Fatalf("log sender: %s", logs.String())
		}
	})
}

// Every mail names the product as the deployment calls it (SITE_NAME), in
// the subject, the body and the signature; "" is the neutral default. The
// name is escaped in the HTML like any other value.
func TestSiteName(t *testing.T) {
	const link = "https://example.com/x#token=abc"
	build := func(site string) []Message {
		return []Message{
			VerifyMessage(site, "a@example.com", "ann", link),
			AccountExistsMessage(site, "a@example.com", link),
			ResetMessage(site, "a@example.com", "ann", link),
			GoogleLinkedMessage(site, "a@example.com", "ann", link),
		}
	}
	for _, tt := range []struct{ site, want, wantHTML string }{
		{"", DefaultSiteName, DefaultSiteName},
		{"Moviebox", "Moviebox", "Moviebox"},
		{"A&B <Films>", "A&B <Films>", "A&amp;B &lt;Films&gt;"},
	} {
		for _, m := range build(tt.site) {
			if !strings.Contains(m.Subject, tt.want) || !strings.Contains(m.Text, tt.want) ||
				!strings.HasSuffix(m.Text, "\n-- \n"+tt.want+"\n") || !strings.Contains(m.HTML, tt.wantHTML) {
				t.Errorf("%s with site %q: subject %q, text %q", m.Kind, tt.site, m.Subject, m.Text)
			}
			if tt.site == "A&B <Films>" && strings.Contains(m.HTML, "<Films>") {
				t.Errorf("%s: the site name reached the HTML unescaped", m.Kind)
			}
		}
	}
}
