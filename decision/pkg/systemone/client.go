package systemone

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Provider   string
	InstanceID string

	APIKey        string
	URL           string
	Model         string
	Timeout       time.Duration
	MaxStateBytes int
}

type Client struct {
	config Config
	http   *http.Client
}

// New bounds the whole operation, including all retry delays. Redirects are
// disabled so the Bearer credential cannot be forwarded to another endpoint.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("TypeSafe API key is required")
	}
	if cfg.Provider == "" {
		cfg.Provider = "typesafe"
	}
	if cfg.Provider != "typesafe" && cfg.Provider != "agentmaurice" {
		return nil, errors.New("unsupported System One provider")
	}
	if cfg.URL == "" {
		cfg.URL = "https://api.typesafe.ai"
	}
	if cfg.Model == "" {
		cfg.Model = "jev-latest"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.MaxStateBytes == 0 {
		cfg.MaxStateBytes = MaxStateBytes
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid TypeSafe URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return nil, errors.New("TypeSafe URL requires HTTPS, except loopback HTTP for testing")
	}
	if cfg.Timeout <= 0 || cfg.MaxStateBytes < 1 || cfg.MaxStateBytes > MaxStateBytes {
		return nil, errors.New("timeout must be positive and state limit must be between 1 and 32768 bytes")
	}
	cfg.URL = endpointURL(cfg.Provider, cfg.URL)
	return &Client{config: cfg, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Vendor and OpenRouter BYOK speak the TypeSafe API on /v1/systemone. The
// AgentMaurice gateway exposes the decision contract on /v1/decisions, named
// by category so another provider can sit behind the same route.
const (
	typeSafePath = "/v1/systemone"
	hostedPath   = "/v1/decisions"
)

func endpointURL(provider, base string) string {
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(strings.TrimSuffix(base, typeSafePath), hostedPath)
	if provider == "agentmaurice" {
		return base + hostedPath
	}
	return base + typeSafePath
}

func (c *Client) Provider() string { return c.config.Provider }

func (c *Client) Model() string   { return c.config.Model }
func (c *Client) StateLimit() int { return c.config.MaxStateBytes }

type providerRequest struct {
	State     json.RawMessage           `json:"state"`
	Model     string                    `json:"model"`
	Questions map[string]map[string]any `json:"questions"`
}

func (c *Client) Ask(ctx context.Context, req Request) (*Result, error) {
	requestID := make([]byte, 16)
	if _, err := rand.Read(requestID); err != nil {
		return nil, errors.New("cannot create System One request id")
	}
	if err := req.Validate(c.config.MaxStateBytes); err != nil {
		return nil, err
	}
	wire := providerRequest{State: req.State, Model: c.config.Model, Questions: map[string]map[string]any{}}
	for id, q := range req.Questions {
		v := map[string]any{"type": q.Type, "instructions": q.Instructions}
		if q.Type == "noul" {
			criteria := map[string]string{}
			if q.TrueMeans != "" {
				criteria["true"] = q.TrueMeans
			}
			if q.FalseMeans != "" {
				criteria["false"] = q.FalseMeans
			}
			if len(criteria) > 0 {
				v["criteria"] = criteria
			}
		} else {
			v["criteria"] = q.Criteria
		}
		wire.Questions[id] = v
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, errors.New("cannot encode System One request")
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return nil, errors.New("System One request cancelled or timed out")
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.URL, bytes.NewReader(body))
		if err != nil {
			return nil, errors.New("cannot create System One request")
		}
		httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("X-Request-ID", hex.EncodeToString(requestID))
		if c.config.InstanceID != "" {
			httpReq.Header.Set("X-AgentMaurice-Instance-ID", c.config.InstanceID)
		}
		resp, err := c.http.Do(httpReq)
		if err != nil {
			// net/http errors may embed URLs. Never return provider bodies or
			// transport error details to tools or logs.
			if ctx.Err() != nil {
				return nil, errors.New("System One request cancelled or timed out")
			}
			return nil, errors.New("System One transport failed")
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
		resp.Body.Close()
		if readErr != nil || len(data) > 2*1024*1024 {
			return nil, errors.New("invalid or oversized System One response")
		}
		if resp.StatusCode == http.StatusOK {
			return decodeResult(data, req, c.config.Provider)
		}
		if (resp.StatusCode == 429 || resp.StatusCode == 529) && attempt < 2 && c.config.Provider != "agentmaurice" {
			delay := time.Duration(100*(1<<attempt)) * time.Millisecond
			if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
				if seconds > 2 {
					seconds = 2
				}
				delay = time.Duration(seconds) * time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, errors.New("System One request cancelled or timed out")
			case <-timer.C:
			}
			continue
		}
		switch resp.StatusCode {
		case 401:
			return nil, &HTTPStatusError{Status: 401, Message: "System One authentication failed (401); check the configured credential"}
		case 422:
			return nil, &HTTPStatusError{Status: 422, Message: "System One rejected the contract (422)"}
		default:
			return nil, &HTTPStatusError{Status: resp.StatusCode, Message: fmt.Sprintf("System One provider failed (HTTP %d)", resp.StatusCode)}
		}
	}
	return nil, errors.New("System One attempts exhausted")
}

// decodeResult validates a provider or gateway response against the request.
// The vendor API carries no confidence_kind: TypeSafe answers are calibrated
// by construction. The AgentMaurice gateway rail serves the decision contract,
// so confidence_kind comes from the gateway and names the resolved provider's
// kind; a missing or unknown kind is a contract violation.
func decodeResult(data []byte, req Request, provider string) (*Result, error) {
	var wire struct {
		Model   string                     `json:"model"`
		Answers map[string]json.RawMessage `json:"answers"`
		Usage   *struct {
			InputTokens  *int64 `json:"input_tokens"`
			OutputTokens *int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	bad := errors.New("invalid System One response contract")
	if json.Unmarshal(data, &wire) != nil || wire.Model == "" || len(wire.Answers) != len(req.Questions) || wire.Usage == nil || wire.Usage.InputTokens == nil || *wire.Usage.InputTokens < 0 || (wire.Usage.OutputTokens != nil && *wire.Usage.OutputTokens < 0) {
		return nil, bad
	}
	result := &Result{Answers: map[string]Answer{}, Provider: provider, Model: wire.Model, InputTokens: *wire.Usage.InputTokens}
	for id, q := range req.Questions {
		var a Answer
		if json.Unmarshal(wire.Answers[id], &a) != nil || a.Type != q.Type {
			return nil, bad
		}
		if provider == "agentmaurice" {
			if !validConfidenceKind(a.ConfidenceKind) {
				return nil, bad
			}
		} else {
			a.ConfidenceKind = "calibrated"
		}
		a.ViaFallback = false
		if q.Type == "noul" {
			if a.Noul == nil || !unit(*a.Noul) || a.Choice != "" || a.Score != nil || a.Confidence != nil || len(a.Probabilities) != 0 || len(a.Legend) != 0 {
				return nil, bad
			}
		} else {
			if a.Confidence == nil || !unit(*a.Confidence) || a.Noul != nil {
				return nil, bad
			}
			expected := map[string]string{}
			if q.Type == "choice" {
				_ = json.Unmarshal(q.Criteria, &expected)
				if _, ok := expected[a.Choice]; !ok || a.Score != nil || len(a.Legend) != 0 {
					return nil, bad
				}
			} else {
				var levels []string
				_ = json.Unmarshal(q.Criteria, &levels)
				for i, v := range levels {
					expected[strconv.Itoa(i)] = v
				}
				if a.Score == nil || math.IsNaN(*a.Score) || *a.Score < 0 || *a.Score > float64(len(levels)-1) || a.Choice != "" || len(a.Legend) != len(expected) {
					return nil, bad
				}
				for k, v := range expected {
					if a.Legend[k] != v {
						return nil, bad
					}
				}
			}
			if len(a.Probabilities) != len(expected) {
				return nil, bad
			}
			lowerSum, upperSum := 0.0, 0.0
			for k := range expected {
				p, ok := a.Probabilities[k]
				if !ok || !unit(p) {
					return nil, bad
				}
				lower, upper := probabilityRoundingBounds(p)
				lowerSum += lower
				upperSum += upper
				if q.Type == "choice" && p > a.Probabilities[a.Choice]+1e-6 {
					return nil, bad
				}
			}
			if lowerSum > 1+1e-6 || upperSum < 1-1e-6 || (q.Type == "score" && !roundedScoreConsistent(a.Probabilities, *a.Score)) {
				return nil, bad
			}
			if q.MinConfidence != nil && *a.Confidence < *q.MinConfidence {
				a.ViaFallback = true
				if q.Type == "choice" {
					a.Choice = "uncertain"
				} else {
					a.Score = nil
				}
			}
		}
		result.Answers[id] = a
	}
	return result, nil
}

func validConfidenceKind(kind string) bool {
	return kind == "calibrated" || kind == "self_reported" || kind == "none"
}

// Jev publishes probabilities and scores rounded to two decimal places. Check
// whether a normalized distribution inside those rounding intervals can yield
// the reported score; do not reject valid responses or alter provider values.
func probabilityRoundingBounds(p float64) (float64, float64) {
	tolerance := 1e-6
	if math.Abs(p*100-math.Round(p*100)) < 1e-6 {
		tolerance = 0.005
	}
	return math.Max(0, p-tolerance), math.Min(1, p+tolerance)
}

func roundedScoreConsistent(probabilities map[string]float64, score float64) bool {
	bound := func(reverse bool) float64 {
		remaining, weighted := 1.0, 0.0
		for i := 0; i < len(probabilities); i++ {
			low, _ := probabilityRoundingBounds(probabilities[strconv.Itoa(i)])
			remaining -= low
			weighted += float64(i) * low
		}
		for step := 0; step < len(probabilities); step++ {
			i := step
			if reverse {
				i = len(probabilities) - 1 - step
			}
			low, high := probabilityRoundingBounds(probabilities[strconv.Itoa(i)])
			amount := math.Min(math.Max(0, remaining), high-low)
			weighted += float64(i) * amount
			remaining -= amount
		}
		return weighted
	}
	tolerance := 1e-6
	if math.Abs(score*100-math.Round(score*100)) < 1e-6 {
		tolerance = 0.005
	}
	return score+tolerance+1e-6 >= bound(false) && score-tolerance-1e-6 <= bound(true)
}

// HTTPStatusError exposes only the status and a safe message, never the body.
type HTTPStatusError struct {
	Status  int
	Message string
}

func (e *HTTPStatusError) Error() string { return e.Message }
