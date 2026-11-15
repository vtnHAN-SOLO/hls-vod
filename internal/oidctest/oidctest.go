// Package oidctest is a fake OpenID Connect provider for tests: discovery,
// JWKS, an authorization endpoint that approves at once, and a token
// endpoint that checks the client and PKCE and returns an RS256 ID token.
// The next sign-in's identity, and ways to spoil its ID token, are set by
// the test. Never imported by production code.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// Identity is who the next sign-in is.
type Identity struct {
	Subject       string
	Email         string
	EmailVerified bool
}

// Spoil makes the next ID token invalid in one way.
type Spoil int

// The ways an ID token can be spoiled.
const (
	SpoilNone      Spoil = iota
	SpoilSignature       // signed with a key that is not in the JWKS
	SpoilAudience        // aud is another client
	SpoilIssuer          // iss is another issuer
	SpoilExpired         // exp is in the past
	SpoilNonce           // nonce differs from the one asked for
)

// Provider is the fake. Its URL is the issuer.
type Provider struct {
	URL                    string
	ClientID, ClientSecret string

	key, otherKey *rsa.PrivateKey

	mu    sync.Mutex
	next  Identity
	spoil Spoil
	codes map[string]grant
}

type grant struct {
	id                         Identity
	spoil                      Spoil
	nonce, challenge, redirect string
}

// New starts a provider for a client with a random id and secret.
func New(t *testing.T) *Provider {
	t.Helper()
	p := &Provider{ClientID: random(t), ClientSecret: random(t), codes: map[string]grant{}}
	var err error
	if p.key, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	if p.otherKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /auth", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p.URL = srv.URL
	return p
}

// Next sets who the next sign-in is, and how its ID token is spoiled.
func (p *Provider) Next(id Identity, spoil Spoil) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next, p.spoil = id, spoil
}

func random(t *testing.T) string {
	t.Helper()
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer": p.URL, "authorization_endpoint": p.URL + "/auth", "token_endpoint": p.URL + "/token",
		"jwks_uri": p.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
		"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
	})
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := p.key.PublicKey
	writeJSON(w, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

// authorize approves at once and sends the browser back with a code.
func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "no randomness", http.StatusInternalServerError)
		return
	}
	code := base64.RawURLEncoding.EncodeToString(b)
	p.mu.Lock()
	p.codes[code] = grant{id: p.next, spoil: p.spoil, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
	p.mu.Unlock()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	bq := back.Query()
	bq.Set("code", code)
	bq.Set("state", q.Get("state"))
	back.RawQuery = bq.Encode()
	// #nosec G710 -- a fake provider in tests: back is the redirect_uri the client sent, as a real provider redirects after checking it
	http.Redirect(w, r, back.String(), http.StatusFound)
}

// token checks the client, the code, the redirect URI and the PKCE
// verifier, and returns the ID token. Each code works once.
func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.FormValue("client_id"), r.FormValue("client_secret")
	}
	if id != p.ClientID || secret != p.ClientSecret {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"error": "invalid_client"})
		return
	}
	p.mu.Lock()
	g, found := p.codes[r.FormValue("code")]
	delete(p.codes, r.FormValue("code"))
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
	if !found || r.FormValue("grant_type") != "authorization_code" || r.FormValue("redirect_uri") != g.redirect ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid_grant"})
		return
	}
	writeJSON(w, map[string]any{"access_token": "fake-access", "token_type": "Bearer", "expires_in": 3600, "id_token": p.idToken(g)})
}

func (p *Provider) idToken(g grant) string {
	now := time.Now()
	claims := map[string]any{
		"iss": p.URL, "aud": p.ClientID, "sub": g.id.Subject, "email": g.id.Email, "email_verified": g.id.EmailVerified,
		"nonce": g.nonce, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
	key := p.key
	switch g.spoil {
	case SpoilSignature:
		key = p.otherKey
	case SpoilAudience:
		claims["aud"] = "another-client"
	case SpoilIssuer:
		claims["iss"] = "https://issuer.example"
	case SpoilExpired:
		claims["iat"], claims["exp"] = now.Add(-2*time.Hour).Unix(), now.Add(-time.Hour).Unix()
	case SpoilNonce:
		claims["nonce"] = "another-nonce"
	case SpoilNone:
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	payload, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		panic(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}
