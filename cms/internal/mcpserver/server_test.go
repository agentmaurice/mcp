package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/cms/internal/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/cms/internal/wordpress"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	testUser = "editor-bot"
	testPass = "abcd EFGH 1234 ijkl MNOP 5678"
)

var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

type fakePost struct {
	ID       int    `json:"id"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Link     string `json:"link"`
	Slug     string `json:"slug"`
	Date     string `json:"date"`
	DateGMT  string `json:"date_gmt"`
	Modified string `json:"modified"`
}

type req struct {
	Method, Path, Query, Auth, CT, CD string
	Body                              []byte
}

// fakeWP reproduces the documented forms of /wp-json/wp/v2.
type fakeWP struct {
	mu          sync.Mutex
	posts       map[int]*fakePost
	media       map[int]map[string]any
	next        int
	reqs        []req
	writes      int
	mode        string // "drop", "500", "html", "redirect", "403", "429", "rate", "leakmsg", "noroute", "trash501", "big"
	srvURL      string
	wrongStatus string
	otherRead   bool
	targetID    int
	altMode     string // failure of the alt_text update only: "drop", "500", "bad", "403"
}

func newFake() *fakeWP {
	return &fakeWP{posts: map[int]*fakePost{}, media: map[int]map[string]any{}, next: 100}
}

func (f *fakeWP) wpErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg, "data": map[string]any{"status": status}})
}

func (f *fakeWP) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		q := req{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), CT: r.Header.Get("Content-Type"), CD: r.Header.Get("Content-Disposition"), Body: body}
		f.reqs = append(f.reqs, q)
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte(testUser+":"+testPass))
		if q.Auth != want {
			f.wpErr(w, 401, "incorrect_password", "The provided password is an invalid application password.")
			return
		}
		isWrite := r.Method != "GET"
		if isWrite {
			f.writes++
		}
		path := strings.TrimPrefix(r.URL.Path, "/wp-json/wp/v2")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		kind := parts[0]
		switch f.mode {
		case "drop":
			if isWrite {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
		case "500":
			if isWrite {
				w.WriteHeader(500)
				return
			}
		case "html":
			w.Write([]byte("<html>maintenance</html>"))
			return
		case "idonly": // 2xx on a write that carries only an id
			if isWrite {
				w.Write([]byte(`{"id":123}`))
				return
			}
		case "otheritem": // a complete answer, but about another item (id 999, a page)
			if isWrite || f.otherRead {
				w.Write([]byte(`{"id":999,"type":"page","status":"trash","link":"https://example.test/p","date":"2026-11-09T10:00:00","modified":"2026-11-09T10:00:00"}`))
				return
			}
		case "othertype": // same id, wrong type
			if isWrite {
				w.Write([]byte(fmt.Sprintf(`{"id":%d,"type":"page","status":"draft","link":"x","date":"d","modified":"m"}`, f.targetID)))
				return
			}
		case "noattach":
			if isWrite {
				w.Write([]byte(`{"id":5,"type":"post","source_url":"u","link":"l","mime_type":"image/png"}`))
				return
			}
		case "wrongstatus": // complete, coherent answer, but not the requested state
			if isWrite && (kind == "posts" || kind == "pages") {
				id := f.next + 1
				if len(parts) == 2 {
					id, _ = strconv.Atoi(parts[1])
				}
				w.Write([]byte(fmt.Sprintf(`{"id":%d,"type":"%s","status":"%s","link":"https://example.test/p","date":"2026-11-09T10:00:00","modified":"2026-11-09T10:00:00"}`, id, map[string]string{"posts": "post", "pages": "page"}[kind], f.wrongStatus)))
				return
			}
		case "idonlyall":
			w.Write([]byte(`{"id":5}`))
			return
		case "badtypes": // 2xx on a write with an invalid type
			if isWrite {
				w.Write([]byte(`{"id":"123","type":"post","status":"publish","link":"x","date":"d","modified":"m"}`))
				return
			}
		case "redirect":
			w.Header().Set("Location", "https://elsewhere.example/secret")
			w.WriteHeader(301)
			return
		case "403":
			f.wpErr(w, 403, "rest_cannot_create", "Sorry, you are not allowed to create posts as this user.")
			return
		case "429":
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			return
		case "leakmsg":
			f.wpErr(w, 400, "rest_invalid_param", "bad value for "+testPass+" and "+testUser+" at "+f.srvURL)
			return
		case "noroute":
			f.wpErr(w, 404, "rest_no_route", "No route was found matching the URL and request method.")
			return
		case "trash501":
			if r.Method == "DELETE" {
				f.wpErr(w, 501, "rest_trash_not_supported", "The post does not support trashing. Set 'force=true' to delete.")
				return
			}
		case "big":
			w.Write([]byte(`{"id":1,"pad":"` + strings.Repeat("x", 5<<20) + `"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case kind == "media" && len(parts) == 1 && r.Method == "POST":
			f.next++
			id := f.next
			m := map[string]any{"id": id, "type": "attachment", "source_url": f.srvURL + "/wp-content/uploads/" + id2(id) + ".png", "link": f.srvURL + "/m" + id2(id), "mime_type": q.CT, "alt_text": ""}
			f.media[id] = m
			json.NewEncoder(w).Encode(m)
		case kind == "media" && len(parts) == 2 && r.Method == "POST":
			switch f.altMode {
			case "drop":
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			case "500":
				w.WriteHeader(500)
				return
			case "bad":
				w.Write([]byte(`{"id":"nope"}`))
				return
			case "noalt":
				id, _ := strconv.Atoi(parts[1])
				w.Write([]byte(fmt.Sprintf(`{"id":%d}`, id)))
				return
			case "emptyalt":
				id, _ := strconv.Atoi(parts[1])
				w.Write([]byte(fmt.Sprintf(`{"id":%d,"alt_text":""}`, id)))
				return
			case "otheralt":
				id, _ := strconv.Atoi(parts[1])
				w.Write([]byte(fmt.Sprintf(`{"id":%d,"alt_text":"something else"}`, id)))
				return
			case "otherid":
				w.Write([]byte(`{"id":999,"alt_text":"Alt"}`))
				return
			case "403":
				f.wpErr(w, 403, "rest_cannot_edit", "Sorry, you are not allowed to edit this post.")
				return
			}
			id, _ := strconv.Atoi(parts[1])
			m := f.media[id]
			if m == nil {
				f.wpErr(w, 404, "rest_post_invalid_id", "Invalid post ID.")
				return
			}
			var in map[string]any
			json.Unmarshal(body, &in)
			m["alt_text"] = in["alt_text"]
			json.NewEncoder(w).Encode(m)
		case (kind == "posts" || kind == "pages") && len(parts) == 1 && r.Method == "POST":
			var in map[string]any
			json.Unmarshal(body, &in)
			f.next++
			p := &fakePost{ID: f.next, Type: map[string]string{"posts": "post", "pages": "page"}[kind], Status: "draft", Date: "2026-11-08T10:00:00", Modified: "2026-11-08T10:00:00"}
			apply(p, in)
			p.Link = f.srvURL + "/?p=" + strconv.Itoa(p.ID)
			f.posts[p.ID] = p
			json.NewEncoder(w).Encode(p)
		case (kind == "posts" || kind == "pages") && len(parts) == 2:
			id, _ := strconv.Atoi(parts[1])
			p := f.posts[id]
			if p == nil || p.Type != map[string]string{"posts": "post", "pages": "page"}[kind] {
				f.wpErr(w, 404, "rest_post_invalid_id", "Invalid post ID.")
				return
			}
			switch r.Method {
			case "GET":
			case "POST":
				var in map[string]any
				json.Unmarshal(body, &in)
				apply(p, in)
			case "DELETE":
				if p.Status == "trash" {
					f.wpErr(w, 410, "rest_already_trashed", "The post has already been deleted.")
					return
				}
				if r.URL.Query().Get("force") == "true" {
					delete(f.posts, id)
					json.NewEncoder(w).Encode(map[string]any{"deleted": true, "previous": p})
					return
				}
				p.Status = "trash"
			}
			json.NewEncoder(w).Encode(p)
		default:
			f.wpErr(w, 404, "rest_no_route", "No route was found matching the URL and request method.")
		}
	})
}

func id2(i int) string { return strconv.Itoa(i) }

func apply(p *fakePost, in map[string]any) {
	if s, ok := in["status"].(string); ok {
		p.Status = s
	}
	if s, ok := in["slug"].(string); ok {
		p.Slug = s
	}
	if s, ok := in["date_gmt"].(string); ok {
		p.DateGMT = s
	}
	p.Modified = "2026-11-08T11:00:00"
}

func (f *fakeWP) set(fn func())   { f.mu.Lock(); fn(); f.mu.Unlock() }
func (f *fakeWP) writeCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.writes }
func (f *fakeWP) last() req       { f.mu.Lock(); defer f.mu.Unlock(); return f.reqs[len(f.reqs)-1] }
func (f *fakeWP) find(method, path string) []req {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []req
	for _, r := range f.reqs {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

var fixedNow = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*handlers, *fakeWP) {
	t.Helper()
	f := newFake()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	f.srvURL = srv.URL
	cfg, issue, _ := wordpress.ParseConfig(srv.URL, testUser, testPass)
	if issue != "" {
		t.Fatal(issue)
	}
	h := newHandlers(Deps{WordPress: wordpress.New(cfg), MaxMediaBytes: 1024, Now: func() time.Time { return fixedNow }})
	return h, f
}

type tcall func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)

func call(t *testing.T, h *handlers, fn tcall, args map[string]any) (map[string]any, *mcp.CallToolResult) {
	t.Helper()
	res, err := h.guard(fn)(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := res.StructuredContent.(map[string]any)
	b, _ := json.Marshal(res.StructuredContent)
	for _, secret := range []string{testPass, strings.ReplaceAll(testPass, " ", ""), base64.StdEncoding.EncodeToString([]byte(testUser + ":" + testPass))} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("secret leaked in result: %s", b)
		}
	}
	return m, res
}

func wantErr(t *testing.T, m map[string]any, res *mcp.CallToolResult, code string) {
	t.Helper()
	if m["error"] != code {
		t.Fatalf("want error %s, got %v", code, m)
	}
	if res.IsError != businessErrorIsError {
		t.Fatalf("isError = %v", res.IsError)
	}
}

func wantOK(t *testing.T, m map[string]any) {
	t.Helper()
	if m["status"] != "ok" {
		t.Fatalf("want ok, got %v", m)
	}
}

func createArgs(extra map[string]any) map[string]any {
	m := map[string]any{"title": "Hello", "content": "<p>Body</p>", "idempotency_key": "key-1"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestSchemasRegister(t *testing.T) {
	if NewWithDeps(Deps{}) == nil {
		t.Fatal("no server")
	}
}

func TestNotConfigured(t *testing.T) {
	h := newHandlers(Deps{Missing: []string{"CMS_WORDPRESS_URL"}, ConfigIssue: "missing"})
	m, res := call(t, h, h.health, nil)
	if res.IsError || m["status"] != "not_configured" || m["configured"] != false {
		t.Fatal(m)
	}
	m, _ = call(t, h, h.capabilities, nil)
	if m["configured"] != false || m["adapter"].(map[string]any)["configured"] != false {
		t.Fatal(m)
	}
	for name, fn := range map[string]tcall{"create": h.postCreate, "media": h.mediaUpload, "get": h.postGet, "update": h.postUpdate, "unpublish": h.postUnpublish} {
		m, res := call(t, h, fn, map[string]any{"bogus": 1})
		if m["error"] != "not_configured" || res.IsError != businessErrorIsError {
			t.Fatalf("%s: %v", name, m)
		}
	}
}

func TestParseConfig(t *testing.T) {
	pw := "abcdefghijklmnop"
	cases := []struct{ url, user, pw, issue string }{
		{"https://blog.example.com", "user", pw, ""},
		{"https://blog.example.com/sub/", "user", pw, ""},
		{"http://localhost:8080", "user", pw, ""},
		{"http://127.0.0.1:9", "user", pw, ""},
		{"http://[::1]:9", "user", pw, ""},
		{"http://blog.example.com", "user", pw, "url_not_https"},
		{"http://127.0.0.1.evil.com", "user", pw, "url_not_https"},
		{"ftp://blog.example.com", "user", pw, "url_invalid"},
		{"https://u:p@blog.example.com", "user", pw, "url_invalid"},
		{"https://blog.example.com/?x=1", "user", pw, "url_invalid"},
		{"not a url", "user", pw, "url_invalid"},
		{"https://blog.example.com", "user", "short", "app_password_too_short"},
		{"", "user", pw, "missing"},
		{"https://blog.example.com", "", "", "missing"},
		{"https://blog.example.com", "bob", pw, "user_too_short"},
	}
	for _, c := range cases {
		_, issue, _ := wordpress.ParseConfig(c.url, c.user, c.pw)
		if issue != c.issue {
			t.Errorf("%q: issue %q want %q", c.url, issue, c.issue)
		}
	}
}

func TestPlainHTTPRemoteRefusedByTools(t *testing.T) {
	_, issue, _ := wordpress.ParseConfig("http://blog.example.com", "u", "abcdefghijklmnop")
	h := newHandlers(Deps{ConfigIssue: issue})
	m, res := call(t, h, h.postGet, map[string]any{"post_id": 1})
	wantErr(t, m, res, "not_configured")
	if m["reason"] != "url_not_https" {
		t.Fatal(m)
	}
}

func TestBasicAuthExact(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(nil))
	wantOK(t, m)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(testUser+":"+testPass))
	if got := f.last().Auth; got != want {
		t.Fatalf("auth %q want %q", got, want)
	}
	if ct := f.last().CT; ct != "application/json" {
		t.Fatal(ct)
	}
}

func TestAuthFailed(t *testing.T) {
	h, f := setup(t)
	cfg, _, _ := wordpress.ParseConfig(f.srvURL, testUser, "wrongwrongwrongwrong")
	h.deps.WordPress = wordpress.New(cfg)
	m, res := call(t, h, h.postGet, map[string]any{"post_id": 5, "type": "post"})
	wantErr(t, m, res, "auth_failed")
	if strings.Contains(fmt.Sprint(m), "wrongwrong") {
		t.Fatal("password in error")
	}
}

func TestCreateBodyAndResult(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(map[string]any{
		"excerpt": "ex", "slug": "my-post", "categories": []int{3, 4}, "tags": []int{9}, "status": "publish", "featured_media": 77,
	}))
	wantOK(t, m)
	if m["post_id"] != 101 || m["state"] != "publish" || m["type"] != "post" || m["link"] == "" {
		t.Fatal(m)
	}
	r := f.last()
	if r.Method != "POST" || r.Path != "/wp-json/wp/v2/posts" {
		t.Fatal(r)
	}
	var body map[string]any
	json.Unmarshal(r.Body, &body)
	if body["title"] != "Hello" || body["content"] != "<p>Body</p>" || body["slug"] != "my-post" || body["status"] != "publish" || body["featured_media"] != float64(77) || len(body["categories"].([]any)) != 2 {
		t.Fatal(body)
	}
	if _, has := body["date_gmt"]; has {
		t.Fatal("unexpected date")
	}
}

func TestCreateDefaultsToDraftAndPage(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(map[string]any{"type": "page"}))
	wantOK(t, m)
	if m["state"] != "draft" || m["type"] != "page" {
		t.Fatal(m)
	}
	if f.last().Path != "/wp-json/wp/v2/pages" {
		t.Fatal(f.last().Path)
	}
	m, res := call(t, h, h.postCreate, createArgs(map[string]any{"type": "page", "categories": []int{1}, "idempotency_key": "k2"}))
	wantErr(t, m, res, "invalid_arguments")
}

func TestFutureStatus(t *testing.T) {
	h, f := setup(t)
	m, res := call(t, h, h.postCreate, createArgs(map[string]any{"status": "future"}))
	wantErr(t, m, res, "invalid_arguments")
	m, res = call(t, h, h.postCreate, createArgs(map[string]any{"status": "future", "date": "2026-11-01T10:00:00Z", "idempotency_key": "k2"}))
	wantErr(t, m, res, "invalid_arguments")
	m, res = call(t, h, h.postCreate, createArgs(map[string]any{"status": "publish", "date": "2027-01-01T10:00:00Z", "idempotency_key": "k3"}))
	wantErr(t, m, res, "invalid_arguments")
	if f.writeCount() != 0 {
		t.Fatal("a refused request reached WordPress")
	}
	m, _ = call(t, h, h.postCreate, createArgs(map[string]any{"status": "future", "date": "2026-12-25T09:30:00+01:00", "idempotency_key": "k4"}))
	wantOK(t, m)
	var body map[string]any
	json.Unmarshal(f.last().Body, &body)
	if body["status"] != "future" || body["date_gmt"] != "2026-12-25T08:30:00" {
		t.Fatal(body)
	}
}

func TestCreateValidation(t *testing.T) {
	h, f := setup(t)
	bad := []map[string]any{
		{"title": "x"}, // no key
		{"title": "x", "idempotency_key": "k", "status": "private"},
		{"title": "x", "idempotency_key": "k", "slug": "Bad Slug"},
		{"title": "x", "idempotency_key": "k", "categories": []int{0}},
		{"title": "x", "idempotency_key": "k", "type": "attachment"},
		{"title": " ", "idempotency_key": "k"},
		{"idempotency_key": "k"},
		{"title": strings.Repeat("a", 501), "idempotency_key": "k"},
		{"title": "x", "idempotency_key": "k", "unknown_field": 1},
		{"title": "x", "idempotency_key": "k", "featured_media": 0},
		{"title": "x", "idempotency_key": "k", "date": "yesterday"},
		{"content": strings.Repeat("a", maxContentBytes+1), "idempotency_key": "k"},
	}
	for i, a := range bad {
		m, res := call(t, h, h.postCreate, a)
		if m["error"] != "invalid_arguments" || res.IsError != businessErrorIsError {
			t.Fatalf("case %d: %v", i, m)
		}
	}
	if f.writeCount() != 0 {
		t.Fatal("validation must happen before any call")
	}
}

func TestInjectedFieldsAreStripped(t *testing.T) {
	h, _ := setup(t)
	a := createArgs(map[string]any{"deployment_id": "d", "organization_id": "o", "user_id": "u", "space_id": "s", "session_id": "x"})
	m, _ := call(t, h, h.postCreate, a)
	wantOK(t, m)
	m, _ = call(t, h, h.postGet, map[string]any{"post_id": 101, "deployment_id": "d", "session_id": "x", "user_id": "u", "space_id": "s", "organization_id": "o"})
	wantOK(t, m)
	m, _ = call(t, h, h.capabilities, map[string]any{"deployment_id": "d"})
	if m["version"] != serviceVersion {
		t.Fatal(m)
	}
}

func TestIdempotency(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(nil))
	wantOK(t, m)
	first := m["post_id"]
	m, _ = call(t, h, h.postCreate, createArgs(nil))
	wantOK(t, m)
	if m["post_id"] != first || m["idempotent_replay"] != true || f.writeCount() != 1 {
		t.Fatalf("%v writes=%d", m, f.writeCount())
	}
	m, res := call(t, h, h.postCreate, createArgs(map[string]any{"title": "Other"}))
	wantErr(t, m, res, "idempotency_conflict")
	if f.writeCount() != 1 {
		t.Fatal("conflict must not call WordPress")
	}
}

func TestIdempotencyReplayAfterDateElapsed(t *testing.T) {
	h, _ := setup(t)
	a := createArgs(map[string]any{"status": "future", "date": "2026-11-08T10:30:00Z"})
	m, _ := call(t, h, h.postCreate, a)
	wantOK(t, m)
	h.deps.Now = func() time.Time { return fixedNow.Add(2 * time.Hour) }
	m, _ = call(t, h, h.postCreate, a)
	wantOK(t, m)
	if m["idempotent_replay"] != true {
		t.Fatal(m)
	}
}

func TestIdempotencyExpiresAndIsBounded(t *testing.T) {
	h, f := setup(t)
	call(t, h, h.postCreate, createArgs(nil))
	h.deps.Now = func() time.Time { return fixedNow.Add(idempotencyTTL + time.Minute) }
	m, _ := call(t, h, h.postCreate, createArgs(nil))
	if m["idempotent_replay"] == true || f.writeCount() != 2 {
		t.Fatal("expired key must not replay")
	}
	h.mu.Lock()
	for i := 0; i < idempotencyMax+10; i++ {
		h.remember(fmt.Sprint("k", i), "fp", map[string]any{}, false)
	}
	n := h.idemList.Len()
	h.mu.Unlock()
	if n > idempotencyMax {
		t.Fatal(n)
	}
}

func TestExplicitRefusalFreesKey(t *testing.T) {
	h, f := setup(t)
	f.set(func() { f.mode = "403" })
	m, res := call(t, h, h.postCreate, createArgs(nil))
	wantErr(t, m, res, "forbidden")
	if m["wp_code"] != "rest_cannot_create" || m["wp_status"] != 403 {
		t.Fatal(m)
	}
	f.set(func() { f.mode = "" })
	m, _ = call(t, h, h.postCreate, createArgs(nil))
	wantOK(t, m)
}

func TestOutcomeUnknown(t *testing.T) {
	for _, mode := range []string{"drop", "500", "html"} {
		t.Run(mode, func(t *testing.T) {
			h, f := setup(t)
			f.set(func() { f.mode = mode })
			m, res := call(t, h, h.postCreate, createArgs(nil))
			wantErr(t, m, res, "outcome_unknown")
			if m["error"] == "failed" {
				t.Fatal("never failed")
			}
			f.set(func() { f.mode = "" })
			before := f.writeCount()
			m, res = call(t, h, h.postCreate, createArgs(nil))
			wantErr(t, m, res, "outcome_unknown")
			if m["idempotent_replay"] != true || f.writeCount() != before {
				t.Fatal("an unknown outcome must not be sent twice")
			}
			m, _ = call(t, h, h.postCreate, createArgs(map[string]any{"idempotency_key": "fresh"}))
			wantOK(t, m)
		})
	}
}

func TestOutcomeUnknownOnUpdateUnpublishAndMedia(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(nil))
	id := m["post_id"]
	f.set(func() { f.mode = "drop" })
	m, res := call(t, h, h.postUpdate, map[string]any{"post_id": id, "type": "post", "title": "T2", "idempotency_key": "u1"})
	wantErr(t, m, res, "outcome_unknown")
	m, res = call(t, h, h.postUnpublish, map[string]any{"post_id": id, "type": "post", "mode": "trash", "idempotency_key": "unp-563"})
	wantErr(t, m, res, "outcome_unknown")
	m, res = call(t, h, h.mediaUpload, map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "a.png", "idempotency_key": "m-1", "mime_type": "image/png"})
	wantErr(t, m, res, "outcome_unknown")
}

func TestMediaUpload(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.mediaUpload, map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "cover-1.png", "mime_type": "image/png", "alt_text": "A cover", "idempotency_key": "m-0"})
	wantOK(t, m)
	if m["media_id"] != 101 || !strings.HasSuffix(m["url"].(string), ".png") || m["alt_text_set"] != true {
		t.Fatal(m)
	}
	up := f.find("POST", "/wp-json/wp/v2/media")[0]
	if up.CT != "image/png" || up.CD != `attachment; filename="cover-1.png"` || string(up.Body) != string(png) {
		t.Fatalf("%+v", up)
	}
	if f.media[101]["alt_text"] != "A cover" {
		t.Fatal(f.media[101])
	}
}

func TestMediaBoundsAndTypes(t *testing.T) {
	h, f := setup(t)
	big := append(append([]byte{}, png...), make([]byte, 2000)...)
	cases := []struct {
		args map[string]any
		code string
	}{
		{map[string]any{"content_base64": base64.StdEncoding.EncodeToString(big), "filename": "a.png", "idempotency_key": "m-2", "mime_type": "image/png"}, "media_too_large"},
		{map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "a.png", "idempotency_key": "m-3", "mime_type": "image/gif"}, "media_type_refused"},
		{map[string]any{"content_base64": base64.StdEncoding.EncodeToString([]byte("<svg/>")), "filename": "a.svg", "idempotency_key": "m-4", "mime_type": "image/svg+xml"}, "media_type_refused"},
		{map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "../a.png", "idempotency_key": "m-5", "mime_type": "image/png"}, "invalid_arguments"},
		{map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "é.png", "idempotency_key": "m-6", "mime_type": "image/png"}, "invalid_arguments"},
		{map[string]any{"content_base64": "!!!", "filename": "a.png", "idempotency_key": "m-7", "mime_type": "image/png"}, "invalid_arguments"},
		{map[string]any{"filename": "a.png", "idempotency_key": "m-8", "mime_type": "image/png"}, "invalid_arguments"},
		{map[string]any{"content_base64": "AAAA", "buffer_ref": "buffer://x", "filename": "a.png", "idempotency_key": "m-9", "mime_type": "image/png"}, "invalid_arguments"},
		{map[string]any{"buffer_ref": "buffer://x", "filename": "a.png", "idempotency_key": "m-10", "mime_type": "image/png"}, "buffer_unavailable"},
	}
	for i, c := range cases {
		m, res := call(t, h, h.mediaUpload, c.args)
		if m["error"] != c.code || res.IsError != businessErrorIsError {
			t.Fatalf("case %d: %v want %s", i, m, c.code)
		}
	}
	if f.writeCount() != 0 {
		t.Fatal("refused media reached WordPress")
	}
}

func TestMediaFromBufferRef(t *testing.T) {
	h, f := setup(t)
	buf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer buf-token-secret" {
			w.WriteHeader(401)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/good") {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"content": map[string]any{"text": base64.StdEncoding.EncodeToString(png), "metadata": map[string]any{"encoding": "base64"}}})
	}))
	defer buf.Close()
	h.deps.Buffer = &buffer.Client{URL: buf.URL + "/api/v1/buffer", Token: "buf-token-secret", HTTP: http.DefaultClient}
	m, _ := call(t, h, h.mediaUpload, map[string]any{"buffer_ref": "buffer://good", "filename": "b.png", "idempotency_key": "m-11", "mime_type": "image/png"})
	wantOK(t, m)
	if len(f.find("POST", "/wp-json/wp/v2/media")) != 1 {
		t.Fatal("no upload")
	}
	m, res := call(t, h, h.mediaUpload, map[string]any{"buffer_ref": "buffer://gone", "filename": "b.png", "idempotency_key": "m-12", "mime_type": "image/png"})
	wantErr(t, m, res, "buffer_not_found")
}

func TestGetResolvesPostsThenPages(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(map[string]any{"type": "page"}))
	id := m["post_id"]
	m, _ = call(t, h, h.postGet, map[string]any{"post_id": id})
	wantOK(t, m)
	if m["type"] != "page" || m["state"] != "draft" || m["link"] == "" || m["date"] == "" || m["modified"] == "" {
		t.Fatal(m)
	}
	if len(f.find("GET", "/wp-json/wp/v2/posts/"+fmt.Sprint(id))) != 1 || len(f.find("GET", "/wp-json/wp/v2/pages/"+fmt.Sprint(id))) != 1 {
		t.Fatal("resolution order")
	}
	m, res := call(t, h, h.postGet, map[string]any{"post_id": 999})
	wantErr(t, m, res, "post_not_found")
	m, res = call(t, h, h.postGet, map[string]any{"post_id": 0})
	wantErr(t, m, res, "invalid_arguments")
	m, res = call(t, h, h.postGet, map[string]any{"post_id": id, "type": "post"})
	wantErr(t, m, res, "post_not_found")
}

func TestUpdatePartialAndIdempotent(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(nil))
	id := m["post_id"]
	m, _ = call(t, h, h.postUpdate, map[string]any{"post_id": id, "status": "publish", "idempotency_key": "up-1"})
	wantOK(t, m)
	if m["state"] != "publish" {
		t.Fatal(m)
	}
	var body map[string]any
	json.Unmarshal(f.last().Body, &body)
	if len(body) != 1 || body["status"] != "publish" {
		t.Fatalf("partial update must only carry the given field: %v", body)
	}
	n := f.writeCount()
	m, _ = call(t, h, h.postUpdate, map[string]any{"post_id": id, "status": "publish", "idempotency_key": "up-1"})
	if m["idempotent_replay"] != true || f.writeCount() != n {
		t.Fatal(m)
	}
	m, res := call(t, h, h.postUpdate, map[string]any{"post_id": id, "status": "draft", "idempotency_key": "up-1"})
	wantErr(t, m, res, "idempotency_conflict")
	m, res = call(t, h, h.postUpdate, map[string]any{"post_id": id, "idempotency_key": "up-2"})
	wantErr(t, m, res, "invalid_arguments")
	m, res = call(t, h, h.postUpdate, map[string]any{"post_id": 4242, "title": "x", "idempotency_key": "up-3"})
	wantErr(t, m, res, "post_not_found")
	m, res = call(t, h, h.postUpdate, map[string]any{"post_id": id, "type": "post", "title": "x", "type_": 1, "idempotency_key": "up-4"})
	wantErr(t, m, res, "invalid_arguments")
}

func TestUnpublishDraftAndTrash(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(map[string]any{"status": "publish"}))
	id := m["post_id"]
	m, _ = call(t, h, h.postUnpublish, map[string]any{"post_id": id, "mode": "draft", "idempotency_key": "unp-690"})
	wantOK(t, m)
	if m["state"] != "draft" {
		t.Fatal(m)
	}
	m, _ = call(t, h, h.postUnpublish, map[string]any{"post_id": id, "mode": "trash", "idempotency_key": "unp-695"})
	wantOK(t, m)
	if m["state"] != "trash" {
		t.Fatal(m)
	}
	dels := f.find("DELETE", "/wp-json/wp/v2/posts/"+fmt.Sprint(id))
	if len(dels) != 1 || strings.Contains(dels[0].Query, "force") {
		t.Fatalf("trash must be a DELETE without force: %+v", dels)
	}
	if _, still := f.posts[int(id.(int))]; !still {
		t.Fatal("post must still exist in the trash")
	}
	m, _ = call(t, h, h.postUnpublish, map[string]any{"post_id": id, "mode": "trash", "idempotency_key": "unp-707"})
	wantOK(t, m)
	if m["already_trashed"] != true {
		t.Fatal(m)
	}
	m, res := call(t, h, h.postUnpublish, map[string]any{"post_id": id, "mode": "delete", "idempotency_key": "unp-712"})
	wantErr(t, m, res, "invalid_arguments")
	m, res = call(t, h, h.postUnpublish, map[string]any{"post_id": id, "mode": "trash", "force": true, "idempotency_key": "unp-714"})
	wantErr(t, m, res, "invalid_arguments")
	for _, r := range f.reqs {
		if strings.Contains(r.Query, "force") {
			t.Fatal("force reached WordPress")
		}
	}
}

func TestTrashNotSupportedNeverForces(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(nil))
	f.set(func() { f.mode = "trash501" })
	m2, res := call(t, h, h.postUnpublish, map[string]any{"post_id": m["post_id"], "type": "post", "mode": "trash", "idempotency_key": "unp-727"})
	wantErr(t, m2, res, "trash_not_supported")
	for _, r := range f.reqs {
		if strings.Contains(r.Query, "force") {
			t.Fatal("force reached WordPress")
		}
	}
}

func TestWordPressErrorMapping(t *testing.T) {
	cases := []struct{ mode, code string }{
		{"403", "forbidden"}, {"429", "rate_limited"}, {"noroute", "rest_api_unavailable"}, {"redirect", "upstream_redirect"}, {"html", "upstream_invalid_response"}, {"big", "upstream_invalid_response"},
	}
	for _, c := range cases {
		h, f := setup(t)
		f.set(func() { f.mode = c.mode })
		m, res := call(t, h, h.postGet, map[string]any{"post_id": 5, "type": "post"})
		wantErr(t, m, res, c.code)
		if c.mode == "429" && m["retry_after_s"] != 120 {
			t.Fatal(m)
		}
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	h, f := setup(t)
	f.set(func() { f.mode = "redirect" })
	m, res := call(t, h, h.postCreate, createArgs(nil))
	wantErr(t, m, res, "upstream_redirect")
	if strings.Contains(fmt.Sprint(m), "elsewhere") {
		t.Fatal("redirect destination leaked")
	}
	if len(f.reqs) != 1 {
		t.Fatal("redirect followed")
	}
}

func TestSecretsAndURLMaskedInErrors(t *testing.T) {
	h, f := setup(t)
	f.set(func() { f.mode = "leakmsg" })
	m, res := call(t, h, h.postCreate, createArgs(nil))
	wantErr(t, m, res, "wordpress_error")
	s := fmt.Sprint(m)
	if strings.Contains(s, testPass) || strings.Contains(s, testUser) || strings.Contains(s, f.srvURL) {
		t.Fatalf("secret in error: %s", s)
	}
	if !strings.Contains(s, "[redacted]") {
		t.Fatal(s)
	}
	// Success results keep the links WordPress returns.
	f.set(func() { f.mode = "" })
	m, _ = call(t, h, h.postCreate, createArgs(map[string]any{"idempotency_key": "ok"}))
	if !strings.HasPrefix(m["link"].(string), f.srvURL) {
		t.Fatal(m)
	}
	cap, _ := call(t, h, h.capabilities, nil)
	if b, _ := json.Marshal(cap); strings.Contains(string(b), f.srvURL) || strings.Contains(string(b), testUser) {
		t.Fatal("capabilities must carry no value")
	}
}

func TestRedactHelper(t *testing.T) {
	got := redact(`pw=abcd "q\"x" ab`, []string{"abcd", "ab", ""})
	if strings.Contains(got, "abcd") || strings.Contains(got, "ab") || !strings.Contains(got, "[redacted]") {
		t.Fatal(got)
	}
}

func TestCapabilities(t *testing.T) {
	h, _ := setup(t)
	m, res := call(t, h, h.capabilities, nil)
	if res.IsError || m["configured"] != true {
		t.Fatal(m)
	}
	if m["unpublish"].(map[string]any)["permanent_delete"] != false {
		t.Fatal(m)
	}
	m, _ = call(t, h, h.health, nil)
	if m["status"] != "ok" {
		t.Fatal(m)
	}
}

func TestMediaIdempotency(t *testing.T) {
	h, f := setup(t)
	args := map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "a.png", "mime_type": "image/png", "idempotency_key": "media-key"}
	m, _ := call(t, h, h.mediaUpload, args)
	wantOK(t, m)
	first := m["media_id"]
	m, _ = call(t, h, h.mediaUpload, args)
	wantOK(t, m)
	if m["media_id"] != first || m["idempotent_replay"] != true || len(f.find("POST", "/wp-json/wp/v2/media")) != 1 {
		t.Fatalf("replay must not upload again: %v", m)
	}
	other := map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "b.png", "mime_type": "image/png", "idempotency_key": "media-key"}
	m, res := call(t, h, h.mediaUpload, other)
	wantErr(t, m, res, "idempotency_conflict")
	noKey := map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "a.png", "mime_type": "image/png"}
	m, res = call(t, h, h.mediaUpload, noKey)
	wantErr(t, m, res, "invalid_arguments")
	// An unknown outcome is memorized and not re-sent.
	f.set(func() { f.mode = "drop" })
	u := map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "c.png", "mime_type": "image/png", "idempotency_key": "media-unknown"}
	m, res = call(t, h, h.mediaUpload, u)
	wantErr(t, m, res, "outcome_unknown")
	f.set(func() { f.mode = "" })
	before := f.writeCount()
	m, res = call(t, h, h.mediaUpload, u)
	wantErr(t, m, res, "outcome_unknown")
	if m["idempotent_replay"] != true || f.writeCount() != before {
		t.Fatal("unknown outcome re-sent")
	}
}

func TestShortUserRefusedAtConfiguration(t *testing.T) {
	_, issue, _ := wordpress.ParseConfig("https://blog.example.com", "bob", "abcdefghijklmnop")
	if issue != "user_too_short" {
		t.Fatal(issue)
	}
	h := newHandlers(Deps{ConfigIssue: issue})
	m, res := call(t, h, h.postGet, map[string]any{"post_id": 1})
	wantErr(t, m, res, "not_configured")
	if m["reason"] != "user_too_short" {
		t.Fatal(m)
	}
}

func TestFourCharUserIsMasked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"code":"rest_cannot_create","message":"Access denied for root","data":{"status":403}}`))
	}))
	defer srv.Close()
	cfg, issue, _ := wordpress.ParseConfig(srv.URL, "root", testPass)
	if issue != "" {
		t.Fatal(issue)
	}
	h := newHandlers(Deps{WordPress: wordpress.New(cfg), Now: func() time.Time { return fixedNow }})
	m, res := call(t, h, h.postCreate, createArgs(nil))
	wantErr(t, m, res, "forbidden")
	if strings.Contains(fmt.Sprint(m), "root") || !strings.Contains(fmt.Sprint(m), "[redacted]") {
		t.Fatalf("user not masked: %v", m)
	}
}

func TestIncompleteWriteResponseIsOutcomeUnknown(t *testing.T) {
	for _, mode := range []string{"idonly", "badtypes"} {
		t.Run(mode, func(t *testing.T) {
			h, f := setup(t)
			f.set(func() { f.mode = mode })
			a := createArgs(map[string]any{"status": "publish"})
			m, res := call(t, h, h.postCreate, a)
			wantErr(t, m, res, "outcome_unknown")
			if m["post_id"] != nil || m["state"] != nil || m["status"] == "ok" {
				t.Fatalf("no partial success: %v", m)
			}
			before := f.writeCount()
			m, res = call(t, h, h.postCreate, a)
			wantErr(t, m, res, "outcome_unknown")
			if m["idempotent_replay"] != true || f.writeCount() != before {
				t.Fatal("the uncertainty must be memorized, not re-sent")
			}
			m, res = call(t, h, h.mediaUpload, map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "a.png", "mime_type": "image/png", "idempotency_key": "mk"})
			wantErr(t, m, res, "outcome_unknown")
		})
	}
}

func TestIncompleteWriteResponseOnUpdateAndUnpublish(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(nil))
	id := m["post_id"]
	f.set(func() { f.mode = "idonly" })
	m, res := call(t, h, h.postUpdate, map[string]any{"post_id": id, "type": "post", "status": "publish", "idempotency_key": "uk"})
	wantErr(t, m, res, "outcome_unknown")
	for _, mode := range []string{"draft", "trash"} {
		m, res = call(t, h, h.postUnpublish, map[string]any{"post_id": id, "type": "post", "mode": mode, "idempotency_key": "unp-" + mode})
		wantErr(t, m, res, "outcome_unknown")
	}
}

func TestIncompleteReadIsInvalidResponse(t *testing.T) {
	h, f := setup(t)
	f.set(func() { f.mode = "idonlyall" })
	m, res := call(t, h, h.postGet, map[string]any{"post_id": 5, "type": "post"})
	wantErr(t, m, res, "upstream_invalid_response")
}

func TestUnpublishIdempotency(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(map[string]any{"status": "publish"}))
	id := m["post_id"]
	args := map[string]any{"post_id": id, "type": "post", "mode": "draft", "idempotency_key": "unp-key"}
	m, _ = call(t, h, h.postUnpublish, args)
	wantOK(t, m)
	n := f.writeCount()
	m, _ = call(t, h, h.postUnpublish, args)
	wantOK(t, m)
	if m["idempotent_replay"] != true || f.writeCount() != n {
		t.Fatalf("replay must not write again: %v", m)
	}
	m, res := call(t, h, h.postUnpublish, map[string]any{"post_id": id, "type": "post", "mode": "trash", "idempotency_key": "unp-key"})
	wantErr(t, m, res, "idempotency_conflict")
	m, res = call(t, h, h.postUnpublish, map[string]any{"post_id": id, "type": "post", "mode": "trash"})
	wantErr(t, m, res, "invalid_arguments")
	// unknown outcome: memorized, not re-sent
	f.set(func() { f.mode = "drop" })
	u := map[string]any{"post_id": id, "type": "post", "mode": "trash", "idempotency_key": "unp-unknown"}
	m, res = call(t, h, h.postUnpublish, u)
	wantErr(t, m, res, "outcome_unknown")
	f.set(func() { f.mode = "" })
	before := f.writeCount()
	m, res = call(t, h, h.postUnpublish, u)
	wantErr(t, m, res, "outcome_unknown")
	if m["idempotent_replay"] != true || f.writeCount() != before {
		t.Fatal("unknown outcome re-sent")
	}
}

func TestAltTextOutcome(t *testing.T) {
	cases := []struct {
		mode string
		want any
	}{{"drop", "unknown"}, {"500", "unknown"}, {"bad", "unknown"}, {"noalt", "unknown"}, {"emptyalt", "unknown"}, {"otheralt", "unknown"}, {"otherid", "unknown"}, {"403", false}, {"", true}}
	for i, c := range cases {
		t.Run("alt_"+c.mode, func(t *testing.T) {
			h, f := setup(t)
			f.set(func() { f.altMode = c.mode })
			m, res := call(t, h, h.mediaUpload, map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "a.png", "mime_type": "image/png", "alt_text": "Alt", "idempotency_key": fmt.Sprint("alt-", i)})
			if res.IsError != false {
				t.Fatalf("the media exists: not an error: %v", m)
			}
			wantOK(t, m)
			if m["media_id"] == nil || m["alt_text_set"] != c.want {
				t.Fatalf("alt_text_set = %v (%T), want %v: %v", m["alt_text_set"], m["alt_text_set"], c.want, m)
			}
			if c.want != true && m["warning"] == nil {
				t.Fatal("warning expected")
			}
			// the stored result replays identically
			m2, _ := call(t, h, h.mediaUpload, map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "a.png", "mime_type": "image/png", "alt_text": "Alt", "idempotency_key": fmt.Sprint("alt-", i)})
			if m2["idempotent_replay"] != true || m2["alt_text_set"] != c.want {
				t.Fatal(m2)
			}
		})
	}
}

func TestMaskingNeverTouchesKeys(t *testing.T) {
	cfg, issue, _ := wordpress.ParseConfig("https://blog.example.com", "status", testPass)
	if issue != "" {
		t.Fatal(issue)
	}
	h := newHandlers(Deps{WordPress: wordpress.New(cfg), Now: func() time.Time { return fixedNow }})
	m, res := call(t, h, h.health, nil)
	if res.IsError || m["status"] != "ok" || m["configured"] != true || m["version"] != serviceVersion {
		t.Fatalf("health contract broken: %v", m)
	}
	// A write-shaped success and an error that both contain the secret as a value.
	fn := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return result(map[string]any{"status": "ok", "post_id": 7, "state": "publish", "link": "https://x/status/", "tags": []string{"status"}, "nested": map[string]any{"status": "status"}}), nil
	}
	out, _ := h.guard(fn)(context.Background(), mcp.CallToolRequest{})
	got := out.StructuredContent.(map[string]any)
	if got["status"] != "ok" || got["state"] != "publish" || got["link"] != "https://x/status/" {
		t.Fatalf("keys must survive, values are masked: %v", got)
	}
	if got["nested"].(map[string]any)["status"] != "status" || got["tags"].([]any)[0] != "[redacted]" {
		t.Fatalf("whole-value masking outside the contract enums: %v", got)
	}
	efn := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return failure("forbidden", "Access denied for status"), nil
	}
	out, _ = h.guard(efn)(context.Background(), mcp.CallToolRequest{})
	got = out.StructuredContent.(map[string]any)
	if got["error"] != "forbidden" || got["message"] != "Access denied for [redacted]" {
		t.Fatalf("%v", got)
	}
}

func TestWriteAnswerForAnotherItemIsOutcomeUnknown(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(nil))
	id := m["post_id"]
	f.set(func() { f.mode = "otheritem" })
	for i, mode := range []string{"trash", "draft"} {
		m, res := call(t, h, h.postUnpublish, map[string]any{"post_id": id, "type": "post", "mode": mode, "idempotency_key": fmt.Sprint("o-", i)})
		wantErr(t, m, res, "outcome_unknown")
		if m["status"] == "ok" {
			t.Fatal("false success")
		}
	}
	m, res := call(t, h, h.postUpdate, map[string]any{"post_id": id, "type": "post", "title": "x", "idempotency_key": "o-upd"})
	wantErr(t, m, res, "outcome_unknown")
	// The uncertainty is memorized, not replayed as a success.
	before := f.writeCount()
	m, res = call(t, h, h.postUnpublish, map[string]any{"post_id": id, "type": "post", "mode": "trash", "idempotency_key": "o-0"})
	wantErr(t, m, res, "outcome_unknown")
	if m["idempotent_replay"] != true || f.writeCount() != before {
		t.Fatal("replay must not re-send")
	}
	// create: right shape, wrong type (a page answer for a post)
	m, res = call(t, h, h.postCreate, createArgs(map[string]any{"idempotency_key": "o-create"}))
	wantErr(t, m, res, "outcome_unknown")
	f.set(func() { f.mode = "" })
	// same id but another type
	m, _ = call(t, h, h.postCreate, createArgs(map[string]any{"idempotency_key": "o-ok"}))
	f.set(func() { f.mode = "othertype"; f.targetID = int(m["post_id"].(int)) })
	m2, res := call(t, h, h.postUnpublish, map[string]any{"post_id": m["post_id"], "type": "post", "mode": "trash", "idempotency_key": "o-type"})
	wantErr(t, m2, res, "outcome_unknown")
}

func TestMediaAnswerMustBeAnAttachment(t *testing.T) {
	h, f := setup(t)
	f.set(func() { f.mode = "noattach" })
	m, res := call(t, h, h.mediaUpload, map[string]any{"content_base64": base64.StdEncoding.EncodeToString(png), "filename": "a.png", "mime_type": "image/png", "idempotency_key": "na"})
	wantErr(t, m, res, "outcome_unknown")
}

func TestReadAnswerForAnotherItemIsInvalid(t *testing.T) {
	h, f := setup(t)
	f.set(func() { f.mode = "otheritem"; f.otherRead = true })
	m, res := call(t, h, h.postGet, map[string]any{"post_id": 123, "type": "post"})
	wantErr(t, m, res, "upstream_invalid_response")
}

func TestWriteMustReturnTheRequestedStatus(t *testing.T) {
	h, f := setup(t)
	m, _ := call(t, h, h.postCreate, createArgs(map[string]any{"status": "publish"}))
	id := m["post_id"]
	f.set(func() { f.mode = "wrongstatus"; f.wrongStatus = "publish" })
	// Codex case: trash answered with a coherent but published item.
	m, res := call(t, h, h.postUnpublish, map[string]any{"post_id": id, "type": "post", "mode": "trash", "idempotency_key": "s-trash"})
	wantErr(t, m, res, "outcome_unknown")
	m, res = call(t, h, h.postUnpublish, map[string]any{"post_id": id, "type": "post", "mode": "draft", "idempotency_key": "s-draft"})
	wantErr(t, m, res, "outcome_unknown")
	m, res = call(t, h, h.postUpdate, map[string]any{"post_id": id, "type": "post", "status": "draft", "idempotency_key": "s-upd"})
	wantErr(t, m, res, "outcome_unknown")
	m, res = call(t, h, h.postCreate, createArgs(map[string]any{"status": "draft", "idempotency_key": "s-create"}))
	wantErr(t, m, res, "outcome_unknown")
	before := f.writeCount()
	m, res = call(t, h, h.postCreate, createArgs(map[string]any{"status": "draft", "idempotency_key": "s-create"}))
	wantErr(t, m, res, "outcome_unknown")
	if m["idempotent_replay"] != true || f.writeCount() != before {
		t.Fatal("replay must not re-send")
	}
	// an update without a requested status is not checked
	m, _ = call(t, h, h.postUpdate, map[string]any{"post_id": id, "type": "post", "title": "T", "idempotency_key": "s-title"})
	wantOK(t, m)
}

func TestFutureMayComeBackPublishOnlyWhenDatePassed(t *testing.T) {
	h, f := setup(t)
	f.set(func() { f.mode = "wrongstatus"; f.wrongStatus = "publish" })
	a := createArgs(map[string]any{"status": "future", "date": "2026-11-08T10:30:00Z", "idempotency_key": "f1"})
	m, res := call(t, h, h.postCreate, a)
	wantErr(t, m, res, "outcome_unknown") // date still ahead: publish is not what was asked
	h.deps.Now = func() time.Time { return fixedNow.Add(time.Hour) }
	// the date passed while the request was in flight: publish is the natural result
	f.set(func() { f.mode = "wrongstatus"; f.wrongStatus = "publish" })
	ok := h.statusConfirmed("future", "2026-11-08T10:30:00Z", "publish")
	if !ok || h.statusConfirmed("future", "2026-12-08T10:30:00Z", "publish") || h.statusConfirmed("draft", "", "publish") {
		t.Fatal("status confirmation rules")
	}
}

func TestUserNameNotMaskedBySubstringInSuccess(t *testing.T) {
	cfg, issue, _ := wordpress.ParseConfig("https://blog.example.com", "publish", testPass)
	if issue != "" {
		t.Fatal(issue)
	}
	h := newHandlers(Deps{WordPress: wordpress.New(cfg), Now: func() time.Time { return fixedNow }})
	ok := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return result(map[string]any{"status": "ok", "state": "publish", "type": "post", "link": "https://blog.example.com/publish-now/", "slug": "publish-now", "title": "publish", "note": "pw " + testPass}), nil
	}
	out, _ := h.guard(ok)(context.Background(), mcp.CallToolRequest{})
	got := out.StructuredContent.(map[string]any)
	if got["state"] != "publish" || got["link"] != "https://blog.example.com/publish-now/" || got["slug"] != "publish-now" {
		t.Fatalf("success contract altered: %v", got)
	}
	if got["title"] != "[redacted]" { // whole value of a free field
		t.Fatalf("whole-value user must be masked: %v", got)
	}
	if strings.Contains(fmt.Sprint(got["note"]), testPass) {
		t.Fatal("password must be masked by substring everywhere")
	}
	// In an error message from WordPress the user is masked by substring, the code is left alone.
	bad := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		r := failure("forbidden", "user publish may not publish at https://blog.example.com")
		r.StructuredContent.(map[string]any)["wp_code"] = "rest_cannot_publish"
		return r, nil
	}
	out, _ = h.guard(bad)(context.Background(), mcp.CallToolRequest{})
	got = out.StructuredContent.(map[string]any)
	if got["message"] != "user [redacted] may not [redacted] at [redacted]" || got["wp_code"] != "rest_cannot_publish" || got["error"] != "forbidden" {
		t.Fatalf("%v", got)
	}
}
