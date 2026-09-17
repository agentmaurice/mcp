package systemone

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxStateBytes = 32 * 1024

var identifier = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[-_][a-z0-9]+)*$`)

type Question struct {
	Type          string          `json:"type"`
	Instructions  string          `json:"instructions"`
	Criteria      json.RawMessage `json:"criteria,omitempty"`
	TrueMeans     string          `json:"true_means,omitempty"`
	FalseMeans    string          `json:"false_means,omitempty"`
	MinConfidence *float64        `json:"min_confidence,omitempty"`
}

type Request struct {
	State     json.RawMessage     `json:"state"`
	Questions map[string]Question `json:"questions"`
}

type Answer struct {
	Type           string             `json:"type"`
	Choice         string             `json:"choice,omitempty"`
	Score          *float64           `json:"score,omitempty"`
	Noul           *float64           `json:"noul,omitempty"`
	Probabilities  map[string]float64 `json:"probabilities,omitempty"`
	Legend         map[string]string  `json:"legend,omitempty"`
	Confidence     *float64           `json:"confidence,omitempty"`
	ConfidenceKind string             `json:"confidence_kind"`
	ViaFallback    bool               `json:"via_fallback"`
}

// A rejected score is explicitly null, rather than a numeric value a caller
// could accidentally use. Other answer types do not carry a score field.
func (a Answer) MarshalJSON() ([]byte, error) {
	type plain Answer
	if a.Type == "score" && a.Score == nil {
		return json.Marshal(struct {
			plain
			Score *float64 `json:"score"`
		}{plain: plain(a)})
	}
	return json.Marshal(plain(a))
}

type Result struct {
	Answers     map[string]Answer `json:"answers"`
	Provider    string            `json:"provider"`
	Model       string            `json:"model"`
	InputTokens int64             `json:"input_tokens"`
}

func unit(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1 }

func (r Request) Validate(maxBytes int) error {
	state := bytes.TrimSpace(r.State)
	if !json.Valid(state) || len(state) == 0 {
		return errors.New("state must be a JSON string, object or array")
	}
	switch state[0] {
	case '"', '{', '[':
	default:
		return errors.New("state must be a JSON string, object or array")
	}
	var compact bytes.Buffer
	_ = json.Compact(&compact, state)
	if compact.Len() > maxBytes {
		return errors.New("state exceeds the configured byte limit")
	}
	if len(r.Questions) < 1 || len(r.Questions) > 16 {
		return errors.New("questions must contain 1 to 16 entries")
	}
	for id, q := range r.Questions {
		if !identifier.MatchString(id) {
			return errors.New("question id has an invalid format")
		}
		if err := q.Validate(); err != nil {
			// Do not echo the id, instructions, criteria or state into errors.
			return fmt.Errorf("invalid question: %w", err)
		}
	}
	return nil
}

func (q Question) Validate() error {
	if strings.TrimSpace(q.Instructions) == "" || utf8.RuneCountInString(q.Instructions) > 500 {
		return errors.New("instructions must contain 1 to 500 characters")
	}
	if q.MinConfidence != nil && !unit(*q.MinConfidence) {
		return errors.New("min_confidence must be between 0 and 1")
	}
	if q.Type != "noul" && (q.TrueMeans != "" || q.FalseMeans != "") {
		return errors.New("true_means and false_means require noul")
	}
	switch q.Type {
	case "choice":
		var criteria map[string]string
		if json.Unmarshal(q.Criteria, &criteria) != nil || len(criteria) < 2 || len(criteria) > 255 {
			return errors.New("choice criteria must contain 2 to 255 descriptions")
		}
		for id, description := range criteria {
			if !identifier.MatchString(id) || id == "uncertain" || strings.TrimSpace(description) == "" {
				return errors.New("choice criteria require valid ids and descriptions; uncertain is reserved")
			}
		}
	case "score":
		var criteria []string
		if json.Unmarshal(q.Criteria, &criteria) != nil || len(criteria) < 2 || len(criteria) > 16 {
			return errors.New("score criteria must contain 2 to 16 ordered levels")
		}
		for _, description := range criteria {
			if strings.TrimSpace(description) == "" {
				return errors.New("score levels must have nonempty descriptions")
			}
		}
	case "noul":
		if len(q.Criteria) != 0 || q.MinConfidence != nil {
			return errors.New("noul does not accept criteria or min_confidence; use true_means and false_means")
		}
	default:
		return errors.New("question type must be choice, score or noul")
	}
	return nil
}
