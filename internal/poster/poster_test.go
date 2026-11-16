package poster

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
)

// gradient is a w×h image whose colour changes everywhere, so a resize has
// something to do.
func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255}) // #nosec G115 -- modulo 256
		}
	}
	return img
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, gradient(w, h)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, gradient(w, h), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withEXIF puts an APP1 Exif segment carrying marker right after the JPEG's
// SOI, as a camera or editor would.
func withEXIF(jpg []byte, marker string) []byte {
	payload := append([]byte("Exif\x00\x00"), marker...)
	n := len(payload) + 2
	seg := append([]byte{0xFF, 0xE1, byte(n >> 8), byte(n)}, payload...) // #nosec G115 -- a short test segment
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

// withPNGText splices a tEXt chunk in after IHDR (the 8-byte signature and
// the 25-byte IHDR chunk), as an editor would write its name there.
func withPNGText(t *testing.T, p []byte, text string) []byte {
	t.Helper()
	chunk := pngChunk("tEXt", []byte("Software\x00"+text))
	out := append([]byte{}, p[:33]...)
	out = append(out, chunk...)
	return append(out, p[33:]...)
}

// pngChunk is one PNG chunk: length, type, data, CRC-32 of type and data.
func pngChunk(typ string, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(data))) // #nosec G115 -- test data is small
	body := append([]byte(typ), data...)
	out = append(out, body...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(body))
}

func TestDetect(t *testing.T) {
	webpFixture, err := os.ReadFile("testdata/gradient-400x600.webp")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
		want Type
		ok   bool
	}{
		{"jpeg", jpegBytes(t, 10, 10), JPEG, true},
		{"png", pngBytes(t, 10, 10), PNG, true},
		{"webp", webpFixture, WebP, true},
		{"text", []byte("this is not an image, whatever its name says"), "", false},
		{"empty", nil, "", false},
		{"gif", []byte("GIF89a\x01\x00\x01\x00"), "", false},
		{"riff but not webp", []byte("RIFF\x00\x00\x00\x00WAVEfmt "), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Detect(tt.data)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("Detect = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestProcessRefuses(t *testing.T) {
	huge := make([]byte, MaxUploadBytes+1)
	copy(huge, pngBytes(t, 10, 10))
	tests := []struct {
		name string
		data []byte
		want error
	}{
		// The name an upload had never matters: only the bytes are read.
		{"a text file named poster.png", []byte("hello, I am text"), ErrUnsupported},
		{"over 10 MB", huge, ErrTooLarge},
		{"too narrow", pngBytes(t, 299, 600), ErrTooSmall},
		{"too short", pngBytes(t, 600, 449), ErrTooSmall},
		{"over 25 megapixels, refused from the header", pngHeaderOnly(t, 5001, 5000), ErrTooManyPx},
		{"a truncated png", pngBytes(t, 400, 600)[:200], ErrCorrupt},
	}
	p := NewProcessor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := p.Process(context.Background(), tt.data); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// pngHeaderOnly is a PNG signature and IHDR claiming w×h, and nothing
// else: DecodeConfig reads the size, a decode would fail. Process must
// refuse it before trying to decode (or allocate) anything.
func pngHeaderOnly(t *testing.T, w, h int) []byte {
	t.Helper()
	ihdr := []byte{byte(w >> 24), byte(w >> 16), byte(w >> 8), byte(w), byte(h >> 24), byte(h >> 16), byte(h >> 8), byte(h), 8, 6, 0, 0, 0} // #nosec G115 -- big-endian bytes
	return append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
}

func TestProcess(t *testing.T) {
	webpFixture, err := os.ReadFile("testdata/gradient-400x600.webp")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		data       []byte
		wantWidths []int
	}{
		// A PNG named poster.jpg is still a PNG: the name is never seen.
		{"png, wide: cropped to 2:3, all widths", pngBytes(t, 2400, 1600), []int{300, 600, 1000}},
		{"jpeg, tall", jpegBytes(t, 1200, 2400), []int{300, 600, 1000}},
		{"jpeg exactly 2:3, 700 wide: no 1000 (never upscaled)", jpegBytes(t, 700, 1050), []int{300, 600}},
		{"the minimum, 300x450: only 300", pngBytes(t, 300, 450), []int{300}},
		{"300 wide but very tall: still 300", pngBytes(t, 300, 2000), []int{300}},
		{"webp 400x600", webpFixture, []int{300}},
	}
	p := NewProcessor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.Process(context.Background(), tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Variants) != len(tt.wantWidths) {
				t.Fatalf("%d variants, want widths %v", len(got.Variants), tt.wantWidths)
			}
			for i, v := range got.Variants {
				if v.Width != tt.wantWidths[i] || v.Height != tt.wantWidths[i]*3/2 {
					t.Errorf("variant %d is %dx%d, want %dx%d", i, v.Width, v.Height, tt.wantWidths[i], tt.wantWidths[i]*3/2)
				}
				img, err := jpeg.Decode(bytes.NewReader(v.Bytes))
				if err != nil {
					t.Fatalf("variant %d is not a JPEG: %v", i, err)
				}
				if b := img.Bounds(); b.Dx() != v.Width || b.Dy() != v.Height || b.Dx()*3 != b.Dy()*2 {
					t.Errorf("variant %d decodes to %v, want exactly 2:3 at %dx%d", i, b, v.Width, v.Height)
				}
			}
		})
	}
}

func TestMetadataDropped(t *testing.T) {
	const marker = "GPS-48.8584N-2.2945E-camera-serial-123"
	p := NewProcessor()
	for name, data := range map[string][]byte{
		"jpeg exif":    withEXIF(jpegBytes(t, 600, 900), marker),
		"png tEXt tag": withPNGText(t, pngBytes(t, 600, 900), marker),
	} {
		t.Run(name, func(t *testing.T) {
			if !bytes.Contains(data, []byte(marker)) {
				t.Fatal("the fixture lost its metadata")
			}
			got, err := p.Process(context.Background(), data)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range got.Variants {
				if bytes.Contains(v.Bytes, []byte(marker)) || bytes.Contains(v.Bytes, []byte("Exif")) {
					t.Fatalf("variant %d still carries the upload's metadata", v.Width)
				}
			}
		})
	}
}

func TestHashChangesWithTheImage(t *testing.T) {
	p := NewProcessor()
	a, err := p.Process(context.Background(), pngBytes(t, 600, 900))
	if err != nil {
		t.Fatal(err)
	}
	again, err := p.Process(context.Background(), pngBytes(t, 600, 900))
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Process(context.Background(), jpegBytes(t, 600, 900))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != again.Hash {
		t.Error("the same image hashed twice differently")
	}
	if a.Hash == b.Hash {
		t.Error("a different image kept the hash: its URLs would not change")
	}
}

func TestCrop2x3(t *testing.T) {
	tests := []struct {
		in, want image.Rectangle
	}{
		{image.Rect(0, 0, 2400, 1600), image.Rect(666, 0, 1732, 1599)},
		{image.Rect(0, 0, 1000, 3000), image.Rect(0, 750, 1000, 2250)},
		{image.Rect(0, 0, 301, 451), image.Rect(0, 0, 300, 450)},
	}
	for _, tt := range tests {
		got := Crop2x3(tt.in)
		if got.Dx()*3 != got.Dy()*2 {
			t.Errorf("Crop2x3(%v) = %v: not 2:3", tt.in, got)
		}
		if !got.In(tt.in) {
			t.Errorf("Crop2x3(%v) = %v: outside the image", tt.in, got)
		}
		if got.Dx() != tt.want.Dx() || got.Dy() != tt.want.Dy() {
			t.Errorf("Crop2x3(%v) = %v, want the size of %v", tt.in, got, tt.want)
		}
	}
}

func TestProcessWaitsForTheSemaphore(t *testing.T) {
	p := NewProcessor()
	p.sem <- struct{}{} // another poster is being processed
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Process(ctx, pngBytes(t, 300, 450)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled while the slot is taken", err)
	}
}
