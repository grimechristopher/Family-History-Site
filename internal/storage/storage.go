// Package storage talks to Supabase Storage.
//
// Uploads go through the Go server using the service_role key, which never
// reaches a browser. Reads are served as short-lived signed URLs, so the buckets
// stay private: family photographs should not be guessable by URL.
package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// MaxPhotoBytes caps an upload. iPad photographs are a few megabytes; this
// leaves plenty of room while refusing something pathological.
const MaxPhotoBytes = 25 << 20 // 25 MiB

// allowedPhotoTypes is an allowlist rather than a blocklist. HEIC is included
// because it is what iPhones and iPads produce by default.
var allowedPhotoTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
	"image/heic": ".heic",
	"image/heif": ".heif",
}

type Client struct {
	BaseURL    string // https://supabase.example.com
	ServiceKey string
	HTTP       *http.Client
}

func New(baseURL, serviceKey string) *Client {
	return &Client{
		BaseURL:    strings.TrimSuffix(baseURL, "/"),
		ServiceKey: serviceKey,
		HTTP:       &http.Client{Timeout: 60 * time.Second},
	}
}

// Configured reports whether uploads are possible. The service key is absent
// until it is copied off the server, and the site should degrade rather than
// crash in the meantime.
func (c *Client) Configured() bool {
	return c != nil && c.ServiceKey != "" && c.BaseURL != ""
}

// ExtensionFor validates a content type and returns the file extension to store
// under.
func ExtensionFor(contentType string) (string, error) {
	base, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		base = strings.ToLower(strings.TrimSpace(contentType))
	}
	ext, ok := allowedPhotoTypes[base]
	if !ok {
		return "", fmt.Errorf("%s is not a picture we can accept", base)
	}
	return ext, nil
}

// ObjectPath builds a stable, non-guessable path for an attachment.
func ObjectPath(entryID int64, token, ext string) string {
	return fmt.Sprintf("entries/%d/%s%s", entryID, token, ext)
}

func (c *Client) request(ctx context.Context, method, endpoint string, body io.Reader, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.ServiceKey)
	req.Header.Set("apikey", c.ServiceKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

// Upload stores an object and returns its path within the bucket.
func (c *Client) Upload(ctx context.Context, bucket, objectPath, contentType string, r io.Reader) error {
	if !c.Configured() {
		return fmt.Errorf("storage is not configured: SUPABASE_SERVICE_ROLE_KEY is unset")
	}

	endpoint := fmt.Sprintf("%s/storage/v1/object/%s/%s",
		c.BaseURL, url.PathEscape(bucket), escapeObjectPath(objectPath))

	req, err := c.request(ctx, http.MethodPost, endpoint, r, contentType)
	if err != nil {
		return err
	}
	// Never silently replace an existing object.
	req.Header.Set("x-upsert", "false")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("upload to %s: %w", bucket, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("upload to %s returned %s: %s", bucket, resp.Status, bytes.TrimSpace(detail))
}

// SignedURL returns a temporary read URL. Short-lived by design: a leaked link
// stops working rather than exposing a family photograph indefinitely.
func (c *Client) SignedURL(ctx context.Context, bucket, objectPath string, ttl time.Duration) (string, error) {
	if !c.Configured() {
		return "", fmt.Errorf("storage is not configured: SUPABASE_SERVICE_ROLE_KEY is unset")
	}

	endpoint := fmt.Sprintf("%s/storage/v1/object/sign/%s/%s",
		c.BaseURL, url.PathEscape(bucket), escapeObjectPath(objectPath))

	payload, err := json.Marshal(map[string]any{"expiresIn": int(ttl.Seconds())})
	if err != nil {
		return "", err
	}
	req, err := c.request(ctx, http.MethodPost, endpoint, bytes.NewReader(payload), "application/json")
	if err != nil {
		return "", err
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("sign %s: %w", objectPath, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("sign %s returned %s: %s", objectPath, resp.Status, bytes.TrimSpace(detail))
	}

	var out struct {
		SignedURL string `json:"signedURL"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode signed url: %w", err)
	}
	if out.SignedURL == "" {
		return "", fmt.Errorf("supabase returned an empty signed url for %s", objectPath)
	}
	// Supabase returns a path relative to the storage API.
	if strings.HasPrefix(out.SignedURL, "http://") || strings.HasPrefix(out.SignedURL, "https://") {
		return out.SignedURL, nil
	}
	return c.BaseURL + "/storage/v1" + ensureLeadingSlash(out.SignedURL), nil
}

func (c *Client) Delete(ctx context.Context, bucket, objectPath string) error {
	if !c.Configured() {
		return fmt.Errorf("storage is not configured: SUPABASE_SERVICE_ROLE_KEY is unset")
	}
	endpoint := fmt.Sprintf("%s/storage/v1/object/%s/%s",
		c.BaseURL, url.PathEscape(bucket), escapeObjectPath(objectPath))

	req, err := c.request(ctx, http.MethodDelete, endpoint, nil, "")
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("delete %s: %w", objectPath, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("delete %s returned %s: %s", objectPath, resp.Status, bytes.TrimSpace(detail))
}

// escapeObjectPath escapes each segment while keeping the slashes, so a path
// stays a path and cannot be used to climb out of its prefix.
func escapeObjectPath(p string) string {
	p = path.Clean("/" + p)
	segments := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

func ensureLeadingSlash(s string) string {
	if strings.HasPrefix(s, "/") {
		return s
	}
	return "/" + s
}
