package storage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// Exercises the real Supabase Storage bucket. Skipped unless credentials are
// present, so it never runs in a plain `make test`.
//
//	SUPABASE_URL=... SUPABASE_SERVICE_ROLE_KEY=... SUPABASE_PHOTO_BUCKET=... \
//	  go test ./internal/storage/ -run RealStorage -v
//
// It uploads one object under entries/_selftest/, reads it back through a signed
// URL, confirms the bucket is not publicly readable, and deletes it again.
func TestRealStorageRoundTrip(t *testing.T) {
	baseURL := os.Getenv("SUPABASE_URL")
	serviceKey := os.Getenv("SUPABASE_SERVICE_ROLE_KEY")
	bucket := os.Getenv("SUPABASE_PHOTO_BUCKET")
	if baseURL == "" || serviceKey == "" {
		t.Skip("SUPABASE_URL and SUPABASE_SERVICE_ROLE_KEY not set")
	}
	if bucket == "" {
		bucket = "family-questions-photos"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c := New(baseURL, serviceKey)
	if !c.Configured() {
		t.Fatal("client reports itself unconfigured")
	}

	payload := tinyPNGBytes()
	object := ObjectPath(0, "gotest-"+time.Now().UTC().Format("20060102T150405"), ".png")
	object = "entries/_selftest/" + object[len("entries/0/"):]

	if err := c.Upload(ctx, bucket, object, "image/png", bytes.NewReader(payload)); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	// Always clean up, even if a later assertion fails.
	t.Cleanup(func() {
		if err := c.Delete(context.Background(), bucket, object); err != nil {
			t.Errorf("cleanup failed, %s may be left behind: %v", object, err)
		}
	})

	signed, err := c.SignedURL(ctx, bucket, object, 2*time.Minute)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	t.Logf("signed url host resolved: %s", signed[:min(len(signed), 60)])

	resp, err := http.Get(signed)
	if err != nil {
		t.Fatalf("fetch signed url: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed fetch = %s", resp.Status)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, payload) {
		t.Errorf("round trip changed the bytes: sent %d, got %d", len(payload), len(got))
	}

	// The bucket must not be readable without a signature: family photographs
	// should not be reachable by anyone who guesses a path.
	pub, err := http.Get(baseURL + "/storage/v1/object/public/" + bucket + "/" + object)
	if err != nil {
		t.Fatalf("public probe: %v", err)
	}
	defer pub.Body.Close()
	if pub.StatusCode == http.StatusOK {
		t.Errorf("bucket %s is publicly readable — it should be private", bucket)
	} else {
		t.Logf("unsigned read correctly refused with %s", pub.Status)
	}

	// Uploading the same path again must not silently replace the original.
	err = c.Upload(ctx, bucket, object, "image/png", bytes.NewReader(payload))
	if err == nil {
		t.Error("re-uploading the same path should be refused, not overwrite")
	}
}

func tinyPNGBytes() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
		0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0,
		0x1f, 0x15, 0xc4, 0x89,
		0, 0, 0, 0x0a, 'I', 'D', 'A', 'T',
		0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
		0x0d, 0x0a, 0x2d, 0xb4,
		0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
