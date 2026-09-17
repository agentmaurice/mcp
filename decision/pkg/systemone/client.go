package systemone

import (
	"bytes"
	"context"
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
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	if !strings.HasSuffix(cfg.URL, "/v1/systemone") {
		cfg.URL += "/v1/systemone"
	}
	return &Client{config: cfg, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Model() string   { return c.config.Model }
func (c *Client) StateLimit() int { return c.config.MaxStateBytes }

type providerRequest struct {
	State     json.RawMessage           `json:"state"`
	Model     string                    `json:"model"`
	Questions map[string]map[string]any `json:"questions"`
}

func (c *Client) Ask(ctx context.Context, req Request) (*Result, error) {
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
			return decodeResult(data, req, c.config.Model)
		}
		if (resp.StatusCode == 429 || resp.StatusCode == 529) && attempt < 2 {
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
			return nil, errors.New("System One authentication failed (401); check the configured credential")
		case 422:
			return nil, errors.New("System One rejected the contract (422)")
		default:
			return nil, fmt.Errorf("System One provider failed (HTTP %d)", resp.StatusCode)
		}
	}
	return nil, errors.New("System One attempts exhausted")
}

func decodeResult(data []byte, req Request, requestedModel string) (*Result, error) {
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
	result := &Result{Answers: map[string]Answer{}, Provider: "typesafe", Model: wire.Model, InputTokens: *wire.Usage.InputTokens}
	for id, q := range req.Questions {
		var a Answer
		if json.Unmarshal(wire.Answers[id], &a) != nil || a.Type != q.Type {
			return nil, bad
		}
		a.ConfidenceKind = "calibrated"
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
			sum, weighted := 0.0, 0.0
			for k := range expected {
				p, ok := a.Probabilities[k]
				if !ok || !unit(p) {
					return nil, bad
				}
				sum += p
				if q.Type == "choice" && p > a.Probabilities[a.Choice]+1e-6 {
					return nil, bad
				}
				if q.Type == "score" {
					i, _ := strconv.Atoi(k)
					weighted += float64(i) * p
				}
			}
			if math.Abs(sum-1) > 1e-4 || (q.Type == "score" && math.Abs(weighted-*a.Score) > 1e-4) {
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
