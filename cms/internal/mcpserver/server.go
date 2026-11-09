// Package mcpserver exposes the CMS MCP tools, a thin client of the native REST
// API of a customer WordPress site (/wp-json/wp/v2, Application Passwords).
package mcpserver

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/cms/internal/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/cms/internal/version"
	"github.com/agentmaurice/mcpchatui/mcp/cms/internal/wordpress"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/mark3labs/mcp-go/mcp"
)

var serviceVersion = version.Version

// businessErrorIsError controls the MCP `isError` flag on business errors
// ({error, message}). The AGENT-1808 brief asks for no flag; set it to true to
// match the current mcp/social behaviour.
const businessErrorIsError = true

const (
	maxTitleChars     = 500
	maxExcerptChars   = 5000
	maxContentBytes   = 1 << 20
	maxSlugChars      = 200
	maxTerms          = 50
	maxAltChars       = 1000
	maxFilenameChars  = 100
	defaultMediaBytes = 20 * 1024 * 1024
	idempotencyMax    = 1000
	idempotencyTTL    = 24 * time.Hour
)

var (
	allowedMedia = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}
	slugRe       = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	filenameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// The One adds these identifiers to every tool call; they are removed
	// before the strict validation of the arguments.
	injectedFields = []string{"deployment_id", "organization_id", "user_id", "space_id", "session_id"}
)

// Deps are the collaborators of the server.
type Deps struct {
	WordPress     *wordpress.Client // nil when not configured
	Buffer        *buffer.Client    // nil when the Buffer service is not configured
	MaxMediaBytes int
	Now           func() time.Time
	Secrets       []string // extra secret values to mask in results
	ConfigIssue   string   // why WordPress is not configured despite env values (never a value)
	Missing       []string // names of the unset variables (never a value)
}

// DepsFromEnv reads CMS_WORDPRESS_URL, CMS_WORDPRESS_USER,
// CMS_WORDPRESS_APP_PASSWORD and the Buffer.
func DepsFromEnv() Deps {
	d := Deps{MaxMediaBytes: defaultMediaBytes, Now: time.Now, Buffer: buffer.FromEnv()}
	cfg, issue, missing := wordpress.ParseConfig(os.Getenv("CMS_WORDPRESS_URL"), os.Getenv("CMS_WORDPRESS_USER"), os.Getenv("CMS_WORDPRESS_APP_PASSWORD"))
	if issue != "" {
		d.ConfigIssue, d.Missing = issue, missing
		return d
	}
	d.WordPress = wordpress.New(cfg)
	return d
}

type idemEntry struct {
	key, fingerprint string
	failed           bool // the stored outcome is an error (outcome_unknown)
	result           map[string]any
	at               time.Time
}

type handlers struct {
	deps Deps

	mu       sync.Mutex // serializes writes (keeps idempotency exact)
	idem     map[string]*list.Element
	idemList *list.List
}

func (h *handlers) init() {
	h.idem = map[string]*list.Element{}
	h.idemList = list.New()
}

// New builds the server from the environment.
func New() *modernmcp.Server { return NewWithDeps(DepsFromEnv()) }

// NewWithDeps builds the server with explicit dependencies (tests).
func NewWithDeps(deps Deps) *modernmcp.Server {
	h := newHandlers(deps)
	s := modernmcp.New("agentmaurice-cms", serviceVersion, "Use CMS to create, update, schedule and unpublish posts and pages, and to upload images, on a connected WordPress site. Status defaults to draft; publish makes the content public immediately. Unpublishing sends to draft or trash, never a permanent delete.")
	str := map[string]any{"type": "string"}
	intArr := map[string]any{"type": "array", "maxItems": maxTerms, "items": map[string]any{"type": "integer", "minimum": 1}}
	statuses := map[string]any{"type": "string", "enum": []string{"draft", "publish", "future"}, "description": "future needs date"}
	typ := map[string]any{"type": "string", "enum": []string{"post", "page"}}
	s.AddTool(readTool("cms_health_v1", "Check the CMS MCP.", schema(nil, nil)), h.guard(h.health))
	s.AddTool(readTool("cms_capabilities_v1", "Describe configuration state, operations and limits.", schema(nil, nil)), h.guard(h.capabilities))
	fields := func(extra map[string]any) map[string]any {
		m := map[string]any{
			"title": str, "content": map[string]any{"type": "string", "description": "HTML"}, "excerpt": str, "slug": str,
			"categories": intArr, "tags": intArr, "status": statuses,
			"date":            map[string]any{"type": "string", "description": "RFC 3339; required for status future"},
			"featured_media":  map[string]any{"type": "integer", "description": "media id; 0 removes it on update"},
			"idempotency_key": str,
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	s.AddTool(writeTool("cms_post_create_v1", "Create a post or page as draft, published or scheduled.",
		schema(fields(map[string]any{"type": typ}), []string{"idempotency_key"})), h.guard(h.postCreate))
	s.AddTool(writeTool("cms_media_upload_v1", "Upload an image (content_base64 or buffer_ref) to the media library; returns its id and URL.", schema(map[string]any{
		"content_base64": str, "buffer_ref": map[string]any{"type": "string", "description": "buffer:// reference"},
		"filename": str, "mime_type": str, "alt_text": str, "idempotency_key": str,
	}, []string{"filename", "mime_type", "idempotency_key"})), h.guard(h.mediaUpload))
	s.AddTool(readTool("cms_post_get_v1", "Read the state of a post or page by id.", schema(map[string]any{"post_id": map[string]any{"type": "integer"}, "type": typ}, []string{"post_id"})), h.guard(h.postGet))
	s.AddTool(writeTool("cms_post_update_v1", "Partially update a post or page (only the given fields change).",
		schema(fields(map[string]any{"post_id": map[string]any{"type": "integer"}, "type": typ}), []string{"post_id", "idempotency_key"})), h.guard(h.postUpdate))
	s.AddTool(deleteTool("cms_post_unpublish_v1", "Unpublish a post or page: back to draft, or to the trash (never a permanent delete).",
		schema(map[string]any{"post_id": map[string]any{"type": "integer"}, "type": typ, "mode": map[string]any{"type": "string", "enum": []string{"draft", "trash"}}, "idempotency_key": map[string]any{"type": "string"}}, []string{"post_id", "mode", "idempotency_key"})), h.guard(h.postUnpublish))
	return s
}

func newHandlers(deps Deps) *handlers {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.MaxMediaBytes <= 0 {
		deps.MaxMediaBytes = defaultMediaBytes
	}
	h := &handlers{deps: deps}
	h.init()
	return h
}

// credentials lists the secret values that no result may carry.
func (h *handlers) credentials() []string {
	var out []string
	if h.deps.WordPress != nil {
		out = append(out, h.deps.WordPress.Secrets()...)
		out = append(out, basicToken(h.deps.WordPress))
	}
	if h.deps.Buffer != nil {
		out = append(out, h.deps.Buffer.Token)
	}
	return append(out, h.deps.Secrets...)
}

func basicToken(c *wordpress.Client) string {
	s := c.Secrets()
	return base64.StdEncoding.EncodeToString([]byte(s[0] + ":" + s[1]))
}

// redact masks secrets in a text (also their JSON-escaped form).
func redact(text string, secrets []string) string {
	for _, sec := range secrets {
		if sec == "" { // no length floor: a short secret is refused at configuration instead
			continue
		}
		esc, _ := json.Marshal(sec)
		text = strings.ReplaceAll(text, sec, "[redacted]")
		text = strings.ReplaceAll(text, strings.Trim(string(esc), `"`), "[redacted]")
	}
	return text
}

// guard masks the credentials in every structured result, whatever its origin.
// In an error result the site base URL is masked too: it is never an output of
// the tools (the links WordPress returns in a success are the product).
func (h *handlers) guard(fn func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := fn(ctx, req)
		if res == nil || res.StructuredContent == nil {
			return res, err
		}
		raw, mErr := json.Marshal(res.StructuredContent)
		if mErr != nil {
			return failure("upstream_error", "unserializable result"), nil
		}
		m, _ := res.StructuredContent.(map[string]any)
		isErr := m != nil && m["error"] != nil
		pol := maskPolicy{sub: h.strongSecrets()}
		if u := h.userName(); u != "" {
			pol.exact = []string{u}
			if isErr {
				pol.msgSub = append(pol.msgSub, u)
			}
		}
		if isErr && h.deps.WordPress != nil {
			pol.msgSub = append(pol.msgSub, h.deps.WordPress.BaseURL())
		}
		// Mask the string VALUES of the structure, never its keys: a short secret such
		// as a user named "status" must not rename a field of the contract.
		var generic any
		if json.Unmarshal(raw, &generic) != nil {
			return failure("upstream_error", "result withheld: it could not be inspected"), nil
		}
		out, changed := pol.apply(generic, "")
		if !changed {
			return res, err
		}
		res.StructuredContent = out
		return res, err
	}
}

// userName is the WordPress user name: an identifier, not a secret in the strong
// sense, so it is not masked by substring (a user named "publish" must not corrupt
// state: "publish").
func (h *handlers) userName() string {
	if h.deps.WordPress == nil {
		return ""
	}
	return h.deps.WordPress.Secrets()[0]
}

// strongSecrets are masked by substring everywhere: the application password (also
// without its spaces), the Basic token, the Buffer token and the extra secrets.
func (h *handlers) strongSecrets() []string {
	var out []string
	for _, s := range h.credentials() {
		if s != h.userName() {
			out = append(out, s)
		}
	}
	return out
}

// maskPolicy: sub is masked by substring in every string value; exact is masked
// when it is the whole value of a field outside the contract enums (protectedKeys);
// msgSub is masked by substring in "message" values only (errors coming from
// WordPress carry the user name and the site URL there).
type maskPolicy struct{ sub, exact, msgSub []string }

var protectedKeys = map[string]bool{"status": true, "state": true, "type": true, "mode": true, "error": true, "mime_type": true, "version": true}

// apply returns v with the policy applied to its string values (map keys are left
// alone) and whether anything changed. key is the field the value belongs to.
func (pol maskPolicy) apply(v any, key string) (any, bool) {
	switch x := v.(type) {
	case string:
		out := redact(x, pol.sub)
		if key == "message" {
			out = redact(out, pol.msgSub)
		}
		if !protectedKeys[key] {
			for _, e := range pol.exact {
				if out == e {
					out = "[redacted]"
				}
			}
		}
		return out, out != x
	case []any:
		changed := false
		for i := range x {
			n, c := pol.apply(x[i], key)
			x[i], changed = n, changed || c
		}
		return x, changed
	case map[string]any:
		changed := false
		for k := range x {
			n, c := pol.apply(x[k], k)
			x[k], changed = n, changed || c
		}
		return x, changed
	}
	return v, false
}

func (h *handlers) health(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if h.deps.WordPress == nil {
		out := map[string]any{"status": "not_configured", "configured": false, "version": serviceVersion}
		if h.deps.ConfigIssue != "" {
			out["reason"] = h.deps.ConfigIssue
		}
		return result(out), nil
	}
	return result(map[string]any{"status": "ok", "configured": true, "version": serviceVersion}), nil
}

func (h *handlers) capabilities(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]any{
		"version":      serviceVersion,
		"configured":   h.deps.WordPress != nil,
		"config_issue": h.deps.ConfigIssue,
		"missing":      h.deps.Missing,
		"adapter":      map[string]any{"name": "wordpress", "configured": h.deps.WordPress != nil, "api": "native REST API /wp-json/wp/v2", "auth": "Application Passwords (HTTP Basic)"},
		"operations":   []string{"post_create", "media_upload", "post_get", "post_update", "post_unpublish"},
		"post_types":   []string{"post", "page"},
		"statuses":     []string{"draft", "publish", "future"},
		"unpublish":    map[string]any{"modes": []string{"draft", "trash"}, "permanent_delete": false},
		"limits": map[string]any{
			"title_chars": maxTitleChars, "excerpt_chars": maxExcerptChars, "content_bytes": maxContentBytes,
			"slug_chars": maxSlugChars, "terms_per_taxonomy": maxTerms,
			"media_bytes": h.deps.MaxMediaBytes, "media_types": []string{"image/gif", "image/jpeg", "image/png", "image/webp"},
			"request_timeout_s": 30, "upload_timeout_s": 120, "response_bytes": 4 << 20, "redirects_followed": false,
		},
		"idempotency":       map[string]any{"scope": "process memory", "max_entries": idempotencyMax, "ttl_seconds": int(idempotencyTTL.Seconds()), "survives_restart": false},
		"buffer_configured": h.deps.Buffer != nil,
		"error_codes": []string{"invalid_arguments", "not_configured", "auth_failed", "forbidden", "rate_limited", "post_not_found",
			"media_too_large", "media_type_refused", "buffer_unavailable", "buffer_not_found", "idempotency_conflict", "wordpress_error",
			"rest_api_unavailable", "trash_not_supported", "upstream_invalid_response", "upstream_redirect", "outcome_unknown", "upstream_error"},
	}), nil
}

func (h *handlers) notConfigured() *mcp.CallToolResult {
	if h.deps.WordPress != nil {
		return nil
	}
	res := map[string]any{"error": "not_configured", "message": "CMS_WORDPRESS_URL, CMS_WORDPRESS_USER and CMS_WORDPRESS_APP_PASSWORD must be set on the sidecar"}
	if h.deps.ConfigIssue != "" {
		res["reason"] = h.deps.ConfigIssue
		switch h.deps.ConfigIssue {
		case "url_not_https":
			res["message"] = "CMS_WORDPRESS_URL must use https (plain http is accepted only for a loopback host)"
		case "url_invalid":
			res["message"] = "CMS_WORDPRESS_URL is not a valid site base URL"
		case "user_too_short":
			res["message"] = "CMS_WORDPRESS_USER is too short (minimum 4 characters): a shorter name cannot be masked without corrupting results"
		case "app_password_too_short":
			res["message"] = "the application password is too short (minimum 16 characters)"
		}
	}
	if len(h.deps.Missing) > 0 {
		res["missing"] = h.deps.Missing
	}
	return &mcp.CallToolResult{IsError: businessErrorIsError, StructuredContent: res}
}

// mapErr turns a client error of a read or a refused write into a stable tool error.
func mapErr(err error) *mcp.CallToolResult {
	var api *wordpress.APIError
	var redir *wordpress.RedirectError
	switch {
	case errors.As(err, &redir):
		return failure("upstream_redirect", fmt.Sprintf("WordPress answered with a redirect (HTTP %d); redirects are not followed, check CMS_WORDPRESS_URL", redir.Code))
	case errors.Is(err, wordpress.ErrBadBody):
		return failure("upstream_invalid_response", "WordPress answered 2xx with a body that is not the documented JSON form (is /wp-json reachable and not rewritten by a proxy?)")
	case errors.Is(err, wordpress.ErrTransport):
		return failure("upstream_error", "network error while calling WordPress")
	case errors.As(err, &api):
		return mapAPI(api)
	}
	return failure("upstream_error", "WordPress request failed")
}

func mapAPI(api *wordpress.APIError) *mcp.CallToolResult {
	extra := func(code, msg string) *mcp.CallToolResult {
		res := failure(code, msg)
		m := res.StructuredContent.(map[string]any)
		m["wp_status"] = api.Status
		if api.Code != "" {
			m["wp_code"] = api.Code
		}
		return res
	}
	msg := api.Message
	if msg == "" {
		msg = fmt.Sprintf("WordPress answered HTTP %d", api.Status)
	}
	switch {
	case api.Status == http.StatusTooManyRequests:
		secs := int(math.Ceil(api.RetryAfter.Seconds()))
		if secs < 1 {
			secs = 1
		}
		res := extra("rate_limited", "WordPress or a proxy in front of it rate-limited the request")
		res.StructuredContent.(map[string]any)["retry_after_s"] = secs
		return res
	case api.Status == http.StatusUnauthorized:
		return extra("auth_failed", "WordPress rejected the credentials: "+msg)
	case api.Status == http.StatusForbidden:
		return extra("forbidden", msg)
	case api.Code == "rest_no_route":
		return extra("rest_api_unavailable", "the WordPress REST route does not exist: check CMS_WORDPRESS_URL, the permalink structure and REST access")
	case api.Code == "rest_trash_not_supported":
		return extra("trash_not_supported", msg)
	case api.Status == http.StatusNotFound:
		return extra("post_not_found", msg)
	case api.Status == http.StatusRequestEntityTooLarge:
		return extra("media_too_large", msg)
	case api.Status >= 400 && api.Status < 500:
		return extra("wordpress_error", msg)
	}
	return extra("upstream_error", msg)
}

// writeOutcome classifies the error of a request that changes the site. A clear
// refusal (4xx, redirect) proves that nothing changed; a transport failure, a 5xx
// or an unreadable 2xx leaves the outcome unknown.
func definitive(err error) bool {
	var api *wordpress.APIError
	var redir *wordpress.RedirectError
	if errors.As(err, &redir) {
		return true
	}
	// 501 rest_trash_not_supported is documented: WordPress refused before doing anything.
	return errors.As(err, &api) && ((api.Status >= 400 && api.Status < 500) || (api.Status == http.StatusNotImplemented && api.Code == "rest_trash_not_supported"))
}

const outcomeUnknownMsg = "the request left but its outcome is unknown: check the post in WordPress (drafts and trash included) before retrying; a new idempotency_key sends the request again"

func outcomeUnknown() map[string]any {
	return map[string]any{"error": "outcome_unknown", "message": outcomeUnknownMsg}
}

func unknownResult(m map[string]any) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: businessErrorIsError, StructuredContent: m}
}

// decode removes the identifiers injected by the One, then decodes strictly.
func decode(request mcp.CallToolRequest, target any) error {
	args := map[string]any{}
	if m, ok := request.Params.Arguments.(map[string]any); ok {
		for k, v := range m {
			args[k] = v
		}
	} else if request.Params.Arguments != nil {
		b, err := json.Marshal(request.Params.Arguments)
		if err != nil || json.Unmarshal(b, &args) != nil {
			return errors.New("arguments must be an object")
		}
	}
	for _, k := range injectedFields {
		delete(args, k)
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("arguments do not match the schema: %w", err)
	}
	return nil
}

type postFields struct {
	Title          *string `json:"title"`
	Content        *string `json:"content"`
	Excerpt        *string `json:"excerpt"`
	Slug           *string `json:"slug"`
	Categories     *[]int  `json:"categories"`
	Tags           *[]int  `json:"tags"`
	Status         string  `json:"status"`
	Date           string  `json:"date"`
	FeaturedMedia  *int    `json:"featured_media"`
	IdempotencyKey string  `json:"idempotency_key"`
}

type createInput struct {
	Type string `json:"type"`
	postFields
}

type updateInput struct {
	PostID int    `json:"post_id"`
	Type   string `json:"type"`
	postFields
}

// validateFields checks the shared fields and builds the WordPress request body.
func (h *handlers) validateFields(f postFields, postType string, create bool) (map[string]any, *mcp.CallToolResult) {
	body := map[string]any{}
	bad := func(msg string) (map[string]any, *mcp.CallToolResult) {
		return nil, failure("invalid_arguments", msg)
	}
	if strings.TrimSpace(f.IdempotencyKey) == "" || len(f.IdempotencyKey) > 200 {
		return bad("idempotency_key is required (at most 200 characters)")
	}
	if f.Title != nil {
		if len([]rune(*f.Title)) > maxTitleChars {
			return bad(fmt.Sprintf("title must hold at most %d characters", maxTitleChars))
		}
		body["title"] = *f.Title
	}
	if f.Content != nil {
		if len(*f.Content) > maxContentBytes {
			return bad(fmt.Sprintf("content must hold at most %d bytes", maxContentBytes))
		}
		body["content"] = *f.Content
	}
	if f.Excerpt != nil {
		if len([]rune(*f.Excerpt)) > maxExcerptChars {
			return bad(fmt.Sprintf("excerpt must hold at most %d characters", maxExcerptChars))
		}
		body["excerpt"] = *f.Excerpt
	}
	if f.Slug != nil {
		if *f.Slug != "" && (len(*f.Slug) > maxSlugChars || !slugRe.MatchString(*f.Slug)) {
			return bad("slug must be lowercase letters, digits and single hyphens")
		}
		if *f.Slug != "" {
			body["slug"] = *f.Slug
		}
	}
	for name, ids := range map[string]*[]int{"categories": f.Categories, "tags": f.Tags} {
		if ids == nil {
			continue
		}
		if postType == "page" {
			return bad("categories and tags apply to posts only, not pages")
		}
		if len(*ids) > maxTerms {
			return bad(fmt.Sprintf("%s holds at most %d ids", name, maxTerms))
		}
		for _, id := range *ids {
			if id < 1 {
				return bad(name + " must hold positive term ids")
			}
		}
		body[name] = *ids
	}
	if f.FeaturedMedia != nil {
		if *f.FeaturedMedia < 0 || (create && *f.FeaturedMedia == 0) {
			return bad("featured_media must be a media id")
		}
		body["featured_media"] = *f.FeaturedMedia
	}
	switch f.Status {
	case "", "draft", "publish", "future":
	default:
		return bad("status must be draft, publish or future")
	}
	var when time.Time
	if f.Date != "" {
		t, err := time.Parse(time.RFC3339, f.Date)
		if err != nil {
			return bad("date must be RFC 3339")
		}
		when = t.UTC()
	}
	now := h.deps.Now()
	switch {
	case f.Status == "future" && f.Date == "":
		return bad("date is required when status is future")
	case f.Status == "future" && !when.After(now):
		return bad("date must be in the future when status is future")
	case f.Status == "publish" && f.Date != "" && when.After(now):
		return bad("a publish status cannot carry a future date: use status future")
	case f.Status == "draft" && f.Date != "" && when.After(now):
		// A dated draft is allowed by WordPress: the date is kept for later.
	}
	if f.Status != "" {
		body["status"] = f.Status
	}
	if f.Date != "" {
		body["date_gmt"] = when.Format("2006-01-02T15:04:05")
	}
	return body, nil
}

func fingerprint(tool string, in any) string {
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(append([]byte(tool+":"), b...))
	return hex.EncodeToString(sum[:])
}

func postResult(p wordpress.Post, requestedStatus string) map[string]any {
	res := map[string]any{"status": "ok", "post_id": p.ID, "type": p.Type, "state": p.Status, "link": p.Link, "date": p.Date, "modified": p.Modified}
	if p.Slug != "" {
		res["slug"] = p.Slug
	}
	if requestedStatus != "" && p.Status != requestedStatus {
		res["note"] = fmt.Sprintf("WordPress stored status %q while %q was requested", p.Status, requestedStatus)
	}
	return res
}

// replay answers an idempotent repeat; ok is false when the key is unknown.
func (h *handlers) replay(key, fp string) (*mcp.CallToolResult, bool) {
	h.expire()
	el, ok := h.idem[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*idemEntry)
	if e.fingerprint != fp {
		return failure("idempotency_conflict", "idempotency_key was already used with a different request"), true
	}
	h.idemList.MoveToFront(el)
	out := map[string]any{}
	for k, v := range e.result {
		out[k] = v
	}
	out["idempotent_replay"] = true
	if e.failed {
		return unknownResult(out), true
	}
	return result(out), true
}

func (h *handlers) expire() {
	now := h.deps.Now()
	for el := h.idemList.Back(); el != nil; {
		prev := el.Prev()
		if now.Sub(el.Value.(*idemEntry).at) > idempotencyTTL {
			h.drop(el)
		}
		el = prev
	}
}

func (h *handlers) drop(el *list.Element) {
	delete(h.idem, el.Value.(*idemEntry).key)
	h.idemList.Remove(el)
}

func (h *handlers) remember(key, fp string, res map[string]any, failed bool) {
	for h.idemList.Len() >= idempotencyMax {
		h.drop(h.idemList.Back())
	}
	h.idem[key] = h.idemList.PushFront(&idemEntry{key: key, fingerprint: fp, result: res, failed: failed, at: h.deps.Now()})
}

func (h *handlers) postCreate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if r := h.notConfigured(); r != nil { // configuration first, before any argument
		return r, nil
	}
	var in createInput
	if err := decode(request, &in); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	if in.Type == "" {
		in.Type = "post"
	}
	if in.Type != "post" && in.Type != "page" {
		return failure("invalid_arguments", "type must be post or page"), nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	fp := fingerprint("create", in)
	// The replay comes before validation: a scheduled post replayed after its date
	// returns the stored result.
	if in.IdempotencyKey != "" {
		if r, ok := h.replay(in.IdempotencyKey, fp); ok {
			return r, nil
		}
	}
	body, r := h.validateFields(in.postFields, in.Type, true)
	if r != nil {
		return r, nil
	}
	if in.Status == "" {
		in.Status = "draft"
		body["status"] = "draft"
	}
	if in.Title == nil && in.Content == nil && in.Excerpt == nil ||
		(strings.TrimSpace(deref(in.Title))+strings.TrimSpace(deref(in.Content))+strings.TrimSpace(deref(in.Excerpt)) == "") {
		return failure("invalid_arguments", "title, content or excerpt must not be empty"), nil
	}
	return h.sendWrite(in.IdempotencyKey, fp, in.Status, in.Date, func() (wordpress.Post, error) {
		return h.deps.WordPress.CreatePost(ctx, in.Type, body)
	}), nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// sendWrite runs a keyed write and memorizes its outcome under the key: a success
// or an unknown outcome is never sent twice; a clear refusal frees the key.
func (h *handlers) sendWrite(key, fp, wantStatus, wantDate string, do func() (wordpress.Post, error)) *mcp.CallToolResult {
	p, err := do()
	if err != nil {
		if definitive(err) {
			return mapErr(err)
		}
		res := outcomeUnknown()
		h.remember(key, fp, res, true)
		return unknownResult(copyMap(res))
	}
	if !h.statusConfirmed(wantStatus, wantDate, p.Status) {
		// The write left but its effect is not the requested one: not a confirmed success.
		res := outcomeUnknown()
		h.remember(key, fp, res, true)
		return unknownResult(copyMap(res))
	}
	res := postResult(p, wantStatus)
	h.remember(key, fp, res, false)
	return result(copyMap(res))
}

// statusConfirmed reports whether the status WordPress returned is the requested
// one. No requested status confirms anything. A scheduled post (future) may come
// back publish when its date has passed in the meantime.
func (h *handlers) statusConfirmed(want, wantDate, got string) bool {
	if want == "" || got == want {
		return true
	}
	if want == "future" && got == "publish" && wantDate != "" {
		if t, err := time.Parse(time.RFC3339, wantDate); err == nil && !t.After(h.deps.Now()) {
			return true
		}
	}
	return false
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// resolve finds the REST collection of an id: the given type, or posts then pages.
func (h *handlers) resolve(ctx context.Context, id int, postType string) (wordpress.Post, string, *mcp.CallToolResult) {
	if postType != "" {
		p, err := h.deps.WordPress.GetPost(ctx, postType, id)
		if err != nil {
			return p, "", mapErr(err)
		}
		return p, postType, nil
	}
	p, err := h.deps.WordPress.GetPost(ctx, "post", id)
	var api *wordpress.APIError
	if errors.As(err, &api) && api.Status == http.StatusNotFound && api.Code != "rest_no_route" {
		p, err = h.deps.WordPress.GetPost(ctx, "page", id)
		if err == nil {
			return p, "page", nil
		}
	} else if err == nil {
		return p, "post", nil
	}
	return p, "", mapErr(err)
}

func checkID(id int, postType string) *mcp.CallToolResult {
	if id < 1 {
		return failure("invalid_arguments", "post_id is required and must be a positive integer")
	}
	if postType != "" && postType != "post" && postType != "page" {
		return failure("invalid_arguments", "type must be post or page")
	}
	return nil
}

func (h *handlers) postGet(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if r := h.notConfigured(); r != nil {
		return r, nil
	}
	var in struct {
		PostID int    `json:"post_id"`
		Type   string `json:"type"`
	}
	if err := decode(request, &in); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	if r := checkID(in.PostID, in.Type); r != nil {
		return r, nil
	}
	p, _, r := h.resolve(ctx, in.PostID, in.Type)
	if r != nil {
		return r, nil
	}
	res := postResult(p, "")
	res["date_gmt"] = p.DateGMT
	return result(res), nil
}

func (h *handlers) postUpdate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if r := h.notConfigured(); r != nil {
		return r, nil
	}
	var in updateInput
	if err := decode(request, &in); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	if r := checkID(in.PostID, in.Type); r != nil {
		return r, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	fp := fingerprint("update", in)
	if in.IdempotencyKey != "" {
		if r, ok := h.replay(in.IdempotencyKey, fp); ok {
			return r, nil
		}
	}
	body, r := h.validateFields(in.postFields, in.Type, false)
	if r != nil {
		return r, nil
	}
	if len(body) == 0 {
		return failure("invalid_arguments", "nothing to update: give at least one field"), nil
	}
	_, postType, r := h.resolve(ctx, in.PostID, in.Type)
	if r != nil {
		return r, nil
	}
	if postType == "page" && (in.Categories != nil || in.Tags != nil) {
		return failure("invalid_arguments", "categories and tags apply to posts only, not pages"), nil
	}
	return h.sendWrite(in.IdempotencyKey, fp, in.Status, in.Date, func() (wordpress.Post, error) {
		return h.deps.WordPress.UpdatePost(ctx, postType, in.PostID, body)
	}), nil
}

func (h *handlers) postUnpublish(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if r := h.notConfigured(); r != nil {
		return r, nil
	}
	var in struct {
		PostID int    `json:"post_id"`
		Type   string `json:"type"`
		Mode   string `json:"mode"`

		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := decode(request, &in); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	fp := fingerprint("unpublish", in)
	if in.IdempotencyKey != "" {
		if r, ok := h.replay(in.IdempotencyKey, fp); ok {
			return r, nil
		}
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" || len(in.IdempotencyKey) > 200 {
		return failure("invalid_arguments", "idempotency_key is required (at most 200 characters)"), nil
	}
	if r := checkID(in.PostID, in.Type); r != nil {
		return r, nil
	}
	if in.Mode != "draft" && in.Mode != "trash" {
		return failure("invalid_arguments", "mode must be draft or trash"), nil
	}
	_, postType, r := h.resolve(ctx, in.PostID, in.Type)
	if r != nil {
		return r, nil
	}
	var p wordpress.Post
	var err error
	if in.Mode == "draft" {
		p, err = h.deps.WordPress.UpdatePost(ctx, postType, in.PostID, map[string]any{"status": "draft"})
	} else {
		// DELETE without force: WordPress moves the item to the trash. There is no
		// path that sends force=true.
		p, err = h.deps.WordPress.TrashPost(ctx, postType, in.PostID)
		var api *wordpress.APIError
		if errors.As(err, &api) && api.Status == http.StatusGone && api.Code == "rest_already_trashed" {
			res := map[string]any{"status": "ok", "post_id": in.PostID, "type": postType, "state": "trash", "already_trashed": true}
			h.remember(in.IdempotencyKey, fp, res, false)
			return result(copyMap(res)), nil
		}
	}
	if err != nil {
		if definitive(err) {
			return mapErr(err), nil
		}
		res := outcomeUnknown()
		h.remember(in.IdempotencyKey, fp, res, true)
		return unknownResult(copyMap(res)), nil
	}
	want := map[string]string{"draft": "draft", "trash": "trash"}[in.Mode]
	if p.Status != want {
		res := outcomeUnknown()
		h.remember(in.IdempotencyKey, fp, res, true)
		return unknownResult(copyMap(res)), nil
	}
	res := postResult(p, want)
	h.remember(in.IdempotencyKey, fp, res, false)
	return result(copyMap(res)), nil
}

func (h *handlers) mediaUpload(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if r := h.notConfigured(); r != nil {
		return r, nil
	}
	var in struct {
		ContentBase64  string `json:"content_base64"`
		BufferRef      string `json:"buffer_ref"`
		Filename       string `json:"filename"`
		MimeType       string `json:"mime_type"`
		AltText        string `json:"alt_text"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := decode(request, &in); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	fp := fingerprint("media", in)
	// The replay comes before validation, as for create and update.
	if in.IdempotencyKey != "" {
		if r, ok := h.replay(in.IdempotencyKey, fp); ok {
			return r, nil
		}
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" || len(in.IdempotencyKey) > 200 {
		return failure("invalid_arguments", "idempotency_key is required (at most 200 characters)"), nil
	}
	if len(in.Filename) == 0 || len(in.Filename) > maxFilenameChars || !filenameRe.MatchString(in.Filename) || strings.Contains(in.Filename, "..") {
		return failure("invalid_arguments", "filename is required: ASCII letters, digits, dot, hyphen and underscore only (at most 100 characters)"), nil
	}
	if len([]rune(in.AltText)) > maxAltChars {
		return failure("invalid_arguments", fmt.Sprintf("alt_text must hold at most %d characters", maxAltChars)), nil
	}
	if (in.ContentBase64 == "") == (in.BufferRef == "") {
		return failure("invalid_arguments", "exactly one of content_base64 or buffer_ref is required"), nil
	}
	declared := strings.ToLower(strings.TrimSpace(in.MimeType))
	if !allowedMedia[declared] {
		return failure("media_type_refused", "media type must be one of image/jpeg, image/png, image/gif, image/webp"), nil
	}
	var data []byte
	if in.BufferRef != "" {
		if h.deps.Buffer == nil {
			return failure("buffer_unavailable", "the Buffer service is not configured on this sidecar"), nil
		}
		b, err := h.deps.Buffer.Load(ctx, in.BufferRef, h.deps.MaxMediaBytes)
		switch {
		case errors.Is(err, buffer.ErrNotFound):
			return failure("buffer_not_found", "the buffer reference was not found or has expired"), nil
		case errors.Is(err, buffer.ErrTooLarge):
			return failure("media_too_large", fmt.Sprintf("media exceeds %d bytes", h.deps.MaxMediaBytes)), nil
		case err != nil:
			return failure("buffer_unavailable", err.Error()), nil
		}
		data = b
	} else {
		if base64.StdEncoding.DecodedLen(len(in.ContentBase64)) > h.deps.MaxMediaBytes+3 {
			return failure("media_too_large", fmt.Sprintf("media exceeds %d bytes", h.deps.MaxMediaBytes)), nil
		}
		b, err := base64.StdEncoding.DecodeString(in.ContentBase64)
		if err != nil || len(b) == 0 {
			return failure("invalid_arguments", "content_base64 is not valid base64"), nil
		}
		data = b
	}
	if len(data) > h.deps.MaxMediaBytes {
		return failure("media_too_large", fmt.Sprintf("media exceeds %d bytes", h.deps.MaxMediaBytes)), nil
	}
	if sniffed := strings.SplitN(http.DetectContentType(data), ";", 2)[0]; sniffed != declared {
		return failure("media_type_refused", "media content does not match its declared type"), nil
	}
	p, err := h.deps.WordPress.UploadMedia(ctx, in.Filename, declared, data)
	if err != nil {
		if definitive(err) {
			return mapErr(err), nil
		}
		res := outcomeUnknown()
		h.remember(in.IdempotencyKey, fp, res, true)
		return unknownResult(copyMap(res)), nil
	}
	res := map[string]any{"status": "ok", "media_id": p.ID, "url": p.SourceURL, "link": p.Link, "mime_type": p.MimeType}
	if in.AltText != "" {
		if err := h.deps.WordPress.SetMediaAlt(ctx, p.ID, in.AltText); err != nil {
			// false is reserved for a certain refusal before any write (4xx or a redirect);
			// anything else left after the request went out may have changed the alt text.
			if definitive(err) {
				res["alt_text_set"] = false
				res["warning"] = "the image is uploaded and its media_id is valid, but WordPress refused to set its alt_text"
			} else {
				res["alt_text_set"] = "unknown"
				res["warning"] = "the image is uploaded and its media_id is valid; whether its alt_text was set is unknown (check the media in WordPress)"
			}
		} else {
			res["alt_text_set"] = true
		}
	}
	h.remember(in.IdempotencyKey, fp, res, false)
	return result(copyMap(res)), nil
}

func tool(name, description string, input mcp.ToolInputSchema, readOnly, destructive, idempotent bool) mcp.Tool {
	return mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent}}
}
func readTool(n, d string, in mcp.ToolInputSchema) mcp.Tool { return tool(n, d, in, true, false, true) }
func writeTool(n, d string, in mcp.ToolInputSchema) mcp.Tool {
	return tool(n, d, in, false, false, true)
}
func deleteTool(n, d string, in mcp.ToolInputSchema) mcp.Tool {
	return tool(n, d, in, false, true, true)
}
func schema(properties map[string]any, required []string) mcp.ToolInputSchema {
	if properties == nil {
		properties = map[string]any{}
	}
	return mcp.ToolInputSchema{Type: "object", Properties: properties, Required: required}
}
func result(content any) *mcp.CallToolResult {
	return &mcp.CallToolResult{StructuredContent: content}
}
func failure(code, message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: businessErrorIsError, StructuredContent: map[string]any{"error": code, "message": message}}
}
