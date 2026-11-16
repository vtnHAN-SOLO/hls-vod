// Package poster turns an uploaded image into a movie's portrait poster:
// the type read from the bytes, the size checked before decoding, a centre
// crop to exactly 2:3, and JPEG variants at fixed widths. It also defines
// where the variants are kept (Store); the Postgres implementation is in
// internal/store, and nothing else knows where the bytes live.
//
// Re-encoding from pixels drops every piece of metadata the upload carried
// (EXIF with its GPS position and camera serial, XMP, ICC profiles, PNG text
// chunks, generator tags). EXIF orientation is not applied either: a phone
// photo whose pixels are stored sideways stays sideways. Posters are made
// for screens (exports from an editor or a generator), which store their
// pixels upright, so the rule is kept simple: what the pixels show is what
// is shown.
package poster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Upload and output rules.
const (
	// MaxUploadBytes bounds the request body.
	MaxUploadBytes = 10 << 20
	// MaxPixels bounds the decoded size: a decoded image takes about 4 bytes
	// a pixel (100 MB at the limit), and the api pod has 512 MB.
	MaxPixels = 25_000_000
	// MinWidth and MinHeight: the smallest upload, the smallest variant
	// at 2:3. Any upload this large crops to at least MinWidth wide.
	MinWidth  = 300
	MinHeight = 450
	// Quality of every JPEG variant.
	Quality = 85
)

// Widths are the variants made, smallest first. Widths above the cropped
// upload are skipped (never upscaled); MinWidth guarantees the first.
var Widths = []int{300, 600, 1000}

// ValidWidth reports whether w is one of Widths.
func ValidWidth(w int) bool {
	for _, v := range Widths {
		if v == w {
			return true
		}
	}
	return false
}

// HeightFor is the height of a 2:3 variant w wide.
func HeightFor(w int) int { return w * 3 / 2 }

// Errors a client can fix: the handler answers them with 400 or 413.
var (
	ErrTooLarge    = fmt.Errorf("the image is larger than %d MB", MaxUploadBytes>>20)
	ErrUnsupported = errors.New("the image must be a JPEG, PNG or WebP file")
	ErrTooManyPx   = fmt.Errorf("the image has more than %d megapixels", MaxPixels/1_000_000)
	ErrTooSmall    = fmt.Errorf("the image must be at least %d×%d pixels", MinWidth, MinHeight)
	ErrCorrupt     = errors.New("the image could not be decoded")
)

// Type is an image format read from the bytes.
type Type string

// The accepted formats.
const (
	JPEG Type = "jpeg"
	PNG  Type = "png"
	WebP Type = "webp"
)

// Detect reads the format from the first bytes (the magic numbers), never
// from a file name or a Content-Type header, which the client chooses.
func Detect(b []byte) (Type, bool) {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return JPEG, true
	case len(b) >= 8 && bytes.Equal(b[:8], []byte("\x89PNG\r\n\x1a\n")):
		return PNG, true
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return WebP, true
	}
	return "", false
}

// Variant is one JPEG of the poster.
type Variant struct {
	Width  int
	Height int
	Bytes  []byte
}

// Poster is a processed upload: its variants, smallest first, and Hash,
// the SHA-256 of the largest one. A new upload has a new hash, so every URL
// built from it changes.
type Poster struct {
	Variants []Variant
	Hash     [sha256.Size]byte
}

// Processor processes one poster at a time: decoding and resizing take
// about 4 bytes a pixel of the upload, so two at once in a 512 MB pod is
// what must not happen.
type Processor struct {
	sem chan struct{}
}

// NewProcessor returns a Processor; one per process.
func NewProcessor() *Processor {
	return &Processor{sem: make(chan struct{}, 1)}
}

// Process checks and converts an upload, waiting while another one is
// processed (or until ctx is done). data is at most MaxUploadBytes.
func (p *Processor) Process(ctx context.Context, data []byte) (Poster, error) {
	if len(data) > MaxUploadBytes {
		return Poster{}, ErrTooLarge
	}
	t, ok := Detect(data)
	if !ok {
		return Poster{}, ErrUnsupported
	}
	// The size from the header, before a single pixel is decoded.
	cfg, err := decodeConfig(t, data)
	if err != nil {
		return Poster{}, ErrCorrupt
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return Poster{}, ErrTooManyPx
	}
	if cfg.Width < MinWidth || cfg.Height < MinHeight {
		return Poster{}, ErrTooSmall
	}

	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return Poster{}, ctx.Err()
	}
	defer func() { <-p.sem }()
	return process(t, data)
}

func decodeConfig(t Type, data []byte) (image.Config, error) {
	r := bytes.NewReader(data)
	switch t {
	case JPEG:
		return jpeg.DecodeConfig(r)
	case PNG:
		return png.DecodeConfig(r)
	default:
		return webp.DecodeConfig(r)
	}
}

func decode(t Type, data []byte) (image.Image, error) {
	r := bytes.NewReader(data)
	switch t {
	case JPEG:
		return jpeg.Decode(r)
	case PNG:
		return png.Decode(r)
	default:
		return webp.Decode(r)
	}
}

// Crop2x3 is the largest centred rectangle of exactly 2:3 inside b.
func Crop2x3(b image.Rectangle) image.Rectangle {
	k := min(b.Dx()/2, b.Dy()/3)
	w, h := 2*k, 3*k
	x := b.Min.X + (b.Dx()-w)/2
	y := b.Min.Y + (b.Dy()-h)/2
	return image.Rect(x, y, x+w, y+h)
}

// VariantWidths are the Widths not above cropped, smallest first; never
// empty (the smallest is kept even if the crop is narrower, which an upload
// of at least MinWidth×MinHeight never is).
func VariantWidths(cropped int) []int {
	ws := []int{}
	for _, w := range Widths {
		if w <= cropped {
			ws = append(ws, w)
		}
	}
	if len(ws) == 0 {
		ws = append(ws, Widths[0])
	}
	return ws
}

func process(t Type, data []byte) (Poster, error) {
	src, err := decode(t, data)
	if err != nil {
		return Poster{}, ErrCorrupt
	}
	// The decoder may disagree with its own header: check what it made.
	b := src.Bounds()
	if b.Dx() < MinWidth || b.Dy() < MinHeight || int64(b.Dx())*int64(b.Dy()) > MaxPixels {
		return Poster{}, ErrCorrupt
	}
	crop := Crop2x3(b)
	widths := VariantWidths(crop.Dx())

	// The largest from the upload, each smaller one from the one above it:
	// CatmullRom over 25 MP once, not three times.
	imgs := make([]*image.RGBA, len(widths))
	from, fromRect := src, crop
	for i := len(widths) - 1; i >= 0; i-- {
		w := widths[i]
		dst := image.NewRGBA(image.Rect(0, 0, w, HeightFor(w)))
		// JPEG has no alpha: transparent areas become black, not whatever
		// colour the transparent pixels happen to hold.
		draw.Draw(dst, dst.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
		draw.CatmullRom.Scale(dst, dst.Bounds(), from, fromRect, draw.Over, nil)
		imgs[i] = dst
		from, fromRect = dst, dst.Bounds()
	}

	out := Poster{Variants: make([]Variant, len(widths))}
	for i, img := range imgs {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: Quality}); err != nil {
			return Poster{}, fmt.Errorf("encoding poster: %w", err)
		}
		out.Variants[i] = Variant{Width: widths[i], Height: HeightFor(widths[i]), Bytes: buf.Bytes()}
	}
	out.Hash = sha256.Sum256(out.Variants[len(out.Variants)-1].Bytes)
	return out, nil
}

// ErrNotFound means no variant matches: an unknown movie or width, or a
// hash that is no longer the movie's poster.
var ErrNotFound = errors.New("poster not found")

// ErrPublished means the poster of a published movie cannot be removed:
// a published movie must have one.
var ErrPublished = errors.New("the movie is published")

// Store keeps the variants of each movie's poster.
type Store interface {
	// Put replaces the movie's poster and its hash in one step.
	// ErrNotFound for an unknown movie.
	Put(ctx context.Context, movieID string, p Poster) error
	// Get returns one variant, only while hash is the movie's poster hash;
	// ErrNotFound otherwise.
	Get(ctx context.Context, movieID string, width int, hash []byte) ([]byte, error)
	// Delete removes the poster (none is fine): ErrPublished while the
	// movie is published, ErrNotFound for an unknown movie.
	Delete(ctx context.Context, movieID string) error
}
