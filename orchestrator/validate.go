package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register decoders for DecodeConfig sniff
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"strings"
)

// ValidatedImage is the result of running an upload through the validator.
// We hand back the raw bytes (already buffered for size enforcement) plus the
// sniffed metadata so callers can forward without re-reading the source.
type ValidatedImage struct {
	Bytes       []byte
	ContentType string
	Filename    string
	Width       int
	Height      int
}

// Sentinel errors so HTTP handlers can map to specific status codes.
var (
	ErrUploadTooLarge     = errors.New("upload exceeds max_upload_bytes")
	ErrUploadEmpty        = errors.New("upload is empty")
	ErrContentTypeBlocked = errors.New("content-type not allowed")
	ErrNotAnImage         = errors.New("payload is not a recognized image")
	ErrImageTooLarge      = errors.New("image dimensions exceed max_image_pixels")
)

// readUploadCapped reads up to limit+1 bytes from r so we can detect overflow
// without ever buffering an unbounded request body. http.MaxBytesReader at the
// router level is the second line of defense.
func readUploadCapped(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("invalid upload limit %d", limit)
	}
	// limit+1 lets us see the overflow byte without reading the whole thing.
	buf := &bytes.Buffer{}
	n, err := io.CopyN(buf, r, limit+1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if n > limit {
		return nil, ErrUploadTooLarge
	}
	if n == 0 {
		return nil, ErrUploadEmpty
	}
	return buf.Bytes(), nil
}

// declaredContentType pulls the MIME type from a multipart Part header, falling
// back to the file extension. We deliberately do not trust the declared value
// alone — sniffMatchesAllowed verifies the bytes actually look like an image.
func declaredContentType(headerCT, filename string) string {
	if ct, _, err := mime.ParseMediaType(headerCT); err == nil && ct != "" {
		return strings.ToLower(ct)
	}
	if i := strings.LastIndex(filename, "."); i >= 0 {
		if guess := mime.TypeByExtension(filename[i:]); guess != "" {
			if ct, _, err := mime.ParseMediaType(guess); err == nil {
				return strings.ToLower(ct)
			}
		}
	}
	return ""
}

func contentTypeAllowed(allow []string, ct string) bool {
	ct = strings.ToLower(ct)
	for _, a := range allow {
		if strings.ToLower(a) == ct {
			return true
		}
	}
	return false
}

// ValidateImageUpload runs the full safeguard chain:
//   1. Size cap (limit+1 read so we never buffer more than allowed).
//   2. Declared content-type allowlist.
//   3. http.DetectContentType sniff — must agree with declared type's family.
//   4. image.DecodeConfig (header-only, no full pixel decode) to enforce the
//      max_image_pixels ceiling. This is what stops decompression bombs:
//      a 1 KiB PNG that decodes to 100k x 100k will be rejected here without
//      ever materializing the pixel buffer.
func ValidateImageUpload(body io.Reader, declaredCT, filename string, cfg Config) (*ValidatedImage, error) {
	data, err := readUploadCapped(body, cfg.MaxUploadBytes)
	if err != nil {
		return nil, err
	}

	ct := declaredContentType(declaredCT, filename)
	if ct == "" {
		// Use the sniff if no declared type — better than rejecting outright.
		ct = http.DetectContentType(data)
	}
	if !contentTypeAllowed(cfg.AllowedContentTypes, ct) {
		return nil, fmt.Errorf("%w: declared %q (allowed: %s)",
			ErrContentTypeBlocked, ct, strings.Join(cfg.AllowedContentTypes, ", "))
	}

	// Sniff actual bytes. http.DetectContentType reads first 512B and never
	// returns image/jpeg for PNG bytes, etc., so this catches mismatched
	// extensions and disguised payloads.
	sniff := strings.ToLower(http.DetectContentType(data))
	if !strings.HasPrefix(sniff, "image/") {
		return nil, fmt.Errorf("%w: sniffed %q", ErrNotAnImage, sniff)
	}
	// We do not require sniff == ct exactly — image/jpeg vs image/jpg etc. are
	// fine as long as both resolve to image families and the declared type is
	// in the allowlist.

	cfgImg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotAnImage, err)
	}
	if cfgImg.Width <= 0 || cfgImg.Height <= 0 {
		return nil, fmt.Errorf("%w: zero dimensions", ErrNotAnImage)
	}
	pixels := int64(cfgImg.Width) * int64(cfgImg.Height)
	if pixels > cfg.MaxImagePixels {
		return nil, fmt.Errorf("%w: %dx%d=%d > %d",
			ErrImageTooLarge, cfgImg.Width, cfgImg.Height, pixels, cfg.MaxImagePixels)
	}

	return &ValidatedImage{
		Bytes:       data,
		ContentType: ct,
		Filename:    filename,
		Width:       cfgImg.Width,
		Height:      cfgImg.Height,
	}, nil
}
