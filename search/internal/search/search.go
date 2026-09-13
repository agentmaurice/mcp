// Package search exposes a bounded read-only projection of one authorized corpus.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxQueryBytes    = 4096
	MaxPassageBytes  = 16 * 1024
	MaxResponseBytes = 256 * 1024
	MaxLimit         = 20
	maxBackendBytes  = 512 * 1024
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var sourceReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,511}$`)

type Error string

func (e Error) Error() string { return string(e) }

const (
	InvalidArgument    Error = "invalid_argument"
	ScopeRequired      Error = "scope_required"
	NotFound           Error = "not_found"
	ModeUnavailable    Error = "mode_unavailable"
	BackendUnavailable Error = "backend_unavailable"
	ResponseTooLarge   Error = "response_too_large"
)

// Config is trusted operator configuration, never model-supplied tool input.
// A process serves one principal and one homogeneous ACL corpus only.
type Config struct {
	URL            string `json:"meili_url"`
	OrganizationID string `json:"organization_id"`
	DeploymentID   string `json:"deployment_id"`
	PrincipalID    string `json:"principal_id"`
	Corpus         string `json:"corpus"`
	IndexUID       string `json:"index_uid"`
	IndexRevision  string `json:"index_revision"`
	Embedder       string `json:"embedder,omitempty"`
}

type Service struct {
	config Config
	apiKey string
	client *http.Client
}

// FromEnv reads explicit configuration and a file-mounted search credential.
func FromEnv() (*Service, error) {
	configData, err := readSmallFile(os.Getenv("SEARCH_CONFIG_FILE"), 8192)
	if err != nil {
		return nil, ScopeRequired
	}
	var config Config
	if err := Decode(configData, &config); err != nil {
		return nil, ScopeRequired
	}
	key, err := readSmallFile(os.Getenv("SEARCH_API_KEY_FILE"), 8192)
	if err != nil {
		return nil, ScopeRequired
	}
	return New(config, strings.TrimSpace(string(key)))
}

func readSmallFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, ScopeRequired
	}
	return b, nil
}

func New(config Config, key string) (*Service, error) {
	for _, id := range []string{config.OrganizationID, config.DeploymentID, config.PrincipalID, config.Corpus, config.IndexUID, config.IndexRevision} {
		if !identifier.MatchString(id) {
			return nil, ScopeRequired
		}
	}
	if config.Embedder != "" && !identifier.MatchString(config.Embedder) {
		return nil, InvalidArgument
	}
	u, err := url.Parse(config.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, InvalidArgument
	}
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n\t ") {
		return nil, ScopeRequired
	}
	config.URL = strings.TrimRight(config.URL, "/")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// This is a configured private backend, not an arbitrary internet proxy.
	transport.Proxy = nil
	return &Service{config: config, apiKey: key, client: &http.Client{
		Timeout: 10 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Decode rejects additional fields and trailing JSON on every transport.
func Decode(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return InvalidArgument
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return InvalidArgument
	}
	return nil
}

type Query struct {
	Corpus string `json:"corpus"`
	Query  string `json:"query"`
	Mode   string `json:"mode,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type Get struct {
	Corpus    string `json:"corpus"`
	PassageID string `json:"passage_id"`
}

type Passage struct {
	ID             string `json:"id"`
	DocumentID     string `json:"document_id"`
	Title          string `json:"title"`
	Content        string `json:"content"`
	SourceRef      string `json:"source_ref"`
	Position       string `json:"position"`
	SourceRevision string `json:"source_revision"`
}

type indexedPassage struct {
	Passage
	OrganizationID string `json:"organization_id"`
	DeploymentID   string `json:"deployment_id"`
	Corpus         string `json:"corpus"`
	IndexRevision  string `json:"index_revision"`
}

type Response struct {
	Status        string    `json:"status"`
	Results       []Passage `json:"results"`
	Mode          string    `json:"mode"`
	IndexRevision string    `json:"index_revision"`
	Truncated     bool      `json:"truncated"`
}

func (s *Service) Modes() []string {
	if s.config.Embedder == "" {
		return []string{"keyword"}
	}
	return []string{"keyword", "semantic", "hybrid"}
}

func (s *Service) Capabilities() map[string]any {
	return map[string]any{"status": "ok", "corpora": []string{s.config.Corpus}, "modes": s.Modes(), "max_query_bytes": MaxQueryBytes, "max_passage_bytes": MaxPassageBytes, "max_response_bytes": MaxResponseBytes, "max_limit": MaxLimit, "identity_mode": "dedicated_stdio", "managed_multiuser": false}
}

func (s *Service) Query(ctx context.Context, q Query) (*Response, error) {
	if q.Corpus != s.config.Corpus {
		return nil, NotFound
	}
	if !utf8.ValidString(q.Query) || strings.TrimSpace(q.Query) == "" || len(q.Query) > MaxQueryBytes {
		return nil, InvalidArgument
	}
	if q.Limit == 0 {
		q.Limit = 5
	}
	if q.Limit < 1 || q.Limit > MaxLimit {
		return nil, InvalidArgument
	}
	if q.Mode == "" {
		q.Mode = "hybrid"
	}
	if q.Mode != "keyword" && q.Mode != "semantic" && q.Mode != "hybrid" {
		return nil, InvalidArgument
	}
	if q.Mode != "keyword" && s.config.Embedder == "" {
		return nil, ModeUnavailable
	}
	return s.search(ctx, q.Query, q.Mode, q.Limit, "")
}

func (s *Service) Get(ctx context.Context, g Get) (*Response, error) {
	if g.Corpus != s.config.Corpus {
		return nil, NotFound
	}
	if !identifier.MatchString(g.PassageID) {
		return nil, InvalidArgument
	}
	r, err := s.search(ctx, "", "keyword", 1, g.PassageID)
	if err != nil {
		return nil, err
	}
	if len(r.Results) != 1 {
		return nil, NotFound
	}
	return r, nil
}

func quoted(value string) string { b, _ := json.Marshal(value); return string(b) }

func (s *Service) search(ctx context.Context, query, mode string, limit int, id string) (*Response, error) {
	c := s.config
	filters := []string{"organization_id = " + quoted(c.OrganizationID), "deployment_id = " + quoted(c.DeploymentID), "corpus = " + quoted(c.Corpus), "index_revision = " + quoted(c.IndexRevision)}
	if id != "" {
		filters = append(filters, "id = "+quoted(id))
	}
	request := map[string]any{"q": query, "limit": limit + 1, "filter": filters, "attributesToRetrieve": []string{"id", "document_id", "title", "content", "source_ref", "position", "source_revision", "organization_id", "deployment_id", "corpus", "index_revision"}}
	if mode != "keyword" {
		ratio := 0.5
		if mode == "semantic" {
			ratio = 1
		}
		request["hybrid"] = map[string]any{"embedder": c.Embedder, "semanticRatio": ratio}
	}
	var data struct {
		Hits []indexedPassage `json:"hits"`
	}
	if err := s.request(ctx, http.MethodPost, "/indexes/"+c.IndexUID+"/search", request, &data); err != nil {
		return nil, err
	}
	if data.Hits == nil || len(data.Hits) > limit+1 {
		return nil, BackendUnavailable
	}
	out := &Response{Status: "ok", Results: []Passage{}, Mode: mode, IndexRevision: c.IndexRevision, Truncated: len(data.Hits) > limit}
	for i, hit := range data.Hits {
		// Fail closed even if an incorrectly configured backend ignores filters.
		if hit.OrganizationID != c.OrganizationID || hit.DeploymentID != c.DeploymentID || hit.Corpus != c.Corpus || hit.IndexRevision != c.IndexRevision || (id != "" && hit.ID != id) {
			return nil, BackendUnavailable
		}
		if !identifier.MatchString(hit.ID) || !identifier.MatchString(hit.DocumentID) || hit.Title == "" || !sourceReference.MatchString(hit.SourceRef) || strings.Contains(hit.SourceRef, "://") || hit.Position == "" || hit.SourceRevision == "" {
			return nil, BackendUnavailable
		}
		if len(hit.Content) > MaxPassageBytes {
			return nil, ResponseTooLarge
		}
		if i == limit {
			break
		}
		out.Results = append(out.Results, hit.Passage)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, BackendUnavailable
	}
	if len(b) > MaxResponseBytes {
		return nil, ResponseTooLarge
	}
	return out, nil
}

func (s *Service) Health(ctx context.Context) error {
	var result struct {
		Status string `json:"status"`
	}
	if err := s.request(ctx, http.MethodGet, "/health", nil, &result); err != nil {
		return err
	}
	if result.Status != "available" {
		return BackendUnavailable
	}
	return nil
}

func (s *Service) request(ctx context.Context, method, path string, body, dst any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return InvalidArgument
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.config.URL+path, reader)
	if err != nil {
		return BackendUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return BackendUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return BackendUnavailable
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxBackendBytes+1))
	if err != nil {
		return BackendUnavailable
	}
	if len(b) > maxBackendBytes {
		return ResponseTooLarge
	}
	if !utf8.Valid(b) || json.Unmarshal(b, dst) != nil {
		return BackendUnavailable
	}
	return nil
}

func Code(err error) string {
	var known Error
	if errors.As(err, &known) {
		return string(known)
	}
	return string(BackendUnavailable)
}
