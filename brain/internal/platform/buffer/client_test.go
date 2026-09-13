package buffer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoadTextUsesAuthenticatedOpaqueKey(t *testing.T) {
	key := "storage:file:secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("missing bearer token")
		}
		if !strings.Contains(r.URL.Path, key) {
			t.Fatalf("opaque key was not kept in the configured path: %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(loadResponse{Content: BufferedContent{
			Version: "1", Namespace: "storage:file", MimeType: "application/json", Text: `{"ok":true}`,
		}})
	}))
	defer server.Close()

	client := NewClient(server.URL+"/buffer", "test-token")
	content, mimeType, err := client.LoadText(context.Background(), key, 1024)
	if err != nil {
		t.Fatalf("load text: %v", err)
	}
	if string(content) != `{"ok":true}` || mimeType != "application/json" {
		t.Fatalf("unexpected content: %q (%s)", content, mimeType)
	}
}

func TestLoadTextDoesNotExposeOpaqueKeyInErrors(t *testing.T) {
	key := "storage:file:secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	_, _, err := NewClient(server.URL, "test-token").LoadText(context.Background(), key, 1024)
	if err == nil || !strings.Contains(err.Error(), ErrNotFound.Error()) {
		t.Fatalf("expected not-found error, got %v", err)
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("opaque key leaked in error: %v", err)
	}
}

func TestLoadTextRejectsBinaryPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(loadResponse{Content: BufferedContent{
			Version: "1", Namespace: "storage:file", MimeType: "application/octet-stream", Binary: "AAEC",
		}})
	}))
	defer server.Close()

	_, _, err := NewClient(server.URL, "test-token").LoadText(context.Background(), "storage:file:key", 1024)
	if err != ErrBinaryContent {
		t.Fatalf("expected binary rejection, got %v", err)
	}
}

func TestLoadTextRejectsInvalidAndOversizedPayloads(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"content":`))
		}))
		defer server.Close()

		_, _, err := NewClient(server.URL, "test-token").LoadText(context.Background(), "storage:file:key", 1024)
		if err != ErrInvalidPayload {
			t.Fatalf("expected invalid payload, got %v", err)
		}
	})

	t.Run("too large", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(loadResponse{Content: BufferedContent{Text: strings.Repeat("x", 1025)}})
		}))
		defer server.Close()

		_, _, err := NewClient(server.URL, "test-token").LoadText(context.Background(), "storage:file:key", 1024)
		if err != ErrTooLarge {
			t.Fatalf("expected size rejection, got %v", err)
		}
	})
}
