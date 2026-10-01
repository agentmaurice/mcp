package systemone

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ptr(v float64) *float64    { return &v }
func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func request() Request {
	return Request{State: raw("PRIVATE_STATE_SENTINEL"), Questions: map[string]Question{
		"kind": {Type: "choice", Instructions: "Classify", Criteria: raw(map[string]string{"article": "News", "product": "A product"})},
	}}
}
func response() map[string]any {
	return map[string]any{"model": "jev-latest", "usage": map[string]any{"input_tokens": 12, "output_tokens": 8}, "answers": map[string]any{
		"kind": map[string]any{"type": "choice", "choice": "article", "probabilities": map[string]any{"article": 0.8, "product": 0.2}, "confidence": 0.7},
	}}
}
func setup(t *testing.T, handler http.HandlerFunc, timeout time.Duration) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, e := New(Config{APIKey: "PRIVATE_KEY_SENTINEL", URL: s.URL, Timeout: timeout})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func send(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

func TestOneRequestMultipleQuestionsAndThresholds(t *testing.T) {
	var calls atomic.Int32
	req := request()
	q := req.Questions["kind"]
	q.MinConfidence = ptr(0.8)
	req.Questions["kind"] = q
	req.Questions["quality"] = Question{Type: "score", Instructions: "Quality", Criteria: raw([]string{"Low", "High"}), MinConfidence: ptr(0.9)}
	req.Questions["urgent"] = Question{Type: "noul", Instructions: "Urgent?", TrueMeans: "Within one day", FalseMeans: "Later"}
	c := setup(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer PRIVATE_KEY_SENTINEL" {
			t.Error("wrong HTTP contract")
		}
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "min_confidence") || strings.Contains(string(b), "true_means") {
			t.Error("local policy leaked into provider request")
		}
		var sent providerRequest
		if e := json.Unmarshal(b, &sent); e != nil {
			t.Fatal(e)
		}
		if len(sent.Questions) != 3 || sent.Model != "jev-latest" {
			t.Error("batch or model changed")
		}
		criteria := sent.Questions["urgent"]["criteria"].(map[string]any)
		if criteria["true"] != "Within one day" || criteria["false"] != "Later" {
			t.Error("noul criteria mapping")
		}
		v := response()
		a := v["answers"].(map[string]any)
		a["quality"] = map[string]any{"type": "score", "score": 0.6, "probabilities": map[string]float64{"0": 0.4, "1": 0.6}, "legend": map[string]string{"0": "Low", "1": "High"}, "confidence": 0.2}
		a["urgent"] = map[string]any{"type": "noul", "noul": 0.99}
		send(w, v)
	}, time.Second)
	out, e := c.Ask(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 || len(out.Answers) != 3 || out.InputTokens != 12 || out.Provider != "typesafe" {
		t.Fatalf("wrong batch result: %+v", out)
	}
	a := out.Answers["kind"]
	if a.Choice != "uncertain" || !a.ViaFallback || a.ConfidenceKind != "calibrated" || *a.Confidence != 0.7 {
		t.Fatalf("wrong choice fallback: %+v", a)
	}
	a = out.Answers["quality"]
	if a.Score != nil || !a.ViaFallback {
		t.Fatal("low score was usable")
	}
	if !strings.Contains(string(raw(a)), `"score":null`) {
		t.Fatal("score null omitted")
	}
	if out.Answers["urgent"].Confidence != nil || *out.Answers["urgent"].Noul != 0.99 {
		t.Fatal("invented noul confidence")
	}
}

func TestConfidenceBoundaryAndAuthorCannotSetKind(t *testing.T) {
	for _, threshold := range []float64{0, 0.7, 1} {
		t.Run(fmt.Sprint(threshold), func(t *testing.T) {
			r := request()
			q := r.Questions["kind"]
			q.MinConfidence = ptr(threshold)
			r.Questions["kind"] = q
			c := setup(t, func(w http.ResponseWriter, _ *http.Request) {
				v := response()
				a := v["answers"].(map[string]any)["kind"].(map[string]any)
				a["confidence_kind"] = "self_reported"
				a["via_fallback"] = true
				send(w, v)
			}, time.Second)
			out, e := c.Ask(context.Background(), r)
			if e != nil {
				t.Fatal(e)
			}
			a := out.Answers["kind"]
			if a.ViaFallback != (threshold > 0.7) || a.ConfidenceKind != "calibrated" {
				t.Fatalf("wrong threshold or provider identity: %+v", a)
			}
		})
	}
}

func TestRetryBudgetAndNonRetryableErrors(t *testing.T) {
	for _, status := range []int{401, 422, 429, 529, 400, 500, 503, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			c := setup(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "PRIVATE_KEY_SENTINEL PRIVATE_STATE_SENTINEL")
			}, 2*time.Second)
			out, e := c.Ask(context.Background(), request())
			if e == nil || out != nil {
				t.Fatal("error hidden")
			}
			want := int32(1)
			if status == 429 || status == 529 {
				want = 3
			}
			if calls.Load() != want {
				t.Fatalf("attempts=%d want %d", calls.Load(), want)
			}
			if strings.Contains(e.Error(), "PRIVATE_") {
				t.Fatal("sensitive provider body leaked")
			}
		})
	}
}

func TestRetryThenSuccess(t *testing.T) {
	var calls atomic.Int32
	c := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(529)
			return
		}
		send(w, response())
	}, time.Second)
	if _, e := c.Ask(context.Background(), request()); e != nil || calls.Load() != 3 {
		t.Fatalf("%v calls=%d", e, calls.Load())
	}
}

func TestTimeoutAndCancellationStopRetries(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelFirst), func(t *testing.T) {
			var calls atomic.Int32
			c := setup(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "999999")
				w.WriteHeader(429)
			}, 35*time.Millisecond)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelFirst {
				cancel()
			}
			start := time.Now()
			_, e := c.Ask(ctx, request())
			if e == nil || time.Since(start) > time.Second || calls.Load() > 1 {
				t.Fatalf("unbounded retries: %v, %d", e, calls.Load())
			}
			if cancelFirst && calls.Load() != 0 {
				t.Fatal("cancelled request reached provider")
			}
		})
	}
}

func TestRedirectNeverForwardsCredential(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	c := setup(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }, time.Second)
	if _, e := c.Ask(context.Background(), request()); e == nil || forwarded.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestMalformedProviderResponses(t *testing.T) {
	cases := map[string]func(map[string]any){
		"missing usage":    func(v map[string]any) { delete(v, "usage") },
		"negative usage":   func(v map[string]any) { v["usage"] = map[string]any{"input_tokens": -1} },
		"fractional usage": func(v map[string]any) { v["usage"] = map[string]any{"input_tokens": 0.5} },
		"missing tokens":   func(v map[string]any) { v["usage"] = map[string]any{} },
		"missing model":    func(v map[string]any) { delete(v, "model") },
		"missing answer":   func(v map[string]any) { v["answers"] = map[string]any{} },
		"wrong id":         func(v map[string]any) { v["answers"] = map[string]any{"other": v["answers"].(map[string]any)["kind"]} },
		"extra answer":     func(v map[string]any) { v["answers"].(map[string]any)["other"] = map[string]any{} },
	}
	mutations := map[string]func(map[string]any){
		"wrong type":            func(a map[string]any) { a["type"] = "noul" },
		"unknown choice":        func(a map[string]any) { a["choice"] = "unknown" },
		"non-max choice":        func(a map[string]any) { a["choice"] = "product" },
		"missing confidence":    func(a map[string]any) { delete(a, "confidence") },
		"null confidence":       func(a map[string]any) { a["confidence"] = nil },
		"high confidence":       func(a map[string]any) { a["confidence"] = 1.1 },
		"negative confidence":   func(a map[string]any) { a["confidence"] = -0.1 },
		"wrong confidence type": func(a map[string]any) { a["confidence"] = "0.8" },
		"missing probabilities": func(a map[string]any) { delete(a, "probabilities") },
		"bad sum":               func(a map[string]any) { a["probabilities"] = map[string]float64{"article": 0.8, "product": 0.4} },
		"negative probability":  func(a map[string]any) { a["probabilities"] = map[string]float64{"article": 1.2, "product": -0.2} },
		"extra probability": func(a map[string]any) {
			a["probabilities"] = map[string]float64{"article": 0.8, "product": 0.2, "other": 0}
		},
		"wrong probability id": func(a map[string]any) { a["probabilities"] = map[string]float64{"article": 0.8, "other": 0.2} },
		"mixed fields":         func(a map[string]any) { a["noul"] = 0.8 },
	}
	for name, mutate := range mutations {
		cases[name] = func(v map[string]any) { mutate(v["answers"].(map[string]any)["kind"].(map[string]any)) }
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := response()
			mutate(v)
			c := setup(t, func(w http.ResponseWriter, _ *http.Request) { send(w, v) }, time.Second)
			if out, e := c.Ask(context.Background(), request()); e == nil || out != nil {
				t.Fatalf("accepted %s", name)
			}
		})
	}
	for _, b := range []string{`{`, `{} {}`, strings.Repeat("x", 2*1024*1024+1)} {
		t.Run(fmt.Sprint(len(b)), func(t *testing.T) {
			c := setup(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, b) }, time.Second)
			if _, e := c.Ask(context.Background(), request()); e == nil {
				t.Fatal("accepted invalid body")
			}
		})
	}
}

func TestValidationRejectsBeforeNetwork(t *testing.T) {
	cases := map[string]func(*Request){
		"null state":    func(r *Request) { r.State = raw(nil) },
		"numeric state": func(r *Request) { r.State = raw(42) },
		"boolean state": func(r *Request) { r.State = raw(true) },
		"bad JSON":      func(r *Request) { r.State = []byte(`{`) },
		"large state":   func(r *Request) { r.State = raw(strings.Repeat("x", MaxStateBytes)) },
		"no questions":  func(r *Request) { r.Questions = nil },
		"too many questions": func(r *Request) {
			for i := 0; i < 17; i++ {
				r.Questions[fmt.Sprintf("q%d", i)] = r.Questions["kind"]
			}
		},
		"invalid id": func(r *Request) { r.Questions["PRIVATE_STATE_SENTINEL"] = r.Questions["kind"] },
	}
	qs := map[string]Question{
		"empty instructions":    {Type: "noul"},
		"long instructions":     {Type: "noul", Instructions: strings.Repeat("é", 501)},
		"unknown type":          {Type: "text", Instructions: "Answer"},
		"one criterion":         {Type: "choice", Instructions: "Choose", Criteria: raw(map[string]string{"a": "A"})},
		"reserved criterion":    {Type: "choice", Instructions: "Choose", Criteria: raw(map[string]string{"a": "A", "uncertain": "U"})},
		"invalid criterion id":  {Type: "choice", Instructions: "Choose", Criteria: raw(map[string]string{"a": "A", "B": "B"})},
		"empty description":     {Type: "choice", Instructions: "Choose", Criteria: raw(map[string]string{"a": "A", "b": " "})},
		"score bad levels":      {Type: "score", Instructions: "Score", Criteria: raw([]string{"a", ""})},
		"score too many levels": {Type: "score", Instructions: "Score", Criteria: raw(make([]string, 17))},
		"noul threshold":        {Type: "noul", Instructions: "True?", MinConfidence: ptr(0.5)},
		"noul criteria":         {Type: "noul", Instructions: "True?", Criteria: raw(map[string]string{"true": "Yes"})},
		"high threshold":        {Type: "score", Instructions: "Score", Criteria: raw([]string{"a", "b"}), MinConfidence: ptr(2)},
		"low threshold":         {Type: "score", Instructions: "Score", Criteria: raw([]string{"a", "b"}), MinConfidence: ptr(-0.1)},
	}
	for name, q := range qs {
		cases[name] = func(r *Request) { r.Questions["kind"] = q }
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			c := setup(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); send(w, response()) }, time.Second)
			r := request()
			mutate(&r)
			_, e := c.Ask(context.Background(), r)
			if e == nil || calls.Load() != 0 {
				t.Fatal("invalid input reached provider")
			}
			if strings.Contains(e.Error(), "PRIVATE_") {
				t.Fatal("input leaked into error")
			}
		})
	}
}

func TestValidationBoundaries(t *testing.T) {
	r := request()
	for _, s := range []json.RawMessage{raw(""), raw(map[string]any{}), raw([]string{}), []byte(`{"large_integer":99999999999999999999999999999999999999}`), raw(strings.Repeat("x", MaxStateBytes-2))} {
		r.State = s
		if e := r.Validate(MaxStateBytes); e != nil {
			t.Fatal(e)
		}
	}
	r.State = raw("ok")
	criteria := map[string]string{}
	for i := 0; i < 255; i++ {
		criteria[fmt.Sprintf("k%d", i)] = "Description"
	}
	q := r.Questions["kind"]
	q.Criteria = raw(criteria)
	q.Instructions = strings.Repeat("é", 500)
	r.Questions["kind"] = q
	for i := 0; i < 15; i++ {
		r.Questions[fmt.Sprintf("q%d", i)] = q
	}
	if e := r.Validate(MaxStateBytes); e != nil {
		t.Fatal(e)
	}
	criteria["overflow"] = "Extra"
	q.Criteria = raw(criteria)
	r.Questions["kind"] = q
	if e := r.Validate(MaxStateBytes); e == nil {
		t.Fatal("256 criteria accepted")
	}
}

func TestConfiguration(t *testing.T) {
	for _, cfg := range []Config{{}, {APIKey: "x", URL: "http://example.com"}, {APIKey: "x", URL: "https://user:password@example.com"}, {APIKey: "x", URL: "https://example.com?key=secret"}, {APIKey: "x", Timeout: -1}, {APIKey: "x", MaxStateBytes: MaxStateBytes + 1}} {
		if _, e := New(cfg); e == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

func TestRoundedScoreResponseFromJev(t *testing.T) {
	req := Request{State: raw("Synthetic news article"), Questions: map[string]Question{"quality": {Type: "score", Instructions: "Writing quality", Criteria: raw([]string{"Low", "Medium", "High"})}}}
	for _, tc := range []struct {
		score float64
		valid bool
	}{{0.27, true}, {0.25, true}, {1.5, false}} {
		wire := map[string]any{"model": "jev-1.13.0", "usage": map[string]any{"input_tokens": 384, "output_tokens": 61}, "answers": map[string]any{"quality": map[string]any{"type": "score", "score": tc.score, "confidence": 0.56, "legend": map[string]string{"0": "Low", "1": "Medium", "2": "High"}, "probabilities": map[string]float64{"0": 0.76, "1": 0.22, "2": 0.02}}}}
		result, err := decodeResult(raw(wire), req, "typesafe")
		if (err == nil) != tc.valid {
			t.Fatalf("score=%v valid=%v err=%v", tc.score, tc.valid, err)
		}
		if tc.valid && *result.Answers["quality"].Score != tc.score {
			t.Fatal("provider score changed")
		}
	}
}

func TestHostedScopeAndRetryOwnership(t *testing.T) {
	for _, hosted := range []bool{false, true} {
		t.Run(fmt.Sprint(hosted), func(t *testing.T) {
			var ids []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ids = append(ids, r.Header.Get("X-Request-ID"))
				if hosted && r.Header.Get("X-AgentMaurice-Instance-ID") != "instance-a" {
					t.Error("instance scope missing")
				}
				w.WriteHeader(429)
			}))
			defer server.Close()
			cfg := Config{APIKey: "fixture", URL: server.URL}
			if hosted {
				cfg.Provider = "agentmaurice"
				cfg.InstanceID = "instance-a"
				cfg.Model = "hosted:jev-latest"
			}
			client, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Ask(context.Background(), request())
			if err == nil {
				t.Fatal("expected rate limit")
			}
			expected := 3
			if hosted {
				expected = 1
			}
			if len(ids) != expected {
				t.Fatalf("attempts=%d want=%d", len(ids), expected)
			}
			for _, id := range ids {
				if id == "" || id != ids[0] {
					t.Fatal("request identity changed during retry")
				}
			}
		})
	}
}

func TestEndpointByProvider(t *testing.T) {
	for _, tc := range []struct{ provider, base, want string }{
		{"typesafe", "https://api.typesafe.ai", "https://api.typesafe.ai/v1/systemone"},
		{"typesafe", "https://api.typesafe.ai/v1/systemone/", "https://api.typesafe.ai/v1/systemone"},
		{"typesafe", "https://openrouter.ai/api", "https://openrouter.ai/api/v1/systemone"},
		{"agentmaurice", "https://llm.agentmaurice.app", "https://llm.agentmaurice.app/v1/decisions"},
		{"agentmaurice", "https://llm.agentmaurice.app/v1/systemone", "https://llm.agentmaurice.app/v1/decisions"},
		{"agentmaurice", "https://llm.agentmaurice.app/v1/decisions/", "https://llm.agentmaurice.app/v1/decisions"},
	} {
		if got := endpointURL(tc.provider, tc.base); got != tc.want {
			t.Errorf("%s %s: got %s want %s", tc.provider, tc.base, got, tc.want)
		}
	}
}

// The hosted rail serves the AgentMaurice decision contract on /v1/decisions:
// the gateway names the resolved provider's confidence kind and the relay
// keeps it, whereas the vendor API on /v1/systemone is calibrated by
// construction and any kind it echoes is ignored.
func TestHostedRailUsesDecisionsRouteAndGatewayConfidenceKind(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  any
		valid bool
	}{{"calibrated", "calibrated", true}, {"self_reported", "self_reported", true}, {"none", "none", true}, {"missing", nil, false}, {"unknown", "guessed", false}} {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				var sent providerRequest
				b, _ := io.ReadAll(r.Body)
				if json.Unmarshal(b, &sent) != nil || sent.Model != "hosted:jev-latest" {
					t.Error("hosted model code not sent")
				}
				v := response()
				v["model"] = "jev-1.13"
				a := v["answers"].(map[string]any)["kind"].(map[string]any)
				if tc.kind != nil {
					a["confidence_kind"] = tc.kind
				}
				a["via_fallback"] = false
				send(w, v)
			}))
			defer server.Close()
			c, err := New(Config{APIKey: "fixture", URL: server.URL, Provider: "agentmaurice", Model: "hosted:jev-latest"})
			if err != nil {
				t.Fatal(err)
			}
			out, err := c.Ask(context.Background(), request())
			if path != "/v1/decisions" {
				t.Fatalf("hosted rail called %s", path)
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if !tc.valid {
				return
			}
			if out.Provider != "agentmaurice" || out.Model != "jev-1.13" || out.Answers["kind"].ConfidenceKind != tc.kind {
				t.Fatalf("gateway contract not kept: %+v", out)
			}
		})
	}
}
