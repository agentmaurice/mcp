package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/agentmaurice/mcpchatui/mcp/decision/pkg/systemone"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Evaluator interface {
	Ask(context.Context, systemone.Request) (*systemone.Result, error)
	Model() string
	StateLimit() int
}

func decode(data []byte, out any) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return errors.New("tool arguments must be an object")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid tool arguments")
	}
	return nil
}

func result(v any, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
	}
	b, e := json.Marshal(v)
	if e != nil {
		return nil, errors.New("cannot encode decision result")
	}
	return &mcp.CallToolResult{StructuredContent: v, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
}

func New(client Evaluator, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "decision", Version: version}, &mcp.ServerOptions{Instructions: "Classify, score or evaluate a yes/no question over short, already extracted content. This server returns typed values and never executes a branch or write. Use min_confidence for consequential choices; uncertain means do not act automatically. Confidence calibration must be checked on your own labelled data."})
	add := func(name, description string, schema map[string]any, fn func(context.Context, json.RawMessage) (any, error)) {
		no := false
		s.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &no}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			v, e := fn(ctx, r.Params.Arguments)
			return result(v, e)
		})
	}
	empty := object(map[string]any{}, nil)
	add("decision_health_v1", "Check local server readiness without contacting TypeSafe or spending tokens.", empty, func(_ context.Context, b json.RawMessage) (any, error) {
		var v struct{}
		if e := decode(b, &v); e != nil {
			return nil, e
		}
		return map[string]any{"status": "ready", "provider": "typesafe", "model": client.Model()}, nil
	})
	add("decision_capabilities_v1", "Discover question types, bounds and confidence semantics before constructing a decision request.", empty, func(_ context.Context, b json.RawMessage) (any, error) {
		var v struct{}
		if e := decode(b, &v); e != nil {
			return nil, e
		}
		return map[string]any{"types": []string{"choice", "score", "noul"}, "max_questions": 16, "max_state_bytes": client.StateLimit(), "choice_criteria": []int{2, 255}, "score_levels": []int{2, 16}, "confidence_kind": "calibrated", "model": client.Model(), "low_confidence_choice": "uncertain", "low_confidence_score": nil, "max_attempts": 3}, nil
	})
	add("decision_ask_v1", "Evaluate 1–16 independent typed questions over the same state in one provider call. Use for batched classification, ordinal scoring or yes/no checks. No text generation, extraction or branch execution.", askSchema(), func(ctx context.Context, b json.RawMessage) (any, error) {
		var req systemone.Request
		if e := decode(b, &req); e != nil {
			return nil, e
		}
		return client.Ask(ctx, req)
	})
	for _, kind := range []string{"choice", "score", "noul"} {
		descriptions := map[string]string{
			"choice": "Choose one of 2–255 named criteria. Below min_confidence, returns uncertain and via_fallback=true. Use to select a category before a separate, governed action.",
			"score":  "Score on 2–16 ordered levels; fractional values are probability-weighted level indices. Below min_confidence, returns score=null and via_fallback=true.",
			"noul":   "Return the probability of yes, between 0 and 1. Optional true_means/false_means explain each outcome. This probability is not a separate confidence score.",
		}
		add("decision_"+kind+"_v1", descriptions[kind], singleSchema(kind), func(ctx context.Context, b json.RawMessage) (any, error) {
			var args struct {
				State         json.RawMessage `json:"state"`
				Instructions  string          `json:"instructions"`
				Criteria      json.RawMessage `json:"criteria,omitempty"`
				MinConfidence *float64        `json:"min_confidence,omitempty"`
				TrueMeans     string          `json:"true_means,omitempty"`
				FalseMeans    string          `json:"false_means,omitempty"`
			}
			if e := decode(b, &args); e != nil {
				return nil, e
			}
			req := systemone.Request{State: args.State, Questions: map[string]systemone.Question{"answer": {Type: kind, Instructions: args.Instructions, Criteria: args.Criteria, MinConfidence: args.MinConfidence, TrueMeans: args.TrueMeans, FalseMeans: args.FalseMeans}}}
			out, e := client.Ask(ctx, req)
			if e != nil {
				return nil, e
			}
			return struct {
				systemone.Answer
			}{Answer: out.Answers["answer"]}, nil
		})
	}
	return s
}

func Handler(s *mcp.Server) http.Handler {
	mux := http.NewServeMux()
	get := func(*http.Request) *mcp.Server { return s }
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(get, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	mux.Handle("/sse", mcp.NewSSEHandler(get, nil))
	for _, path := range []string{"/health", "/ready"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024)
		mux.ServeHTTP(w, r)
	})
}

func object(properties map[string]any, required []string) map[string]any {
	s := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}
func stateSchema() map[string]any {
	return map[string]any{"type": []string{"string", "object", "array"}, "description": "Already extracted content, at most 32768 bytes as compact JSON. No images or file references."}
}
func questionSchema(kind string) map[string]any {
	p := map[string]any{"type": map[string]any{"const": kind}, "instructions": map[string]any{"type": "string", "minLength": 1, "maxLength": 500}}
	required := []string{"type", "instructions"}
	if kind == "noul" {
		p["true_means"] = map[string]any{"type": "string"}
		p["false_means"] = map[string]any{"type": "string"}
	} else {
		p["min_confidence"] = map[string]any{"type": "number", "minimum": 0, "maximum": 1}
		required = append(required, "criteria")
		if kind == "choice" {
			p["criteria"] = map[string]any{"type": "object", "minProperties": 2, "maxProperties": 255, "propertyNames": map[string]any{"pattern": `^[a-z][a-z0-9]*(?:[-_][a-z0-9]+)*$`, "not": map[string]any{"const": "uncertain"}}, "additionalProperties": map[string]any{"type": "string", "minLength": 1}}
		} else {
			p["criteria"] = map[string]any{"type": "array", "minItems": 2, "maxItems": 16, "items": map[string]any{"type": "string", "minLength": 1}}
		}
	}
	return object(p, required)
}
func askSchema() map[string]any {
	return object(map[string]any{"state": stateSchema(), "questions": map[string]any{"type": "object", "minProperties": 1, "maxProperties": 16, "propertyNames": map[string]any{"pattern": `^[a-z][a-z0-9]*(?:[-_][a-z0-9]+)*$`}, "additionalProperties": map[string]any{"oneOf": []any{questionSchema("choice"), questionSchema("score"), questionSchema("noul")}}}}, []string{"state", "questions"})
}
func singleSchema(kind string) map[string]any {
	s := questionSchema(kind)
	p := s["properties"].(map[string]any)
	delete(p, "type")
	p["state"] = stateSchema()
	r := []string{"state", "instructions"}
	if kind != "noul" {
		r = append(r, "criteria")
	}
	s["required"] = r
	return s
}
