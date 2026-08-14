// Package media owns image bytes: where they go, what is allowed in, and how
// they come back out.
//
// The Storage port is the seam a hosted provider drops into. Everything above
// it — validation, the handlers, the database rows — is written against the
// interface, so moving from local disk to Cloudinary is a change in cmd/api
// and nowhere else.
package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Stored is where an image ended up.
type Stored struct {
	// URL is what a browser fetches. Relative for local storage, absolute for
	// a CDN — clients must treat it as opaque and never build one themselves.
	URL string
	// PublicID is the storage key, kept on the row so the bytes can be deleted
	// with it. Opaque to everything above this package.
	PublicID string
}

// Storage is where image bytes live.
type Storage interface {
	Save(ctx context.Context, name string, contentType string, data []byte) (Stored, error)
	Delete(ctx context.Context, publicID string) error
}

// Limits on what may be uploaded.
const (
	// MaxImageBytes caps a single upload. Portfolio photos off a phone are
	// comfortably under this once the browser has encoded them.
	MaxImageBytes = 5 << 20 // 5 MiB

	// MaxPerService stops one service turning into an unbounded photo dump.
	MaxPerService = 12
)

// ErrUnsupportedType is returned for anything that is not an allowed image.
var ErrUnsupportedType = errors.New("media: unsupported image type")

// ErrTooLarge is returned for an upload over MaxImageBytes.
var ErrTooLarge = errors.New("media: image is too large")

// allowedTypes maps a sniffed content type to the extension it is stored as.
//
// SVG is deliberately absent, and this is not squeamishness: an SVG is a
// document that can carry script, so serving one from the API's own origin
// would be a stored-XSS hole. Only raster formats are accepted.
var allowedTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// Inspect validates bytes and reports the extension they should be stored as.
//
// The type is sniffed from the content, never taken from the request: a
// client-supplied Content-Type is a claim, not a fact, and the whole point of
// the allow-list is that it cannot be talked around.
func Inspect(data []byte) (contentType, extension string, err error) {
	if len(data) == 0 {
		return "", "", ErrUnsupportedType
	}
	if len(data) > MaxImageBytes {
		return "", "", ErrTooLarge
	}

	contentType = http.DetectContentType(data)
	// DetectContentType can return "image/jpeg; charset=..." shapes.
	if index := strings.IndexByte(contentType, ';'); index >= 0 {
		contentType = strings.TrimSpace(contentType[:index])
	}

	extension, ok := allowedTypes[contentType]
	if !ok {
		return "", "", ErrUnsupportedType
	}
	return contentType, extension, nil
}

// LocalStorage writes to a directory on disk and serves it back from the API.
//
// It exists so the feature is usable with no third-party account. It is not a
// production answer: a container filesystem is ephemeral, and two API replicas
// do not share one. Point Storage at a hosted provider before that matters.
type LocalStorage struct {
	dir     string
	baseURL string
}

// NewLocalStorage prepares dir, creating it if needed. baseURL is the path
// prefix the files are served under.
func NewLocalStorage(dir, baseURL string) (*LocalStorage, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("media: create upload dir: %w", err)
	}
	return &LocalStorage{dir: dir, baseURL: strings.TrimSuffix(baseURL, "/")}, nil
}

// Save writes the bytes under a generated name.
//
// The stored name is generated here rather than derived from what the client
// called the file: a caller-supplied name is the classic path-traversal
// vector, and nothing downstream needs the original.
func (s *LocalStorage) Save(_ context.Context, _ string, contentType string, data []byte) (Stored, error) {
	extension, ok := allowedTypes[contentType]
	if !ok {
		return Stored{}, ErrUnsupportedType
	}

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return Stored{}, fmt.Errorf("media: name: %w", err)
	}
	key := hex.EncodeToString(buf) + extension

	if err := os.WriteFile(filepath.Join(s.dir, key), data, 0o640); err != nil {
		return Stored{}, fmt.Errorf("media: write: %w", err)
	}

	return Stored{URL: s.baseURL + "/" + key, PublicID: key}, nil
}

// Delete removes the file. A key that is already gone is not an error: the row
// is going away either way, and failing here would strand it.
func (s *LocalStorage) Delete(_ context.Context, publicID string) error {
	path, err := s.resolve(publicID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("media: delete: %w", err)
	}
	return nil
}

// Open returns the file for serving.
func (s *LocalStorage) Open(publicID string) (io.ReadSeekCloser, string, error) {
	path, err := s.resolve(publicID)
	if err != nil {
		return nil, "", err
	}

	file, err := os.Open(path) //nolint:gosec // path is validated by resolve
	if err != nil {
		return nil, "", err
	}

	// Re-sniff on the way out rather than trusting the extension: the response
	// is served from the API's own origin, so the type it claims matters.
	header := make([]byte, 512)
	n, _ := io.ReadFull(file, header)
	contentType, _, err := Inspect(header[:n])
	if err != nil {
		_ = file.Close()
		return nil, "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, "", err
	}

	return file, contentType, nil
}

// resolve turns a key into a path inside the storage dir, refusing anything
// that tries to point outside it.
func (s *LocalStorage) resolve(publicID string) (string, error) {
	// A key is a bare filename by construction. Anything with a separator or a
	// parent reference did not come from Save.
	if publicID == "" ||
		strings.ContainsAny(publicID, `/\`) ||
		strings.Contains(publicID, "..") {
		return "", fmt.Errorf("media: bad key %q", publicID)
	}

	path := filepath.Join(s.dir, publicID)

	// Belt and braces: even with the checks above, confirm the result really
	// is under the storage directory before opening it.
	if !strings.HasPrefix(path, filepath.Clean(s.dir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("media: key escapes storage dir")
	}
	return path, nil
}

// ReadLimited reads at most MaxImageBytes+1 so an oversized upload is detected
// without buffering all of it.
func ReadLimited(r io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(r, MaxImageBytes+1)); err != nil {
		return nil, err
	}
	if buf.Len() > MaxImageBytes {
		return nil, ErrTooLarge
	}
	return buf.Bytes(), nil
}
