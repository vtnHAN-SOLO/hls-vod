package service

import (
	"crypto/md5" // #nosec G501 -- nginx secure_link_md5 only speaks MD5; see MediaSigner
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mediattl"
)

// Lifetimes of signed media URLs; see internal/mediattl, which the
// transcoder's cleanup delay is derived from too.
const (
	PlaybackURLTTL = mediattl.PlaybackURLTTL
	PosterURLTTL   = mediattl.PosterURLTTL
)

// MediaSigner signs media-delivery URLs:
//
//	/media/{token}/{expires}/{movie_id}/{path inside library/{movie_id}/}
//
// token = base64url(md5("{expires}/{movie_id} {key}")), unpadded, which is
// what nginx's secure_link_md5 "$exp/$movie_id {key}" checks. The token
// covers the whole movie folder, so one token serves every playlist,
// segment, poster and thumbnail of that movie, and hls.js's relative rung
// and segment URLs inherit it from the master playlist's path.
//
// MD5 because nginx's stock secure_link module offers nothing else. The key
// goes last: md5(key || message) would let anyone holding one valid token
// extend the message (length extension) and mint others; with the key at
// the end the attacker cannot compute the digest without it. The message is
// not chosen by the client (the api signs only ids it serves), and tokens
// are short-lived, which is what makes MD5 acceptable here.
//
// Not bound to the client IP: mobile networks and the reverse proxy change it.
type MediaSigner struct {
	key []byte
	now func() time.Time
	// ttl, when non-zero, replaces every lifetime (DEV_MEDIA_URL_TTL).
	ttl time.Duration
}

// NewMediaSigner returns a signer for key, which config has validated
// (config.ValidMediaSigningKey). devTTL is 0 except in local development.
func NewMediaSigner(key string, devTTL time.Duration) (*MediaSigner, error) {
	if key == "" {
		return nil, errors.New("media signing key is empty")
	}
	if devTTL < 0 {
		return nil, errors.New("media URL TTL must not be negative")
	}
	return &MediaSigner{key: []byte(key), now: time.Now, ttl: devTTL}, nil
}

// Token is the secure_link token for one movie folder until expires (Unix
// seconds).
func (s *MediaSigner) Token(movieID string, expires int64) string {
	msg := make([]byte, 0, 64+len(s.key))
	msg = strconv.AppendInt(msg, expires, 10)
	msg = append(msg, '/')
	msg = append(msg, movieID...)
	msg = append(msg, ' ')
	msg = append(msg, s.key...)
	sum := md5.Sum(msg) // #nosec G401 -- secret-suffix MD5 is what nginx secure_link_md5 verifies; see the type comment
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Expires is when a URL signed now with ttl stops working, in Unix seconds:
// now+ttl rounded up to mediattl.Step(ttl).
func (s *MediaSigner) Expires(ttl time.Duration) int64 {
	ttl = mediattl.Effective(ttl, s.ttl)
	step := int64(mediattl.Step(ttl).Seconds())
	at := s.now().Add(ttl).Unix()
	return (at + step - 1) / step * step
}

// URL is the signed media-delivery URL of rel inside the movie's library
// dir, valid for ttl, or "" when rel is unsafe (see mediapath.Rel): a URL
// never climbs out of the signed folder. movieID must be a validated,
// canonical (lowercase) UUID, the form nginx accepts and the token covers.
func (s *MediaSigner) URL(movieID, rel string, ttl time.Duration) string {
	exp := s.Expires(ttl)
	return mediapath.URL(MediaPrefix+s.Token(movieID, exp)+"/"+strconv.FormatInt(exp, 10)+"/", movieID, rel)
}
