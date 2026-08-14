package media

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Cloudinary's signature scheme specifies SHA-1
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CloudinaryStorage uploads through Cloudinary's server-side API.
//
// NOTE: this implementation is written to Cloudinary's documented contract but
// has never been run against the real service — there are no credentials in
// this repo to run it with. Treat the first upload after configuring it as the
// test, and read the error rather than assuming the wiring is wrong.
//
// It uploads server-side rather than issuing a browser-side signature (which
// is what backend.md §11 describes). That keeps one upload path for the client
// regardless of backend, at the cost of image bytes passing through the API.
// Switching to signed direct upload later is a change to this file plus a new
// endpoint, not to anything above the Storage port.
type CloudinaryStorage struct {
	cloudName string
	apiKey    string
	apiSecret string
	folder    string
	client    *http.Client
}

// NewCloudinaryStorage builds the hosted backend.
func NewCloudinaryStorage(cloudName, apiKey, apiSecret, folder string) *CloudinaryStorage {
	return &CloudinaryStorage{
		cloudName: cloudName,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		folder:    folder,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Configured reports whether enough is set for this backend to work.
func (c *CloudinaryStorage) Configured() bool {
	return c.cloudName != "" && c.apiKey != "" && c.apiSecret != ""
}

// Save uploads the bytes and returns the delivery URL and public id.
func (c *CloudinaryStorage) Save(ctx context.Context, name, contentType string, data []byte) (Stored, error) {
	if _, ok := allowedTypes[contentType]; !ok {
		return Stored{}, ErrUnsupportedType
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	params := map[string]string{"timestamp": timestamp}
	if c.folder != "" {
		params["folder"] = c.folder
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)

	part, err := form.CreateFormFile("file", name)
	if err != nil {
		return Stored{}, fmt.Errorf("media: cloudinary form: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return Stored{}, fmt.Errorf("media: cloudinary form: %w", err)
	}

	for key, value := range params {
		if err := form.WriteField(key, value); err != nil {
			return Stored{}, fmt.Errorf("media: cloudinary form: %w", err)
		}
	}
	// api_key and the signature itself are never part of the signed string.
	if err := form.WriteField("api_key", c.apiKey); err != nil {
		return Stored{}, fmt.Errorf("media: cloudinary form: %w", err)
	}
	if err := form.WriteField("signature", c.sign(params)); err != nil {
		return Stored{}, fmt.Errorf("media: cloudinary form: %w", err)
	}
	if err := form.Close(); err != nil {
		return Stored{}, fmt.Errorf("media: cloudinary form: %w", err)
	}

	endpoint := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/image/upload",
		url.PathEscape(c.cloudName))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return Stored{}, fmt.Errorf("media: cloudinary request: %w", err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())

	resp, err := c.client.Do(req)
	if err != nil {
		return Stored{}, fmt.Errorf("media: cloudinary upload: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Stored{}, fmt.Errorf("media: cloudinary read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Stored{}, fmt.Errorf("media: cloudinary upload: %s: %s",
			resp.Status, strings.TrimSpace(string(payload)))
	}

	var decoded struct {
		SecureURL string `json:"secure_url"`
		PublicID  string `json:"public_id"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return Stored{}, fmt.Errorf("media: cloudinary decode: %w", err)
	}
	if decoded.SecureURL == "" || decoded.PublicID == "" {
		return Stored{}, fmt.Errorf("media: cloudinary returned no url")
	}

	return Stored{URL: decoded.SecureURL, PublicID: decoded.PublicID}, nil
}

// Delete removes an asset by public id.
func (c *CloudinaryStorage) Delete(ctx context.Context, publicID string) error {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	params := map[string]string{"public_id": publicID, "timestamp": timestamp}

	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}
	form.Set("api_key", c.apiKey)
	form.Set("signature", c.sign(params))

	endpoint := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/image/destroy",
		url.PathEscape(c.cloudName))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("media: cloudinary request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("media: cloudinary destroy: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("media: cloudinary destroy: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("media: cloudinary destroy: %s: %s",
			resp.Status, strings.TrimSpace(string(payload)))
	}

	// The status code is not the answer here. Destroying a public id that does
	// not exist still returns 200, with the outcome in the body — so checking
	// only the status would report success for a delete that removed nothing,
	// and assets would pile up in the account unnoticed.
	var decoded struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return fmt.Errorf("media: cloudinary destroy decode: %w", err)
	}

	switch decoded.Result {
	case "ok":
		return nil
	case "not found":
		// Already gone. The row is being removed either way, so this is the
		// outcome we wanted, not an error — same reasoning as a missing file
		// on local disk.
		return nil
	default:
		return fmt.Errorf("media: cloudinary destroy: %s", decoded.Result)
	}
}

// sign builds Cloudinary's upload signature: every signed parameter sorted by
// key, joined as k=v pairs, with the API secret appended, hashed with SHA-1.
// The algorithm is theirs — SHA-1 here is interoperability, not a choice.
func (c *CloudinaryStorage) sign(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+params[key])
	}

	sum := sha1.Sum([]byte(strings.Join(parts, "&") + c.apiSecret)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}
