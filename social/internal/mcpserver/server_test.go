package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/social/internal/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/social/internal/postiz"
	"github.com/mark3labs/mcp-go/mcp"
)

const testKey = "sk-super-secret-key"

var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

// fakePostiz reproduces the documented forms of the public API.
type fakePostiz struct {
	mu          sync.Mutex
	posts       []postiz.Post
	creates     int
	uploads     int
	deletes     []string
	forceCode   int
	logs        bytes.Buffer
	lastBody    map[string]any
	windowCheck bool
	dropCreate  bool // close the connection after receiving POST /posts
	partial     bool // answer only the first channel
	uploaded    atomic.Bool
	rawBody     string // when set, GET /posts and /integrations answer 200 with it
	createMode  string // "truncated", "empty", "noid", "500", "400"
}

func (f *fakePostiz) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		fmt.Fprintf(&f.logs, "%s %s\n", r.Method, r.URL.String())
		if r.Header.Get("Authorization") != testKey {
			w.WriteHeader(401)
			return
		}
		if f.forceCode != 0 {
			if f.forceCode == 302 {
				w.Header().Set("Location", "/public/v1/elsewhere-secret")
			}
			if f.forceCode == 429 {
				w.Header().Set("Retry-After", "120")
			}
			w.WriteHeader(f.forceCode)
			return
		}
		switch {
		case r.Method == "GET" && f.rawBody != "" && (r.URL.Path == "/public/v1/integrations" || r.URL.Path == "/public/v1/posts"):
			w.Write([]byte(f.rawBody))
		case r.Method == "GET" && r.URL.Path == "/public/v1/integrations":
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": "ch1", "name": "Acme", "identifier": "x", "picture": "https://p/a.jpg", "disabled": false, "profile": "acme"},
				{"id": "ch2", "name": "Acme LI", "identifier": "linkedin", "disabled": false},
				{"id": "off", "name": "Off", "identifier": "bluesky", "disabled": true},
			})
		case r.Method == "POST" && r.URL.Path == "/public/v1/upload":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				w.WriteHeader(400)
				return
			}
			if _, _, err := r.FormFile("file"); err != nil {
				w.WriteHeader(400)
				return
			}
			f.uploads++
			f.uploaded.Store(true)
			json.NewEncoder(w).Encode(map[string]any{"id": "m1", "name": "a.png", "path": "https://u/a.png"})
		case r.Method == "POST" && r.URL.Path == "/public/v1/posts":
			f.creates++
			body, _ := io.ReadAll(r.Body)
			if f.dropCreate {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			json.Unmarshal(body, &f.lastBody)
			var req postiz.CreateRequest
			json.Unmarshal(body, &req)
			switch f.createMode {
			case "truncated":
				w.Write([]byte(`[{"postId":"p1","integra`))
				return
			case "empty":
				w.Write([]byte(`[]`))
				return
			case "noid":
				w.Write([]byte(`[{"integration":"ch1"},{"integration":"ch2","postId":""}]`))
				return
			case "500":
				w.WriteHeader(500)
				return
			case "400":
				w.WriteHeader(400)
				return
			}
			var out []map[string]any
			for i, p := range req.Posts {
				if f.partial && i > 0 {
					continue
				}
				id := fmt.Sprintf("post-%d-%d", f.creates, i)
				st := map[string]string{"draft": "DRAFT", "schedule": "QUEUE", "now": "PUBLISHED"}[req.Type]
				f.posts = append(f.posts, postiz.Post{ID: id, State: st, PublishDate: req.Date, ReleaseURL: "https://x.com/" + id, Integration: struct {
					ID string `json:"id"`
				}{p.Integration.ID}})
				out = append(out, map[string]any{"postId": id, "integration": p.Integration.ID})
			}
			json.NewEncoder(w).Encode(out)
		case r.Method == "GET" && r.URL.Path == "/public/v1/posts":
			if r.URL.Query().Get("startDate") == "" || r.URL.Query().Get("endDate") == "" {
				w.WriteHeader(400)
				return
			}
			// like a real range query: only posts whose date is in [start, end]
			st, _ := time.Parse(time.RFC3339, r.URL.Query().Get("startDate"))
			en, _ := time.Parse(time.RFC3339, r.URL.Query().Get("endDate"))
			var in []postiz.Post
			for _, p := range f.posts {
				d, err := time.Parse(time.RFC3339, p.PublishDate)
				if p.PublishDate == "" || err != nil || (!d.Before(st) && !d.After(en)) {
					in = append(in, p)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"posts": in})
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/public/v1/posts/"):
			id := strings.TrimPrefix(r.URL.Path, "/public/v1/posts/")
			for _, p := range f.posts {
				if p.ID == id {
					f.deletes = append(f.deletes, id)
					json.NewEncoder(w).Encode(map[string]any{"id": id})
					return
				}
			}
			w.WriteHeader(404)
		default:
			w.WriteHeader(404)
		}
	})
}

func (f *fakePostiz) set(fn func())    { f.mu.Lock(); fn(); f.mu.Unlock() }
func (f *fakePostiz) createCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.creates }

func setup(t *testing.T, key string, rate int) (*handlers, *fakePostiz, *time.Time) {
	f := &fakePostiz{}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	lim := postiz.NewLimiter(rate)
	lim.Now = func() time.Time { return now }
	deps := Deps{Postiz: postiz.New(srv.URL+"/public/v1", key, lim), RatePerHour: rate, MaxMediaBytes: 1024, Now: func() time.Time { return now }}
	NewWithDeps(deps)
	h := &handlers{deps: deps}
	h.init()
	return h, f, &now
}

type tcall func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)

func call(t *testing.T, fn tcall, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := fn(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := res.StructuredContent.(map[string]any)
	b, _ := json.Marshal(res.StructuredContent)
	if strings.Contains(string(b), testKey) {
		t.Fatalf("API key leaked in result: %s", b)
	}
	return m, res.IsError
}

func base() map[string]any {
	return map[string]any{"channels": []string{"ch1", "ch2"}, "content": "hello", "mode": "now", "idempotency_key": "k1"}
}

func wantErr(t *testing.T, m map[string]any, isErr bool, code string) {
	t.Helper()
	if !isErr || m["error"] != code {
		t.Fatalf("want error %s, got %v (isError=%v)", code, m, isErr)
	}
}

func TestHealthAndCapabilitiesWithoutConfig(t *testing.T) {
	h := &handlers{deps: Deps{RatePerHour: 30, MaxMediaBytes: 1024, Now: time.Now}}
	h.init()
	m, e := call(t, h.guard(h.health), nil)
	if e || m["status"] != "not_configured" || m["configured"] != false {
		t.Fatal(m)
	}
	m, e = call(t, h.guard(h.capabilities), nil)
	if e || m["configured"] != false {
		t.Fatal(m)
	}
	for _, fn := range []tcall{h.guard(h.channels), h.guard(h.postCreate), h.guard(h.postGet), h.guard(h.postDelete)} {
		m, e = call(t, fn, map[string]any{"post_id": "x", "channels": []string{"a"}, "content": "c", "mode": "now", "idempotency_key": "k"})
		wantErr(t, m, e, "not_configured")
	}
}

func TestAuth(t *testing.T) {
	h, _, _ := setup(t, "wrong", 30)
	m, e := call(t, h.guard(h.channels), nil)
	wantErr(t, m, e, "auth_failed")
	if strings.Contains(fmt.Sprint(m), "wrong") {
		t.Fatal("key in error")
	}
}

func TestChannels(t *testing.T) {
	h, _, _ := setup(t, testKey, 30)
	m, e := call(t, h.guard(h.channels), nil)
	if e {
		t.Fatal(m)
	}
	chs := m["channels"].([]map[string]any)
	if len(chs) != 3 || chs[0]["provider"] != "x" || chs[2]["disabled"] != true {
		t.Fatal(chs)
	}
}

func TestRateLimit429(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	f.set(func() { f.forceCode = 429 })
	m, e := call(t, h.guard(h.channels), nil)
	wantErr(t, m, e, "rate_limited")
	if m["retry_after_s"] != 120 {
		t.Fatal(m)
	}
}

func TestClientLimiter(t *testing.T) {
	h, f, now := setup(t, testKey, 2)
	call(t, h.guard(h.channels), nil)
	call(t, h.guard(h.channels), nil)
	m, e := call(t, h.guard(h.channels), nil)
	wantErr(t, m, e, "rate_limited")
	if s := m["retry_after_s"].(int); s != 3600 {
		t.Fatalf("retry %d", s)
	}
	if n := strings.Count(f.logs.String(), "GET"); n != 2 {
		t.Fatalf("third call must not reach Postiz, got %d", n)
	}
	*now = now.Add(time.Hour + time.Second)
	if m, e := call(t, h.guard(h.channels), nil); e {
		t.Fatal(m)
	}
}

func TestScheduleValidation(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	a := base()
	a["mode"] = "schedule"
	m, e := call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "schedule_at_invalid")
	a["schedule_at"] = "2026-11-07T09:00:00Z"
	m, e = call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "schedule_at_invalid")
	a["schedule_at"] = "tomorrow"
	m, e = call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "schedule_at_invalid")
	a["schedule_at"] = "2026-11-08T09:00:00+01:00"
	m, e = call(t, h.guard(h.postCreate), a)
	if e || m["state"] != "scheduled" {
		t.Fatal(m)
	}
	if f.lastBody["type"] != "schedule" || f.lastBody["date"] != "2026-11-08T08:00:00.000Z" {
		t.Fatal(f.lastBody)
	}
}

func TestUnknownAndDisabledChannel(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	a := base()
	a["channels"] = []string{"ch1", "nope"}
	m, e := call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "unknown_channel")
	a["channels"] = []string{"off"}
	m, e = call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "channel_disabled")
	if f.createCount() != 0 {
		t.Fatal("no post must be created")
	}
}

func TestArgumentBounds(t *testing.T) {
	h, _, _ := setup(t, testKey, 30)
	a := base()
	a["content"] = strings.Repeat("a", 5001)
	m, e := call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "invalid_arguments")
	a = base()
	a["channels"] = []string{}
	m, e = call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "invalid_arguments")
	a = base()
	delete(a, "idempotency_key")
	m, e = call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "invalid_arguments")
	a = base()
	a["mode"] = "later"
	m, e = call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "invalid_arguments")
}

func media(data []byte, mime string) map[string]any {
	return map[string]any{"content_base64": base64.StdEncoding.EncodeToString(data), "filename": "a.png", "mime_type": mime}
}

func TestMedia(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	a := base()
	a["media"] = []map[string]any{media(append(png, make([]byte, 2000)...), "image/png")}
	m, e := call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "media_too_large")
	a["media"] = []map[string]any{media([]byte("%PDF-1.4 hello"), "application/pdf")}
	m, e = call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "media_type_refused")
	a["media"] = []map[string]any{media([]byte("%PDF-1.4 hello"), "image/png")}
	m, e = call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "media_type_refused")
	a["media"] = []map[string]any{media(png, "image/png"), media(png, "image/png"), media(png, "image/png"), media(png, "image/png"), media(png, "image/png")}
	m, e = call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "invalid_arguments")
	a["media"] = []map[string]any{{"ref": "buffer://x", "filename": "a.png", "mime_type": "image/png"}}
	m, e = call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "buffer_unavailable")
	if f.uploads != 0 || f.createCount() != 0 {
		t.Fatal("nothing must reach Postiz on refused media")
	}
	a["media"] = []map[string]any{media(png, "image/png")}
	m, e = call(t, h.guard(h.postCreate), a)
	if e || f.uploads != 1 {
		t.Fatal(m)
	}
	body, _ := json.Marshal(f.lastBody)
	if !strings.Contains(string(body), `"path":"https://u/a.png"`) || !strings.Contains(string(body), `"__type":"x"`) || !strings.Contains(string(body), "who_can_reply_post") {
		t.Fatalf("unexpected body %s", body)
	}
}

func TestIdempotency(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	m1, e := call(t, h.guard(h.postCreate), base())
	if e {
		t.Fatal(m1)
	}
	m2, e := call(t, h.guard(h.postCreate), base())
	if e || m2["post_id"] != m1["post_id"] || m2["idempotent_replay"] != true {
		t.Fatal(m2)
	}
	if f.createCount() != 1 {
		t.Fatalf("double post: %d", f.creates)
	}
	a := base()
	a["content"] = "other"
	m, e := call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "idempotency_conflict")
}

func TestIdempotencyBounded(t *testing.T) {
	h, _, now := setup(t, testKey, 100000)
	for i := 0; i < idempotencyMax+5; i++ {
		a := base()
		a["idempotency_key"] = fmt.Sprint("k", i)
		if m, e := call(t, h.guard(h.postCreate), a); e {
			t.Fatal(m)
		}
	}
	if len(h.idem) != idempotencyMax || h.idemList.Len() != idempotencyMax {
		t.Fatalf("memory not bounded: %d", len(h.idem))
	}
	*now = now.Add(idempotencyTTL + time.Minute)
	h.mu.Lock()
	h.expire()
	h.mu.Unlock()
	if len(h.idem) != 0 {
		t.Fatal("ttl not applied")
	}
}

func TestCreateGetDelete(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	a := base()
	a["mode"] = "draft"
	m, e := call(t, h.guard(h.postCreate), a)
	if e || m["state"] != "draft" || m["group"] == "" {
		t.Fatal(m)
	}
	id := m["post_id"].(string)
	g, e := call(t, h.guard(h.postGet), map[string]any{"post_id": id})
	if e || g["state"] != "draft" || len(g["channels"].([]map[string]any)) != 2 {
		t.Fatal(g)
	}
	f.mu.Lock()
	f.posts[0].State = "ERROR"
	msg := "rejected"
	f.posts[0].Error = &msg
	f.mu.Unlock()
	g, _ = call(t, h.guard(h.postGet), map[string]any{"post_id": id})
	if g["state"] != "partial" {
		t.Fatal(g)
	}
	d, e := call(t, h.guard(h.postDelete), map[string]any{"post_id": id})
	if e || d["deleted"] != true || len(f.deletes) != 1 {
		t.Fatal(d)
	}
	d, e = call(t, h.guard(h.postDelete), map[string]any{"post_id": "ghost"})
	wantErr(t, d, e, "post_not_found")
	g, e = call(t, h.guard(h.postGet), map[string]any{"post_id": "ghost"})
	wantErr(t, g, e, "not_found_in_window")
}

func TestKeyNeverInErrorsOrCapabilities(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	f.set(func() { f.forceCode = 500 })
	m, e := call(t, h.guard(h.channels), nil)
	wantErr(t, m, e, "upstream_error")
	m, _ = call(t, h.guard(h.capabilities), nil)
	if strings.Contains(fmt.Sprint(m), testKey) {
		t.Fatal("leak")
	}
}

func TestMediaFromBufferRef(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	bsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/gone") {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"content": map[string]any{"mime_type": "image/png", "text": base64.StdEncoding.EncodeToString(png), "metadata": map[string]any{"encoding": "base64"}}})
	}))
	defer bsrv.Close()
	h.deps.Buffer = &bufferClient{URL: bsrv.URL + "/api/v1/buffer", Token: "tok", HTTP: bsrv.Client()}
	a := base()
	a["media"] = []map[string]any{{"ref": "buffer://social:abc", "filename": "a.png", "mime_type": "image/png"}}
	if m, e := call(t, h.guard(h.postCreate), a); e || f.uploads != 1 {
		t.Fatal(m)
	}
	a["idempotency_key"] = "k2"
	a["media"] = []map[string]any{{"ref": "buffer://gone", "filename": "a.png", "mime_type": "image/png"}}
	m, e := call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "buffer_not_found")
}

type bufferClient = buffer.Client

func TestHealthConfigured(t *testing.T) {
	h, _, _ := setup(t, testKey, 30)
	m, e := call(t, h.guard(h.health), nil)
	if e || m["status"] != "ok" || m["configured"] != true {
		t.Fatal(m)
	}
}

func TestIdempotentReplayAfterScheduleElapsed(t *testing.T) {
	h, f, now := setup(t, testKey, 30)
	a := base()
	a["mode"] = "schedule"
	a["schedule_at"] = "2026-11-07T11:00:00Z"
	m1, e := call(t, h.guard(h.postCreate), a)
	if e {
		t.Fatal(m1)
	}
	*now = now.Add(3 * time.Hour) // the schedule is now in the past
	m2, e := call(t, h.guard(h.postCreate), a)
	if e || m2["post_id"] != m1["post_id"] || m2["idempotent_replay"] != true || f.createCount() != 1 {
		t.Fatalf("replay must return the stored result: %v", m2)
	}
	a["content"] = "different"
	m3, e := call(t, h.guard(h.postCreate), a)
	wantErr(t, m3, e, "idempotency_conflict")
	a2 := base() // new key, past date: still rejected
	a2["idempotency_key"] = "fresh"
	a2["mode"] = "schedule"
	a2["schedule_at"] = "2026-11-07T11:00:00Z"
	m4, e := call(t, h.guard(h.postCreate), a2)
	wantErr(t, m4, e, "schedule_at_invalid")
}

func TestGetFindsFarFutureAndUnknownIDs(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	a := base()
	a["mode"] = "schedule"
	a["schedule_at"] = "2029-03-01T09:00:00Z" // far beyond the default 365-day window
	m, e := call(t, h.guard(h.postCreate), a)
	if e {
		t.Fatal(m)
	}
	f.mu.Lock()
	f.windowCheck = true
	f.mu.Unlock()
	g, e := call(t, h.guard(h.postGet), map[string]any{"post_id": m["post_id"]})
	if e || g["state"] != "scheduled" {
		t.Fatalf("far-future post must be found: %v", g)
	}
	// An id unknown to this process (restart) is searched in the widened window.
	h2 := &handlers{deps: h.deps}
	h2.init()
	g, e = call(t, h2.guard(h2.postGet), map[string]any{"post_id": m["post_id"]})
	if e || g["state"] != "scheduled" {
		t.Fatalf("unknown id inside 5 years must be found: %v", g)
	}
	g, e = call(t, h2.guard(h2.postGet), map[string]any{"post_id": "ghost"})
	wantErr(t, g, e, "not_found_in_window")
	if g["window_start"] == nil || g["window_end"] == nil {
		t.Fatal("bounds missing", g)
	}
}

func TestSecretsMaskedEverywhere(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	h.deps.Secrets = []string{"buffer-token-xyz"}
	a := base()
	a["mode"] = "draft"
	m, _ := call(t, h.guard(h.postCreate), a)
	f.mu.Lock()
	leak := "boom " + testKey + " and buffer-token-xyz"
	f.posts[0].Error = &leak
	f.posts[0].State = "ERROR"
	f.mu.Unlock()
	g, e := call(t, h.guard(h.postGet), map[string]any{"post_id": m["post_id"]})
	if e {
		t.Fatal(g)
	}
	b, _ := json.Marshal(g)
	if strings.Contains(string(b), testKey) || strings.Contains(string(b), "buffer-token-xyz") || !strings.Contains(string(b), "[redacted]") {
		t.Fatalf("secret not masked: %s", b)
	}
	// a secret inside an arbitrary error message is masked by the guard too
	leaky := h.guard(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return failure("upstream_error", "bad header "+testKey), nil
	})
	res, _ := leaky(context.Background(), mcp.CallToolRequest{})
	if b, _ := json.Marshal(res.StructuredContent); strings.Contains(string(b), testKey) {
		t.Fatalf("guard leaked: %s", b)
	}
	if strings.Contains(f.logs.String(), "sk-super") {
		t.Fatal("key in request log")
	}
}

func TestShortKeyRefused(t *testing.T) {
	t.Setenv("SOCIAL_POSTIZ_URL", "http://localhost:1/public/v1")
	t.Setenv("SOCIAL_POSTIZ_API_KEY", "only15chars-key") // 15 characters // gitleaks:allow (test fixture, not a credential)
	d := DepsFromEnv()
	if d.Postiz != nil || d.ConfigIssue != "api_key_too_short" {
		t.Fatal("short key must be refused")
	}
	h := &handlers{deps: d}
	h.init()
	m, e := call(t, h.guard(h.channels), nil)
	wantErr(t, m, e, "not_configured")
	if m["reason"] != "api_key_too_short" || strings.Contains(fmt.Sprint(m), "only15chars") {
		t.Fatal(m)
	}
	m, _ = call(t, h.guard(h.health), nil)
	if m["status"] != "not_configured" || m["reason"] != "api_key_too_short" {
		t.Fatal(m)
	}
	m, _ = call(t, h.guard(h.capabilities), nil)
	if m["configured"] != false || m["config_issue"] != "api_key_too_short" {
		t.Fatal(m)
	}
	t.Setenv("SOCIAL_POSTIZ_API_KEY", "exactly-16-chars") // gitleaks:allow (test fixture, not a credential)
	if d := DepsFromEnv(); d.Postiz == nil {
		t.Fatal("16 characters must be accepted")
	}
}

func TestOutcomeUnknownNoSecondPost(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	f.set(func() { f.dropCreate = true })
	m, e := call(t, h.guard(h.postCreate), base())
	wantErr(t, m, e, "outcome_unknown")
	f.set(func() { f.dropCreate = false })
	m, e = call(t, h.guard(h.postCreate), base())
	wantErr(t, m, e, "outcome_unknown")
	if !strings.Contains(fmt.Sprint(m["message"]), "nouvelle idempotency_key") || f.createCount() != 1 {
		t.Fatalf("replay must not POST again: creates=%d %v", f.createCount(), m)
	}
	a := base()
	a["content"] = "other"
	m, e = call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "idempotency_conflict")
	a = base()
	a["idempotency_key"] = "new-key"
	if m, e = call(t, h.guard(h.postCreate), a); e || f.createCount() != 2 {
		t.Fatal(m)
	}
}

func TestPartialFailure(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	f.set(func() { f.partial = true })
	m, e := call(t, h.guard(h.postCreate), base())
	wantErr(t, m, e, "partial_failure")
	if m["status"] != nil {
		t.Fatal("must not announce ok")
	}
	chs := m["channels"].([]map[string]any)
	if len(chs) != 2 || chs[0]["state"] != "scheduled" || chs[1]["state"] != "unknown" || chs[1]["id"] != "ch2" {
		t.Fatal(chs)
	}
	f.set(func() { f.partial = false })
	m, e = call(t, h.guard(h.postCreate), base())
	wantErr(t, m, e, "partial_failure")
	if m["idempotent_replay"] != true || f.createCount() != 1 {
		t.Fatal("partial result must be memorized", m)
	}
}

func TestDecideCreateTable(t *testing.T) {
	two := []string{"ch1", "ch2"}
	full := []postiz.Created{{PostID: "a", Integration: "ch1"}, {PostID: "b", Integration: "ch2"}}
	cases := []struct {
		name    string
		created []postiz.Created
		err     error
		want    createKind
	}{
		{"transport", nil, fmt.Errorf("%w: network error", postiz.ErrTransport), createUnknown},
		{"200 truncated", nil, postiz.ErrBadBody, createUnknown},
		{"200 empty array", []postiz.Created{}, nil, createUnknown},
		{"200 no postId", []postiz.Created{{Integration: "ch1"}}, nil, createUnknown},
		{"200 partial", full[:1], nil, createPartial},
		{"200 full", full, nil, createOK},
		{"400", nil, &postiz.StatusError{Code: 400}, createRelease},
		{"401", nil, postiz.ErrUnauthorized, createRelease},
		{"429", nil, &postiz.RateLimitedError{RetryAfter: time.Minute}, createRelease},
		{"local limiter", nil, &postiz.RateLimitedError{Local: true}, createRelease},
		{"500", nil, &postiz.StatusError{Code: 500}, createUnknown},
		{"502", nil, &postiz.StatusError{Code: 502}, createUnknown},
		{"unclassified", nil, errors.New("boom"), createUnknown},
	}
	for _, c := range cases {
		if got := decideCreate(c.created, c.err, two).kind; got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestAmbiguousCreateResponsesAreMemorized(t *testing.T) {
	for _, mode := range []string{"truncated", "empty", "noid", "500"} {
		h, f, _ := setup(t, testKey, 30)
		f.set(func() { f.createMode = mode })
		a := base()
		m, e := call(t, h.guard(h.postCreate), a)
		code := "outcome_unknown"
		if mode == "noid" {
			code = "outcome_unknown"
		}
		wantErr(t, m, e, code)
		f.set(func() { f.createMode = "" })
		m, e = call(t, h.guard(h.postCreate), a)
		wantErr(t, m, e, code)
		if f.createCount() != 1 || m["idempotent_replay"] != true {
			t.Fatalf("%s: replay must not POST again (creates=%d)", mode, f.createCount())
		}
	}
}

func TestExplicitRefusalReleasesKey(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	f.set(func() { f.createMode = "400" })
	m, e := call(t, h.guard(h.postCreate), base())
	wantErr(t, m, e, "upstream_error")
	f.set(func() { f.createMode = "" })
	if m, e = call(t, h.guard(h.postCreate), base()); e || f.createCount() != 2 {
		t.Fatalf("a 400 must free the key: %v", m)
	}
}

func TestGetMarksMissingSiblingUnknown(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	m, e := call(t, h.guard(h.postCreate), base())
	if e {
		t.Fatal(m)
	}
	f.set(func() { f.posts[0].State = "PUBLISHED"; f.posts = f.posts[:1] }) // sibling vanished from the list
	g, e := call(t, h.guard(h.postGet), map[string]any{"post_id": m["post_id"]})
	if e || g["state"] != "partial" {
		t.Fatalf("global state must not be published: %v", g)
	}
	chs := g["channels"].([]map[string]any)
	if len(chs) != 2 || chs[1]["state"] != "unknown" || chs[1]["id"] != "ch2" {
		t.Fatal(chs)
	}
}

func TestPartialCreationNeverReadAsPublished(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	f.set(func() { f.partial = true }) // Postiz returns only p for ch1
	m, e := call(t, h.guard(h.postCreate), base())
	wantErr(t, m, e, "partial_failure")
	f.set(func() { f.posts[0].State = "PUBLISHED" })
	g, e := call(t, h.guard(h.postGet), map[string]any{"post_id": m["post_id"]})
	if e || g["state"] != "partial" {
		t.Fatalf("must be partial, got %v", g)
	}
	chs := g["channels"].([]map[string]any)
	if len(chs) != 2 || chs[0]["state"] != "published" || chs[1]["id"] != "ch2" || chs[1]["state"] != "unknown" {
		t.Fatal(chs)
	}
}

func TestRedirectsNotFollowedAndCounted(t *testing.T) {
	h, f, _ := setup(t, testKey, 2)
	f.set(func() { f.forceCode = 302 })
	m, e := call(t, h.guard(h.channels), nil)
	wantErr(t, m, e, "upstream_redirect")
	if strings.Contains(fmt.Sprint(m), "elsewhere-secret") || !strings.Contains(fmt.Sprint(m), "302") {
		t.Fatalf("code only, no destination: %v", m)
	}
	f.mu.Lock()
	n := strings.Count(f.logs.String(), "GET")
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("redirect followed: %d requests", n)
	}
	call(t, h.guard(h.channels), nil)
	m, e = call(t, h.guard(h.channels), nil)
	wantErr(t, m, e, "rate_limited") // both redirected requests were counted
	if decideCreate(nil, &postiz.RedirectError{Code: 307}, []string{"a"}).kind != createUnknown {
		t.Fatal("a redirect on POST /posts is an unknown outcome")
	}
}

func TestScheduleLeadAndRecheck(t *testing.T) {
	h, f, now := setup(t, testKey, 30)
	a := base()
	a["mode"] = "schedule"
	a["schedule_at"] = now.Add(60 * time.Second).Format(time.RFC3339)
	m, e := call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "schedule_at_invalid")
	// valid at validation (130 s ahead) but passed after the upload: no POST
	t0 := *now
	h.deps.Now = func() time.Time {
		if f.uploaded.Load() {
			return t0.Add(time.Hour)
		}
		return t0
	}
	a["schedule_at"] = t0.Add(130 * time.Second).Format(time.RFC3339)
	a["media"] = []map[string]any{media(png, "image/png")}
	m, e = call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "schedule_at_invalid")
	if f.createCount() != 0 {
		t.Fatal("no POST after the date has passed")
	}
}

func TestNotConfiguredBeforeArguments(t *testing.T) {
	h := &handlers{deps: Deps{RatePerHour: 30, MaxMediaBytes: 1024, Now: time.Now}}
	h.init()
	for name, fn := range map[string]tcall{"create": h.guard(h.postCreate), "get": h.guard(h.postGet), "delete": h.guard(h.postDelete), "channels": h.guard(h.channels)} {
		m, e := call(t, fn, map[string]any{})
		if !e || m["error"] != "not_configured" {
			t.Fatalf("%s: %v", name, m)
		}
	}
}

func TestDecideCreateDistinctIDs(t *testing.T) {
	two := []string{"ch1", "ch2"}
	cases := []struct {
		name    string
		created []postiz.Created
		want    createKind
	}{
		{"same postId on two channels", []postiz.Created{{PostID: "x", Integration: "ch1"}, {PostID: "x", Integration: "ch2"}}, createPartial},
		{"missing integration", []postiz.Created{{PostID: "a"}, {PostID: "b"}}, createUnknown},
		{"unknown integration", []postiz.Created{{PostID: "a", Integration: "zz"}, {PostID: "b", Integration: "ch2"}}, createPartial},
		{"channel answered twice", []postiz.Created{{PostID: "a", Integration: "ch1"}, {PostID: "b", Integration: "ch1"}}, createPartial},
		{"distinct and complete", []postiz.Created{{PostID: "a", Integration: "ch1"}, {PostID: "b", Integration: "ch2"}}, createOK},
	}
	for _, c := range cases {
		if got := decideCreate(c.created, nil, two).kind; got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestAggregateStateTable(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"published"}, "published"},
		{[]string{"draft", "draft"}, "draft"},
		{[]string{"error"}, "error"},
		{[]string{"published", "draft"}, "mixed"},
		{[]string{"published", "scheduled"}, "mixed"},
		{[]string{"published", "error"}, "partial"},
		{[]string{"scheduled", "missing"}, "partial"},
		{[]string{"published", "unknown"}, "partial"},
	}
	for _, c := range cases {
		set := map[string]bool{}
		for _, s := range c.in {
			set[s] = true
		}
		if got := aggregateState(set); got != c.want {
			t.Errorf("%v: got %s want %s", c.in, got, c.want)
		}
	}
}

func TestScheduleMarginRecheckedBeforePost(t *testing.T) {
	h, f, now := setup(t, testKey, 30)
	t0 := *now
	// valid at validation (200 s ahead); at send time only 30 s remain: inside the margin
	h.deps.Now = func() time.Time {
		if f.uploaded.Load() {
			return t0.Add(170 * time.Second)
		}
		return t0
	}
	a := base()
	a["mode"] = "schedule"
	a["schedule_at"] = t0.Add(200 * time.Second).Format(time.RFC3339)
	a["media"] = []map[string]any{media(png, "image/png")}
	m, e := call(t, h.guard(h.postCreate), a)
	wantErr(t, m, e, "schedule_at_invalid")
	if f.createCount() != 0 {
		t.Fatal("no POST inside the margin")
	}
}

func TestRateIsFixedAndNotConfigurable(t *testing.T) {
	t.Setenv("SOCIAL_RATE_LIMIT_PER_HOUR", "5000")
	t.Setenv("SOCIAL_POSTIZ_URL", "http://localhost:1/public/v1")
	t.Setenv("SOCIAL_POSTIZ_API_KEY", "exactly-16-chars") // gitleaks:allow (test fixture, not a credential)
	d := DepsFromEnv()
	if d.RatePerHour != 30 || d.Postiz.Limiter.Max != 30 {
		t.Fatalf("rate must stay 30, got %d / %d", d.RatePerHour, d.Postiz.Limiter.Max)
	}
}

func TestUnconfirmedChannelIsUnknownThenInferred(t *testing.T) {
	h, f, _ := setup(t, testKey, 30)
	f.set(func() { f.partial = true })
	m, e := call(t, h.guard(h.postCreate), base())
	wantErr(t, m, e, "partial_failure")
	if m["channels"].([]map[string]any)[1]["state"] != "unknown" {
		t.Fatal("unconfirmed channel must not be reported missing", m)
	}
	// Postiz did create ch2 after all: p2 appears on ch2 with the group's planned date.
	f.set(func() {
		f.posts[0].State = "PUBLISHED"
		p2 := postiz.Post{ID: "p2", State: "PUBLISHED", PublishDate: f.posts[0].PublishDate, ReleaseURL: "https://x.com/p2"}
		p2.Integration.ID = "ch2"
		f.posts = append(f.posts, p2)
	})
	g, e := call(t, h.guard(h.postGet), map[string]any{"post_id": m["post_id"]})
	if e || g["state"] != "partial" {
		t.Fatalf("an inference never proves: %v", g)
	}
	chs := g["channels"].([]map[string]any)
	if len(chs) != 2 || chs[1]["id"] != "ch2" || chs[1]["post_id"] != "p2" || chs[1]["inferred"] != true || chs[1]["state"] != "published" {
		t.Fatal(chs)
	}
	// another post on ch2 with a different date is not inferred
	f.set(func() { f.posts[1].PublishDate = "2030-01-01T00:00:00.000Z" })
	g, _ = call(t, h.guard(h.postGet), map[string]any{"post_id": m["post_id"]})
	if c := g["channels"].([]map[string]any)[1]; c["state"] != "unknown" || c["inferred"] != nil || g["state"] != "partial" {
		t.Fatal(g)
	}
}

func TestMediaSizeNotConfigurable(t *testing.T) {
	t.Setenv("SOCIAL_MAX_MEDIA_BYTES", "10")
	t.Setenv("SOCIAL_POSTIZ_URL", "http://localhost:1/public/v1")
	t.Setenv("SOCIAL_POSTIZ_API_KEY", "exactly-16-chars") // gitleaks:allow (test fixture, not a credential)
	if d := DepsFromEnv(); d.MaxMediaBytes != 20*1024*1024 {
		t.Fatalf("media size must stay fixed, got %d", d.MaxMediaBytes)
	}
}

func TestInvalidListBodiesAreNeverNotFound(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"other":[]}`, `{"posts":null}`, `{"posts":`, `[]`, `"x"`} {
		h, f, _ := setup(t, testKey, 30)
		m, e := call(t, h.guard(h.postCreate), base()) // known post, then the list goes bad
		if e {
			t.Fatal(m)
		}
		f.set(func() { f.rawBody = body })
		for _, id := range []string{m["post_id"].(string), "unknown-id"} {
			g, e := call(t, h.guard(h.postGet), map[string]any{"post_id": id})
			wantErr(t, g, e, "upstream_invalid_response")
		}
	}
	for _, body := range []string{`{}`, `null`, `{"integrations":[]}`, `[`} {
		h, f, _ := setup(t, testKey, 30)
		f.set(func() { f.rawBody = body })
		m, e := call(t, h.guard(h.channels), nil)
		wantErr(t, m, e, "upstream_invalid_response")
	}
	h, f, _ := setup(t, testKey, 30) // an empty array of channels is a valid answer
	f.set(func() { f.rawBody = `[]` })
	if m, e := call(t, h.guard(h.channels), nil); e || len(m["channels"].([]map[string]any)) != 0 {
		t.Fatal(m)
	}
}
