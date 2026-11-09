package mcpserver

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	pngenc "image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/cms/internal/wordpress"
)

// TestIntegrationRealWordPress drives the tools against a REAL WordPress
// (Application Passwords, /wp-json/wp/v2). It is skipped unless
// CMS_WP_INTEGRATION_URL, CMS_WP_INTEGRATION_USER and CMS_WP_INTEGRATION_APP_PASSWORD
// are set; docker/compose.yaml and docker/wp-setup.sh give a disposable one.
func TestIntegrationRealWordPress(t *testing.T) {
	url, user, pass := os.Getenv("CMS_WP_INTEGRATION_URL"), os.Getenv("CMS_WP_INTEGRATION_USER"), os.Getenv("CMS_WP_INTEGRATION_APP_PASSWORD")
	if url == "" || user == "" || pass == "" {
		t.Skip("set CMS_WP_INTEGRATION_URL, CMS_WP_INTEGRATION_USER and CMS_WP_INTEGRATION_APP_PASSWORD to run against a real WordPress")
	}
	cfg, issue, _ := wordpress.ParseConfig(url, user, pass)
	if issue != "" {
		t.Fatalf("integration configuration refused: %s", issue)
	}
	h := newHandlers(Deps{WordPress: wordpress.New(cfg), MaxMediaBytes: defaultMediaBytes, Secrets: []string{pass}})
	run := fmt.Sprint(time.Now().UnixNano())
	slug := "cms-it-" + run

	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		img.Set(x, x, color.RGBA{R: 200, A: 255})
	}
	var pngBuf bytes.Buffer
	if err := pngenc.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}

	m, _ := call(t, h, h.mediaUpload, map[string]any{"content_base64": base64.StdEncoding.EncodeToString(pngBuf.Bytes()), "filename": "cms-it-" + run + ".png", "mime_type": "image/png", "alt_text": "integration", "idempotency_key": "it-media-" + run})
	wantOK(t, m)
	mediaID, ok := m["media_id"].(int)
	if !ok || mediaID < 1 || !strings.HasPrefix(fmt.Sprint(m["url"]), "http") {
		t.Fatalf("media result: %v", m)
	}
	if resp, err := http.Get(fmt.Sprint(m["url"])); err != nil || resp.StatusCode != 200 {
		t.Fatalf("uploaded media not served: %v %v", err, resp)
	} else {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	m, _ = call(t, h, h.postCreate, map[string]any{"title": "Integration " + run, "content": "<p>Hello <strong>WordPress</strong></p>", "excerpt": "ex", "slug": slug, "status": "draft", "featured_media": mediaID, "idempotency_key": "it-create-" + run})
	wantOK(t, m)
	id := m["post_id"]
	if m["state"] != "draft" || m["type"] != "post" {
		t.Fatalf("draft: %v", m)
	}
	m, _ = call(t, h, h.postCreate, map[string]any{"title": "Integration " + run, "content": "<p>Hello <strong>WordPress</strong></p>", "excerpt": "ex", "slug": slug, "status": "draft", "featured_media": mediaID, "idempotency_key": "it-create-" + run})
	if m["idempotent_replay"] != true || m["post_id"] != id {
		t.Fatalf("replay: %v", m)
	}

	m, _ = call(t, h, h.postUpdate, map[string]any{"post_id": id, "status": "publish", "title": "Integration " + run + " (published)", "idempotency_key": "it-pub-" + run})
	wantOK(t, m)
	if m["state"] != "publish" {
		t.Fatalf("publish: %v", m)
	}
	link := fmt.Sprint(m["link"])
	if resp, err := http.Get(link); err != nil || resp.StatusCode != 200 {
		t.Fatalf("published post not served at %s: %v %v", link, err, resp)
	} else {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	m, _ = call(t, h, h.postGet, map[string]any{"post_id": id})
	wantOK(t, m)
	if m["state"] != "publish" || m["modified"] == "" || m["date"] == "" {
		t.Fatalf("get: %v", m)
	}

	m, _ = call(t, h, h.postUnpublish, map[string]any{"post_id": id, "mode": "draft", "idempotency_key": "it-draft-" + run})
	wantOK(t, m)
	if m["state"] != "draft" {
		t.Fatalf("back to draft: %v", m)
	}

	when := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	m, _ = call(t, h, h.postUpdate, map[string]any{"post_id": id, "status": "future", "date": when, "idempotency_key": "it-future-" + run})
	wantOK(t, m)
	if m["state"] != "future" {
		t.Fatalf("future: %v", m)
	}

	m, _ = call(t, h, h.postUnpublish, map[string]any{"post_id": id, "mode": "trash", "idempotency_key": "it-trash-" + run})
	wantOK(t, m)
	if m["state"] != "trash" {
		t.Fatalf("trash: %v", m)
	}
	// Trashed, not destroyed: the post is still readable.
	m, _ = call(t, h, h.postGet, map[string]any{"post_id": id, "type": "post"})
	wantOK(t, m)
	if m["state"] != "trash" {
		t.Fatalf("still in trash: %v", m)
	}

	m, _ = call(t, h, h.postCreate, map[string]any{"type": "page", "title": "Page " + run, "content": "<p>page</p>", "idempotency_key": "it-page-" + run})
	wantOK(t, m)
	if m["type"] != "page" || m["state"] != "draft" {
		t.Fatalf("page: %v", m)
	}
}
