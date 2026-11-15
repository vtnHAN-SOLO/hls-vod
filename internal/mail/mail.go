// Package mail sends the api's mails (verification links and notices):
// through Resend's REST API, or, in local development, into the log. Mail
// goes out in the background through a bounded Outbox, so a request never
// waits for it and its response time never shows whether one was sent.
package mail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Kinds of mail, as the CHECK on outgoing_mail.kind allows them.
const (
	KindVerify        = "verify"
	KindAccountExists = "account_exists"
	KindReset         = "reset"
	KindGoogleLinked  = "google_linked"
)

// Message is one mail. Text and HTML carry the same content.
type Message struct {
	Kind    string
	To      string // one lowercase address
	Subject string
	Text    string
	HTML    string
}

// Sender delivers one message.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// AddressHash is how outgoing_mail stores an address: the SHA-256 of its
// lowercase form, so the limits work without keeping addresses.
func AddressHash(addr string) []byte {
	sum := sha256.Sum256([]byte(strings.ToLower(addr)))
	return sum[:]
}

// redact is what logs say about an address: its domain only.
func redact(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return "…" + addr[i:]
	}
	return "…"
}

// ResendURL is Resend's send-email endpoint.
const ResendURL = "https://api.resend.com/emails"

// ResendTimeout bounds one call to Resend.
const ResendTimeout = 10 * time.Second

// Resend sends through Resend's REST API with an API key.
type Resend struct {
	key, from, endpoint string
	client              *http.Client
}

// NewResend returns a Resend sender. endpoint "" means ResendURL; tests pass
// an httptest server.
func NewResend(apiKey, from, endpoint string) *Resend {
	if endpoint == "" {
		endpoint = ResendURL
	}
	return &Resend{key: apiKey, from: from, endpoint: endpoint, client: &http.Client{Timeout: ResendTimeout}}
}

type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
	HTML    string   `json:"html"`
}

// Send posts m to Resend. The error never contains the message (which may
// carry a token), the address or the key: only the status and Resend's error
// name.
func (r *Resend) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(resendRequest{From: r.from, To: []string{m.To}, Subject: m.Subject, Text: m.Text, HTML: m.HTML})
	if err != nil {
		return fmt.Errorf("encoding mail: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, ResendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building Resend request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("calling Resend: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 == 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	var e struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	return fmt.Errorf("resend answered %d (%s)", resp.StatusCode, e.Name)
}

// Log "sends" by writing the whole mail, link and all, to the log: local
// development only (config refuses MAIL_BACKEND=log off loopback).
type Log struct{ log *slog.Logger }

// NewLog returns the log sender.
func NewLog(log *slog.Logger) *Log { return &Log{log: log} }

// Send logs m.
func (l *Log) Send(ctx context.Context, m Message) error {
	l.log.InfoContext(ctx, "mail not sent (MAIL_BACKEND=log)",
		"kind", m.Kind, "to", m.To, "subject", m.Subject, "text", m.Text)
	return nil
}
