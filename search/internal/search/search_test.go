package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func config(url string) Config {
	return Config{URL: url, OrganizationID: "org-a", DeploymentID: "dep-a", PrincipalID: "alice", Corpus: "manual", IndexUID: "manual_v1", IndexRevision: "v1", Embedder: "test"}
}

func fixture() indexedPassage {
	return indexedPassage{Passage: Passage{ID: "p1", DocumentID: "doc1", Title: "Procédure", Content: "Résilier le contrat.", SourceRef: "storage:doc1", Position: "section-1", SourceRevision: "r1"}, OrganizationID: "org-a", DeploymentID: "dep-a", Corpus: "manual", IndexRevision: "v1"}
}

func TestScopeAppliedBeforeSearchAndGet(t *testing.T) {
	for _, get := range []bool{false, true} {
		t.Run(map[bool]string{false: "query", true: "get"}[get], func(t *testing.T) {
			called := false
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.Method != "POST" || r.URL.Path != "/indexes/manual_v1/search" || r.Header.Get("Authorization") != "Bearer search-only" {
					t.Error("unexpected backend request")
				}
				var data map[string]any
				if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
					t.Fatal(err)
				}
				filters, _ := json.Marshal(data["filter"])
				for _, required := range []string{`organization_id = \"org-a\"`, `deployment_id = \"dep-a\"`, `corpus = \"manual\"`, `index_revision = \"v1\"`} {
					if !strings.Contains(string(filters), required) {
						t.Errorf("missing mandatory filter %s in %s", required, filters)
					}
				}
				if get && !strings.Contains(string(filters), `id = \"p1\"`) {
					t.Error("get bypassed id filter")
				}
				json.NewEncoder(w).Encode(map[string]any{"hits": []indexedPassage{fixture()}})
			}))
			defer backend.Close()
			s, err := New(config(backend.URL), "search-only")
			if err != nil {
				t.Fatal(err)
			}
			var response *Response
			if get {
				response, err = s.Get(context.Background(), Get{Corpus: "manual", PassageID: "p1"})
			} else {
				response, err = s.Query(context.Background(), Query{Corpus: "manual", Query: "résilier", Mode: "hybrid"})
			}
			if err != nil || !called || len(response.Results) != 1 {
				t.Fatalf("response=%+v error=%v called=%v", response, err, called)
			}
		})
	}
}

func TestForbiddenInputsNeverReachBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("backend must not be called") }))
	defer backend.Close()
	s, _ := New(config(backend.URL), "search-only")
	for _, q := range []Query{{Corpus: "other", Query: "x"}, {Corpus: "manual", Query: " "}, {Corpus: "manual", Query: strings.Repeat("é", 2049)}, {Corpus: "manual", Query: "x", Mode: "auto"}, {Corpus: "manual", Query: "x", Limit: 21}, {Corpus: "manual", Query: "x", Limit: -1}} {
		if _, err := s.Query(context.Background(), q); err == nil {
			t.Errorf("accepted %+v", q)
		}
	}
	if _, err := s.Get(context.Background(), Get{Corpus: "other", PassageID: "p1"}); err != NotFound {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), Get{Corpus: "manual", PassageID: `p1" OR id EXISTS`}); err != InvalidArgument {
		t.Fatal(err)
	}
	c := config(backend.URL)
	c.Embedder = ""
	keywordOnly, _ := New(c, "search-only")
	if _, err := keywordOnly.Query(context.Background(), Query{Corpus: "manual", Query: "x"}); err != ModeUnavailable {
		t.Fatal(err)
	}
}

func TestBackendCannotLeakWrongScopeOrOversizedPassage(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*indexedPassage)
		want   error
	}{
		{"organization", func(p *indexedPassage) { p.OrganizationID = "org-b" }, BackendUnavailable},
		{"deployment", func(p *indexedPassage) { p.DeploymentID = "dep-b" }, BackendUnavailable},
		{"corpus", func(p *indexedPassage) { p.Corpus = "private" }, BackendUnavailable},
		{"revision", func(p *indexedPassage) { p.IndexRevision = "old" }, BackendUnavailable},
		{"provenance", func(p *indexedPassage) { p.SourceRef = "" }, BackendUnavailable},
		{"signed_url", func(p *indexedPassage) { p.SourceRef = "https://files.example/doc?signature=secret" }, BackendUnavailable},
		{"size", func(p *indexedPassage) { p.Content = strings.Repeat("a", MaxPassageBytes+1) }, ResponseTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := fixture()
			test.change(&p)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"hits": []indexedPassage{p}})
			}))
			defer backend.Close()
			s, _ := New(config(backend.URL), "search-only")
			if out, err := s.Query(context.Background(), Query{Corpus: "manual", Query: "x"}); err != test.want || out != nil {
				t.Fatalf("out=%+v err=%v", out, err)
			}
		})
	}
}

func TestRedirectDoesNotForwardCredential(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect followed") }))
	defer other.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer backend.Close()
	s, _ := New(config(backend.URL), "search-only")
	if err := s.Health(context.Background()); err != BackendUnavailable {
		t.Fatal(err)
	}
}

func TestBackendErrorsAreSanitizedAndBounded(t *testing.T) {
	for _, body := range []string{`{"message":"secret-credential"}`, strings.Repeat("x", maxBackendBytes+1)} {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(body)) }))
		s, _ := New(config(backend.URL), "search-only")
		_, err := s.Query(context.Background(), Query{Corpus: "manual", Query: "x"})
		if err == nil || strings.Contains(err.Error(), "secret-credential") {
			t.Fatal(err)
		}
		backend.Close()
	}
}

func TestExplicitTruncationAndMissingPassage(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"hits": []indexedPassage{fixture(), fixture()}})
	}))
	defer backend.Close()
	s, _ := New(config(backend.URL), "search-only")
	r, err := s.Query(context.Background(), Query{Corpus: "manual", Query: "x", Limit: 1})
	if err != nil || !r.Truncated || len(r.Results) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"hits":[]}`)) }))
	defer empty.Close()
	s, _ = New(config(empty.URL), "search-only")
	if _, err := s.Get(context.Background(), Get{Corpus: "manual", PassageID: "missing"}); err != NotFound {
		t.Fatal(err)
	}
}

func TestConfigurationRequiresBindingAndRejectsCredentialURL(t *testing.T) {
	c := config("http://127.0.0.1:7700")
	c.PrincipalID = ""
	if _, err := New(c, "key"); err != ScopeRequired {
		t.Fatal(err)
	}
	for _, u := range []string{"file:///tmp/db", "http://user:secret@localhost:7700", "http://localhost:7700?key=secret", "http://localhost:7700/path"} {
		if _, err := New(config(u), "key"); err == nil {
			t.Fatal("accepted URL", u)
		}
	}
	if _, err := New(config("http://localhost:7700"), "\n"); err != ScopeRequired {
		t.Fatal(err)
	}
}
