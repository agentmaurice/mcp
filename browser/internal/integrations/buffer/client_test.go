package buffer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUploadSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/buffer" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"namespace":"browser"`) {
			t.Fatalf("request body missing namespace: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"reference":{"type":"buffer","key":"browser:abc","size":123,"mime_type":"image/png","expires_in":600}}`))
	}))
	defer srv.Close()

	client := NewClient(Options{
		BaseURL:        srv.URL,
		AuthToken:      "test-token",
		MaxUploadBytes: 1024 * 1024,
		NetworkRetries: 0,
		Timeout:        2 * time.Second,
	}, nil)

	ref, err := client.Upload(context.Background(), UploadRequest{
		Namespace:  "browser",
		ToolName:   "browser_screenshot",
		MimeType:   "image/png",
		Text:       "base64payload",
		Summary:    "screen",
		TTLSeconds: 600,
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if ref == nil || ref.Key != "browser:abc" {
		t.Fatalf("unexpected reference: %+v", ref)
	}
}

func TestUploadAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	client := NewClient(Options{
		BaseURL:        srv.URL,
		AuthToken:      "bad-token",
		MaxUploadBytes: 1024 * 1024,
		Timeout:        2 * time.Second,
	}, nil)

	_, err := client.Upload(context.Background(), UploadRequest{
		Namespace: "browser",
		Text:      "payload",
	})
	if err == nil {
		t.Fatal("expected auth error, got nil")
	}

	var uploadErr *UploadError
	if !errors.As(err, &uploadErr) {
		t.Fatalf("expected UploadError, got: %T", err)
	}
	if uploadErr.Reason != FailureAuth {
		t.Fatalf("expected auth failure, got: %s", uploadErr.Reason)
	}
}

func TestUploadTooLargeBeforeRequest(t *testing.T) {
	client := NewClient(Options{
		BaseURL:        "http://example.com",
		AuthToken:      "token",
		MaxUploadBytes: 10,
		Timeout:        2 * time.Second,
	}, nil)

	_, err := client.Upload(context.Background(), UploadRequest{
		Namespace: "browser",
		Text:      strings.Repeat("a", 100),
	})
	if err == nil {
		t.Fatal("expected too_large error, got nil")
	}

	var uploadErr *UploadError
	if !errors.As(err, &uploadErr) {
		t.Fatalf("expected UploadError, got: %T", err)
	}
	if uploadErr.Reason != FailureTooLarge {
		t.Fatalf("expected too_large failure, got: %s", uploadErr.Reason)
	}
}

type flakyTransport struct {
	calls int
}

func (t *flakyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls++
	if t.calls == 1 {
		return nil, errors.New("connection reset by peer")
	}

	return &http.Response{
		StatusCode: http.StatusCreated,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"reference":{"type":"buffer","key":"browser:retry-ok"}}`)),
		Request:    req,
	}, nil
}

func TestUploadRetriesNetworkErrors(t *testing.T) {
	client := NewClient(Options{
		BaseURL:        "http://buffer.local",
		AuthToken:      "token",
		MaxUploadBytes: 1024 * 1024,
		NetworkRetries: 1,
		Timeout:        2 * time.Second,
	}, nil)

	transport := &flakyTransport{}
	client.httpClient = &http.Client{
		Timeout:   2 * time.Second,
		Transport: transport,
	}

	ref, err := client.Upload(context.Background(), UploadRequest{
		Namespace: "browser",
		Text:      "payload",
	})
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if ref == nil || ref.Key != "browser:retry-ok" {
		t.Fatalf("unexpected reference: %+v", ref)
	}
	if transport.calls != 2 {
		t.Fatalf("expected 2 calls, got %d", transport.calls)
	}
}
