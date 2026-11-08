// Package mcpserver exposes the Social MCP tools, a thin client of the public
// REST API of a Postiz instance (Postiz Cloud or the customer's own).
package mcpserver

import (
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
	"strings"
	"sync"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/agentmaurice/mcpchatui/mcp/social/internal/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/social/internal/postiz"
	"github.com/agentmaurice/mcpchatui/mcp/social/internal/version"
	"github.com/mark3labs/mcp-go/mcp"
)

var serviceVersion = version.Version

const (
	maxContentChars    = 5000
	maxChannels        = 10
	maxMedia           = 4
	defaultMediaBytes  = 20 * 1024 * 1024
	defaultRatePerHour = 30
	idempotencyMax     = 1000
	idempotencyTTL     = 24 * time.Hour
	lookupBack         = 90 * 24 * time.Hour
	lookupForward      = 365 * 24 * time.Hour
	minScheduleLead    = 120 * time.Second
	minKeyLen          = 16 // shorter keys cannot be masked reliably: refused
	unknownForward     = 5 * 365 * 24 * time.Hour
)

var allowedMedia = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}

// Deps are the collaborators of the server.
type Deps struct {
	Postiz        *postiz.Client // nil when not configured
	Buffer        *buffer.Client // nil when the Buffer service is not configured
	RatePerHour   int
	MaxMediaBytes int
	Now           func() time.Time
	Secrets       []string // extra secret values to mask in results
	ConfigIssue   string   // why Postiz is not configured despite env values (never a value)
}

// DepsFromEnv reads SOCIAL_POSTIZ_URL, SOCIAL_POSTIZ_API_KEY,
// and the Buffer. The rate (30 requests per hour) and the media size (20 MiB) are
// fixed; RatePerHour and MaxMediaBytes are fields only so that tests can lower them.
func DepsFromEnv() Deps {
	d := Deps{RatePerHour: defaultRatePerHour, MaxMediaBytes: defaultMediaBytes, Now: time.Now, Buffer: buffer.FromEnv()}
	u, k := strings.TrimSpace(os.Getenv("SOCIAL_POSTIZ_URL")), strings.TrimSpace(os.Getenv("SOCIAL_POSTIZ_API_KEY"))
	if u != "" && k != "" && len(k) < minKeyLen {
		d.ConfigIssue = "api_key_too_short"
	} else if u != "" && k != "" {
		d.Postiz = postiz.New(u, k, postiz.NewLimiter(d.RatePerHour))
	}
	return d
}

type idemEntry struct {
	key, fingerprint string
	failed           bool // stored outcome is an error (outcome_unknown, partial_failure)
	result           map[string]any
}

type handlers struct {
	deps Deps

	mu       sync.Mutex // serializes post creation (bounded rate; keeps idempotency exact)
	idem     map[string]*list.Element
	idemList *list.List
	idemAt   map[string]time.Time
	groups   map[string][]string  // post_id -> all post ids of the same create call (in memory)
	when     map[string]time.Time // post_id -> planned date known from this process
	chanOf   map[string]string    // post_id -> channel id known from this process
	missing  map[string][]string  // post_id -> requested channels that got no post at creation
}

func (h *handlers) init() {
	h.idem = map[string]*list.Element{}
	h.idemList = list.New()
	h.idemAt = map[string]time.Time{}
	h.groups = map[string][]string{}
	h.when = map[string]time.Time{}
	h.chanOf = map[string]string{}
	h.missing = map[string][]string{}
}

// New builds the server from the environment.
func New() *modernmcp.Server { return NewWithDeps(DepsFromEnv()) }

// NewWithDeps builds the server with explicit dependencies (tests).
func NewWithDeps(deps Deps) *modernmcp.Server {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.MaxMediaBytes <= 0 {
		deps.MaxMediaBytes = defaultMediaBytes
	}
	if deps.RatePerHour <= 0 {
		deps.RatePerHour = defaultRatePerHour
	}
	h := &handlers{deps: deps}
	h.init()
	s := modernmcp.New("agentmaurice-social", serviceVersion, "Use Social to publish or schedule posts on social channels through a connected Postiz account. Posts default to drafts only when mode is draft; mode now publishes immediately.")
	str := map[string]any{"type": "string"}
	s.AddTool(readTool("social_health_v1", "Check the Social MCP.", schema(nil, nil)), h.guard(h.health))
	s.AddTool(readTool("social_capabilities_v1", "Describe configuration state, operations, limits and rate.", schema(nil, nil)), h.guard(h.capabilities))
	s.AddTool(readTool("social_channels_v1", "List the channels connected to the Postiz account.", schema(nil, nil)), h.guard(h.channels))
	media := map[string]any{"type": "object", "properties": map[string]any{
		"content_base64": str, "ref": map[string]any{"type": "string", "description": "buffer:// reference"}, "filename": str, "mime_type": str,
	}, "required": []string{"filename", "mime_type"}}
	s.AddTool(writeTool("social_post_create_v1", "Create a draft, schedule or immediate post on 1 to 10 channels.", schema(map[string]any{
		"channels":        map[string]any{"type": "array", "minItems": 1, "maxItems": maxChannels, "items": str},
		"content":         map[string]any{"type": "string", "maxLength": maxContentChars},
		"media":           map[string]any{"type": "array", "maxItems": maxMedia, "items": media},
		"mode":            map[string]any{"type": "string", "enum": []string{"draft", "schedule", "now"}},
		"schedule_at":     map[string]any{"type": "string", "description": "RFC 3339, in the future; required for schedule"},
		"idempotency_key": str,
	}, []string{"channels", "content", "mode", "idempotency_key"})), h.guard(h.postCreate))
	s.AddTool(readTool("social_post_get_v1", "Read the state of a post.", schema(map[string]any{"post_id": str}, []string{"post_id"})), h.guard(h.postGet))
	s.AddTool(deleteTool("social_post_delete_v1", "Remove a post from the schedule or unpublish it (every post of the same group).", schema(map[string]any{"post_id": str}, []string{"post_id"})), h.guard(h.postDelete))
	return s
}

// secrets lists every configured secret value; none may reach a result.
func (h *handlers) secrets() []string {
	var out []string
	if h.deps.Postiz != nil {
		out = append(out, h.deps.Postiz.Secret())
	}
	if h.deps.Buffer != nil {
		out = append(out, h.deps.Buffer.Token)
	}
	return append(out, h.deps.Secrets...)
}

// redact masks configured secrets in a text (also their JSON-escaped form).
func redact(text string, secrets []string) string {
	for _, sec := range secrets {
		if len(sec) < 4 { // configured keys are >= 16; other secrets: best effort
			continue
		}
		esc, _ := json.Marshal(sec)
		text = strings.ReplaceAll(text, sec, "[redacted]")
		text = strings.ReplaceAll(text, strings.Trim(string(esc), `"`), "[redacted]")
	}
	return text
}

// guard masks configured secrets in every structured result, whatever its origin.
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
		clean := redact(string(raw), h.secrets())
		if clean == string(raw) {
			return res, err
		}
		var out any
		if json.Unmarshal([]byte(clean), &out) != nil {
			return failure("upstream_error", "result withheld: it contained a secret"), nil
		}
		res.StructuredContent = out
		return res, err
	}
}

func (h *handlers) health(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if h.deps.Postiz == nil {
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
		"configured":   h.deps.Postiz != nil,
		"config_issue": h.deps.ConfigIssue,
		"backend":      map[string]any{"name": "postiz", "kind": "connected service (Postiz Cloud or customer instance)", "api": "public REST API /public/v1"},
		"operations":   []string{"channels", "post_create", "post_get", "post_delete"},
		"modes":        []string{"draft", "schedule", "now"},
		"rate":         map[string]any{"client_limit_per_hour": h.deps.RatePerHour, "window_seconds": 3600, "known": true},
		"limits": map[string]any{
			"channels_per_post": maxChannels, "content_chars": maxContentChars, "media_per_post": maxMedia,
			"media_bytes": h.deps.MaxMediaBytes, "media_types": []string{"image/gif", "image/jpeg", "image/png", "image/webp"},
		},
		"idempotency":       map[string]any{"scope": "process memory", "max_entries": idempotencyMax, "ttl_seconds": int(idempotencyTTL.Seconds()), "survives_restart": false},
		"buffer_configured": h.deps.Buffer != nil,
		"error_codes": []string{"invalid_arguments", "not_configured", "auth_failed", "rate_limited", "unknown_channel", "channel_disabled",
			"schedule_at_invalid", "media_too_large", "media_type_refused", "buffer_unavailable", "buffer_not_found", "idempotency_conflict", "post_not_found", "upstream_invalid_response", "upstream_redirect", "not_found_in_window", "outcome_unknown", "partial_failure", "upstream_error"},
	}), nil
}

func (h *handlers) notConfigured() *mcp.CallToolResult {
	if h.deps.Postiz == nil {
		if h.deps.ConfigIssue != "" {
			return &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{"error": "not_configured", "reason": h.deps.ConfigIssue, "message": "the Postiz API key is too short (minimum 16 characters)"}}
		}
		return failure("not_configured", "SOCIAL_POSTIZ_URL and SOCIAL_POSTIZ_API_KEY must be set on the sidecar")
	}
	return nil
}

// mapErr turns a client error into a stable tool error; it never exposes the key.
func mapErr(err error) *mcp.CallToolResult {
	var rl *postiz.RateLimitedError
	var redir *postiz.RedirectError
	switch {
	case errors.As(err, &rl):
		secs := int(math.Ceil(rl.RetryAfter.Seconds()))
		if secs < 1 {
			secs = 1
		}
		msg := "Postiz rate limit reached"
		if rl.Local {
			msg = "client-side rate limit reached"
		}
		return &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{"error": "rate_limited", "message": msg, "retry_after_s": secs}}
	case errors.As(err, &redir):
		return failure("upstream_redirect", fmt.Sprintf("Postiz answered with a redirect (HTTP %d); redirects are not followed", redir.Code))
	case errors.Is(err, postiz.ErrBadBody):
		return failure("upstream_invalid_response", "Postiz answered 200 with a body that is not the documented form")
	case errors.Is(err, postiz.ErrUnauthorized):
		return failure("auth_failed", "Postiz rejected the API key")
	case errors.Is(err, postiz.ErrNotFound):
		return failure("post_not_found", "Postiz does not know this resource")
	default:
		return failure("upstream_error", err.Error())
	}
}

func (h *handlers) channels(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if r := h.notConfigured(); r != nil {
		return r, nil
	}
	list, err := h.deps.Postiz.Integrations(ctx)
	if err != nil {
		return mapErr(err), nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, it := range list {
		c := map[string]any{"id": it.ID, "provider": it.Identifier, "name": it.Name, "disabled": it.Disabled}
		if it.Picture != "" {
			c["picture"] = it.Picture
		}
		out = append(out, c)
	}
	return result(map[string]any{"channels": out}), nil
}

type mediaInput struct {
	ContentBase64 string `json:"content_base64"`
	Ref           string `json:"ref"`
	Filename      string `json:"filename"`
	MimeType      string `json:"mime_type"`
}

type createInput struct {
	Channels       []string     `json:"channels"`
	Content        string       `json:"content"`
	Media          []mediaInput `json:"media"`
	Mode           string       `json:"mode"`
	ScheduleAt     string       `json:"schedule_at"`
	IdempotencyKey string       `json:"idempotency_key"`
}

func (h *handlers) postCreate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if r := h.notConfigured(); r != nil { // configuration first, before any argument
		return r, nil
	}
	var in createInput
	if err := decode(request, &in); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	return h.create(ctx, in), nil
}

func (h *handlers) validate(in createInput) (*mcp.CallToolResult, time.Time) {
	var zero time.Time
	if strings.TrimSpace(in.IdempotencyKey) == "" || len(in.IdempotencyKey) > 200 {
		return failure("invalid_arguments", "idempotency_key is required (at most 200 characters)"), zero
	}
	if n := len(in.Channels); n < 1 || n > maxChannels {
		return failure("invalid_arguments", fmt.Sprintf("channels must hold 1 to %d ids", maxChannels)), zero
	}
	seen := map[string]bool{}
	for _, c := range in.Channels {
		if strings.TrimSpace(c) == "" || seen[c] {
			return failure("invalid_arguments", "channels must be distinct, non-empty ids"), zero
		}
		seen[c] = true
	}
	if strings.TrimSpace(in.Content) == "" || len([]rune(in.Content)) > maxContentChars {
		return failure("invalid_arguments", fmt.Sprintf("content must hold 1 to %d characters", maxContentChars)), zero
	}
	if len(in.Media) > maxMedia {
		return failure("invalid_arguments", fmt.Sprintf("at most %d media", maxMedia)), zero
	}
	when := h.deps.Now().UTC()
	switch in.Mode {
	case "now", "draft":
		if in.ScheduleAt != "" && in.Mode == "draft" {
			t, err := time.Parse(time.RFC3339, in.ScheduleAt)
			if err != nil {
				return failure("schedule_at_invalid", "schedule_at must be RFC 3339"), zero
			}
			when = t.UTC()
		}
	case "schedule":
		if in.ScheduleAt == "" {
			return failure("schedule_at_invalid", "schedule_at is required when mode is schedule"), zero
		}
		t, err := time.Parse(time.RFC3339, in.ScheduleAt)
		if err != nil {
			return failure("schedule_at_invalid", "schedule_at must be RFC 3339"), zero
		}
		if !t.After(h.deps.Now().Add(minScheduleLead)) {
			return failure("schedule_at_invalid", "schedule_at must be at least 120 seconds in the future"), zero
		}
		when = t.UTC()
	default:
		return failure("invalid_arguments", "mode must be draft, schedule or now"), zero
	}
	return nil, when
}

func fingerprint(in createInput) string {
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (h *handlers) create(ctx context.Context, in createInput) *mcp.CallToolResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	fp := fingerprint(in)
	h.expire()
	if el, ok := h.idem[in.IdempotencyKey]; ok {
		e := el.Value.(*idemEntry)
		if e.fingerprint != fp {
			return failure("idempotency_conflict", "idempotency_key was already used with a different request")
		}
		h.idemList.MoveToFront(el)
		out := map[string]any{}
		for k, v := range e.result {
			out[k] = v
		}
		out["idempotent_replay"] = true
		if e.failed {
			if out["error"] == "outcome_unknown" {
				out["message"] = outcomeUnknownMsg
			}
			return &mcp.CallToolResult{IsError: true, StructuredContent: out}
		}
		return result(out)
	}

	// Validation comes after the idempotent replay: a replay of a schedule whose date
	// has since passed must return the stored result.
	r, when := h.validate(in)
	if r != nil {
		return r
	}
	integrations, err := h.deps.Postiz.Integrations(ctx)
	if err != nil {
		return mapErr(err)
	}
	byID := map[string]postiz.Integration{}
	for _, it := range integrations {
		byID[it.ID] = it
	}
	for _, c := range in.Channels {
		it, ok := byID[c]
		if !ok {
			return failure("unknown_channel", "channel "+c+" is not connected to this Postiz account")
		}
		if it.Disabled {
			return failure("channel_disabled", "channel "+c+" is disabled")
		}
	}

	// Validate and load every media before any upload or post: nothing partial.
	type loaded struct {
		in   mediaInput
		data []byte
	}
	files := make([]loaded, 0, len(in.Media))
	for _, m := range in.Media {
		data, r := h.loadMedia(ctx, m)
		if r != nil {
			return r
		}
		files = append(files, loaded{m, data})
	}
	var images []postiz.Media
	for _, f := range files {
		up, err := h.deps.Postiz.Upload(ctx, f.in.Filename, f.in.MimeType, f.data)
		if err != nil {
			return mapErr(err)
		}
		images = append(images, up)
	}

	// Recheck right before the POST: uploads and lookups take time.
	if in.Mode == "schedule" && !when.After(h.deps.Now().Add(minScheduleLead)) {
		return failure("schedule_at_invalid", "schedule_at is less than 120 seconds ahead at send time")
	}
	// The group is derived from the idempotency key: a retry targets the same group.
	// Whether Postiz honours a client-chosen group is a hypothesis; the list API
	// exposes no group field, so it cannot be used to resolve an unknown outcome.
	group := groupFromKey(in.IdempotencyKey)
	req := postiz.CreateRequest{Type: in.Mode, Date: when.Format("2006-01-02T15:04:05.000Z"), Tags: []struct{}{}}
	for _, c := range in.Channels {
		var item postiz.PostItem
		item.Integration.ID = c
		item.Group = group
		item.Value = []postiz.PostValue{{Content: in.Content, Image: append([]postiz.Media{}, images...)}}
		item.Settings = settingsFor(byID[c].Identifier)
		req.Posts = append(req.Posts, item)
	}
	created, err := h.deps.Postiz.CreatePosts(ctx, req)
	decision := decideCreate(created, err, in.Channels)
	if decision.kind == createRelease {
		return mapErr(err) // explicit no-effect refusal: the key stays free
	}
	if decision.kind == createUnknown {
		res := map[string]any{"error": "outcome_unknown", "message": outcomeUnknownMsg, "group": group}
		h.remember(in.IdempotencyKey, fp, res, true)
		return &mcp.CallToolResult{IsError: true, StructuredContent: res}
	}
	// Postiz publishes through a queue: "now" is accepted, not yet confirmed published.
	state := map[string]string{"draft": "draft", "schedule": "scheduled", "now": "scheduled"}[in.Mode]
	got := decision.proven
	chans := make([]map[string]any, 0, len(in.Channels))
	ids := make([]string, 0, len(in.Channels))
	missing := 0
	for _, ch := range in.Channels {
		if id, ok := got[ch]; ok {
			chans = append(chans, map[string]any{"id": ch, "post_id": id, "state": state})
			ids = append(ids, id)
			h.chanOf[id] = ch
		} else {
			chans = append(chans, map[string]any{"id": ch, "state": "unknown"})
			missing++
		}
	}
	if missing > 0 {
		res := map[string]any{"error": "partial_failure", "message": fmt.Sprintf("%d of %d channels returned no post id; check the Postiz schedule before retrying", missing, len(in.Channels)), "group": group, "channels": chans}
		if len(ids) > 0 {
			res["post_id"] = ids[0]
		}
		h.remember(in.IdempotencyKey, fp, res, true)
		var lost []string
		for _, ch := range in.Channels {
			if _, ok := got[ch]; !ok {
				lost = append(lost, ch)
			}
		}
		for _, id := range ids {
			h.groups[id] = ids
			h.when[id] = when
			h.missing[id] = lost
		}
		return &mcp.CallToolResult{IsError: true, StructuredContent: res}
	}
	res := map[string]any{"status": "ok", "post_id": ids[0], "group": group, "state": state, "channels": chans}
	if in.Mode == "now" {
		res["note"] = "accepted for immediate publication; read social_post_get_v1 for the final state"
	}
	h.remember(in.IdempotencyKey, fp, res, false)
	for _, id := range ids {
		h.groups[id] = ids
		h.when[id] = when
	}
	return result(res)
}

func (h *handlers) loadMedia(ctx context.Context, m mediaInput) ([]byte, *mcp.CallToolResult) {
	if strings.TrimSpace(m.Filename) == "" || strings.ContainsAny(m.Filename, "/\\\x00") {
		return nil, failure("invalid_arguments", "media filename is required and must not contain path separators")
	}
	if (m.ContentBase64 == "") == (m.Ref == "") {
		return nil, failure("invalid_arguments", "media needs exactly one of content_base64 or ref")
	}
	declared := strings.ToLower(strings.TrimSpace(m.MimeType))
	if !allowedMedia[declared] {
		return nil, failure("media_type_refused", "media type must be one of image/jpeg, image/png, image/gif, image/webp")
	}
	var data []byte
	if m.Ref != "" {
		if h.deps.Buffer == nil {
			return nil, failure("buffer_unavailable", "the Buffer service is not configured on this sidecar")
		}
		b, err := h.deps.Buffer.Load(ctx, m.Ref, h.deps.MaxMediaBytes)
		switch {
		case errors.Is(err, buffer.ErrNotFound):
			return nil, failure("buffer_not_found", "the buffer reference was not found or has expired")
		case errors.Is(err, buffer.ErrTooLarge):
			return nil, failure("media_too_large", fmt.Sprintf("media exceeds %d bytes", h.deps.MaxMediaBytes))
		case err != nil:
			return nil, failure("buffer_unavailable", err.Error())
		}
		data = b
	} else {
		if base64.StdEncoding.DecodedLen(len(m.ContentBase64)) > h.deps.MaxMediaBytes+3 {
			return nil, failure("media_too_large", fmt.Sprintf("media exceeds %d bytes", h.deps.MaxMediaBytes))
		}
		b, err := base64.StdEncoding.DecodeString(m.ContentBase64)
		if err != nil || len(b) == 0 {
			return nil, failure("invalid_arguments", "content_base64 is not valid base64")
		}
		data = b
	}
	if len(data) > h.deps.MaxMediaBytes {
		return nil, failure("media_too_large", fmt.Sprintf("media exceeds %d bytes", h.deps.MaxMediaBytes))
	}
	if sniffed := strings.SplitN(http.DetectContentType(data), ";", 2)[0]; sniffed != declared {
		return nil, failure("media_type_refused", "media content does not match its declared type")
	}
	return data, nil
}

// settingsFor builds the provider settings block. `__type` is required by
// Postiz; X also requires who_can_reply_post (docs). Other providers may need
// more fields (hypothesis: Postiz then answers with an error, mapped to upstream_error).
func settingsFor(identifier string) map[string]any {
	s := map[string]any{"__type": identifier}
	if identifier == "x" {
		s["who_can_reply_post"] = "everyone"
	}
	return s
}

type createKind int

const (
	createOK      createKind = iota // every requested channel has a postId
	createPartial                   // some channels are proven, others are not
	createUnknown                   // nothing is proven: a post may exist
	createRelease                   // explicit no-effect refusal: the key can be reused
)

type createDecision struct {
	kind   createKind
	proven map[string]string // channel id -> postId, non-empty only
}

// decideCreate is the single rule for the outcome of POST /posts. Once the request
// has left, only an answer that proves the creation (a postId per channel) or an
// explicit, documented no-effect refusal (4xx, 401/403, 429, local rate limit) is
// conclusive. Transport errors, 2xx with an unreadable or empty body, 5xx and any
// other status leave the outcome unknown.
func decideCreate(created []postiz.Created, err error, requested []string) createDecision {
	if err != nil {
		var rl *postiz.RateLimitedError
		var se *postiz.StatusError
		switch {
		case errors.As(err, &rl), errors.Is(err, postiz.ErrUnauthorized), errors.Is(err, postiz.ErrNotFound):
			return createDecision{kind: createRelease}
		case errors.As(err, &se) && se.Code >= 400 && se.Code < 500:
			return createDecision{kind: createRelease}
		}
		return createDecision{kind: createUnknown}
	}
	want := map[string]bool{}
	for _, c := range requested {
		want[c] = true
	}
	proven := map[string]string{}
	usedIDs := map[string]bool{}
	for _, c := range created {
		// Proof needs a known channel, a non-empty postId and distinct postIds:
		// one id shared by two channels proves only one creation.
		if c.PostID == "" || !want[c.Integration] || usedIDs[c.PostID] {
			continue
		}
		if _, dup := proven[c.Integration]; dup {
			continue
		}
		proven[c.Integration] = c.PostID
		usedIDs[c.PostID] = true
	}
	switch {
	case len(proven) == 0:
		return createDecision{kind: createUnknown}
	case len(proven) < len(requested):
		return createDecision{kind: createPartial, proven: proven}
	}
	return createDecision{kind: createOK, proven: proven}
}

const outcomeUnknownMsg = "vérifiez le planning Postiz ; envoyez avec une nouvelle idempotency_key pour republier"

func groupFromKey(key string) string {
	sum := sha256.Sum256([]byte("social-group:" + key))
	return hex.EncodeToString(sum[:16])
}

func (h *handlers) expire() {
	now := h.deps.Now()
	for el := h.idemList.Back(); el != nil; {
		e := el.Value.(*idemEntry)
		prev := el.Prev()
		if now.Sub(h.idemAt[e.key]) > idempotencyTTL {
			h.drop(el)
		}
		el = prev
	}
}

func (h *handlers) drop(el *list.Element) {
	e := el.Value.(*idemEntry)
	delete(h.idem, e.key)
	delete(h.idemAt, e.key)
	h.idemList.Remove(el)
}

func (h *handlers) remember(key, fp string, res map[string]any, failed bool) {
	for h.idemList.Len() >= idempotencyMax {
		h.drop(h.idemList.Back())
	}
	h.idem[key] = h.idemList.PushFront(&idemEntry{key: key, fingerprint: fp, result: res, failed: failed})
	h.idemAt[key] = h.deps.Now()
	if len(h.groups) > idempotencyMax*maxChannels {
		h.groups = map[string][]string{}
		h.when = map[string]time.Time{}
		h.chanOf = map[string]string{}
		h.missing = map[string][]string{}
	}
}

func mapState(s string) string {
	switch s {
	case "PUBLISHED":
		return "published"
	case "QUEUE":
		return "scheduled"
	case "DRAFT":
		return "draft"
	case "ERROR":
		return "error"
	}
	return strings.ToLower(s)
}

func (h *handlers) postGet(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if r := h.notConfigured(); r != nil { // configuration first, before any argument
		return r, nil
	}
	var in struct {
		PostID string `json:"post_id"`
	}
	if err := decode(request, &in); err != nil || strings.TrimSpace(in.PostID) == "" {
		return failure("invalid_arguments", "post_id is required"), nil
	}
	h.mu.Lock()
	ids := h.groups[in.PostID]
	known, hasWhen := h.when[in.PostID]
	lost := h.missing[in.PostID]
	_, knownPost := h.chanOf[in.PostID] // confirmed by this process: absence from the list proves nothing
	h.mu.Unlock()
	if len(ids) == 0 {
		ids = []string{in.PostID}
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	now := h.deps.Now()
	start, end := now.Add(-lookupBack), now.Add(lookupForward)
	if hasWhen { // known planned date: the window always contains it
		if w := known.Add(-24 * time.Hour); w.Before(start) {
			start = w
		}
		if w := known.Add(24 * time.Hour); w.After(end) {
			end = w
		}
	} else { // unknown id (other process, restart): widest bounded window
		end = now.Add(unknownForward)
	}
	posts, err := h.deps.Postiz.ListPosts(ctx, start, end)
	if err != nil {
		return mapErr(err), nil
	}
	chans := []map[string]any{}
	states := map[string]bool{}
	for _, p := range posts {
		if !want[p.ID] {
			continue
		}
		st := mapState(p.State)
		states[st] = true
		c := map[string]any{"id": p.Integration.ID, "state": st}
		if p.ReleaseURL != "" {
			c["url"] = p.ReleaseURL
		}
		if p.Error != nil && *p.Error != "" {
			c["error"] = redact(*p.Error, h.secrets())
		}
		chans = append(chans, c)
	}
	// Channels whose creation was not confirmed: "unknown", never "missing" (nothing
	// proves they were not created). Postiz's list has no group field, so a post on
	// that channel with the planned date of this group is reported as inferred.
	if len(chans) > 0 || knownPost {
		for _, ch := range lost {
			entry := map[string]any{"id": ch, "state": "unknown"}
			if hasWhen {
				for _, p := range posts {
					if want[p.ID] || p.Integration.ID != ch {
						continue
					}
					if d, err := time.Parse(time.RFC3339, p.PublishDate); err != nil || !d.Truncate(time.Second).Equal(known.Truncate(time.Second)) {
						continue
					}
					entry = map[string]any{"id": ch, "post_id": p.ID, "state": mapState(p.State), "inferred": true}
					if p.ReleaseURL != "" {
						entry["url"] = p.ReleaseURL
					}
					if p.Error != nil && *p.Error != "" {
						entry["error"] = redact(*p.Error, h.secrets())
					}
					break
				}
			}
			chans = append(chans, entry)
			if entry["inferred"] == true {
				states["unknown"] = true // a deduction is informative, never proof
			} else {
				states[fmt.Sprint(entry["state"])] = true
			}
		}
	}
	// Known siblings absent from the list: that channel is unknown, not absent.
	if len(chans) > 0 || knownPost {
		seen := map[string]bool{}
		for _, p := range posts {
			seen[p.ID] = true
		}
		h.mu.Lock()
		for _, id := range ids {
			if !seen[id] && h.chanOf[id] != "" {
				chans = append(chans, map[string]any{"id": h.chanOf[id], "state": "unknown"})
				states["unknown"] = true
			}
		}
		h.mu.Unlock()
	}
	if len(chans) == 0 {
		if !hasWhen {
			// Postiz has no single-post read: only a bounded list window can be searched.
			return &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{
				"error": "not_found_in_window", "message": "the post was not found in the searched window; Postiz has no single-post read, so a later post may exist",
				"window_start": start.UTC().Format(time.RFC3339), "window_end": end.UTC().Format(time.RFC3339)}}, nil
		}
		return failure("post_not_found", "no post with this id in the lookup window"), nil
	}
	state := aggregateState(states)
	return result(map[string]any{"post_id": in.PostID, "state": state, "channels": chans}), nil
}

// aggregateState: a single common state is that state; otherwise any error,
// missing or unknown channel gives "partial"; otherwise "mixed" (the per-channel
// list is authoritative).
func aggregateState(states map[string]bool) string {
	if len(states) == 1 {
		for k := range states {
			return k
		}
	}
	if states["error"] || states["missing"] || states["unknown"] {
		return "partial"
	}
	return "mixed"
}

func (h *handlers) postDelete(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if r := h.notConfigured(); r != nil { // configuration first, before any argument
		return r, nil
	}
	var in struct {
		PostID string `json:"post_id"`
	}
	if err := decode(request, &in); err != nil || strings.TrimSpace(in.PostID) == "" {
		return failure("invalid_arguments", "post_id is required"), nil
	}
	if err := h.deps.Postiz.DeletePost(ctx, in.PostID); err != nil {
		return mapErr(err), nil
	}
	return result(map[string]any{"status": "ok", "deleted": true}), nil
}

func decode(request mcp.CallToolRequest, target any) error {
	payload, err := json.Marshal(request.Params.Arguments)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("arguments do not match schema: %w", err)
	}
	return nil
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
	return &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{"error": code, "message": message}}
}
