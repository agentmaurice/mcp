package inspect

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"go.uber.org/zap"
)

type DetectionRuleType string

const (
	RuleTypePII    DetectionRuleType = "pii"
	RuleTypeCustom DetectionRuleType = "custom"
)

type DetectionRule struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Type        DetectionRuleType `json:"type"`
	Description string            `json:"description"`
	Prompt      string            `json:"prompt"`
	Categories  []string          `json:"categories,omitempty"`
	Regexps     []string          `json:"regexps,omitempty"`
	Severity    string            `json:"severity"`
}

type DetectionFinding struct {
	RuleID                  string   `json:"rule_id"`
	RuleName                string   `json:"rule_name"`
	Severity                string   `json:"severity"`
	Categories              []string `json:"categories,omitempty"`
	Summary                 string   `json:"summary"`
	Snippets                []string `json:"snippets,omitempty"`
	IdentificationRiskScore float64  `json:"identification_risk_score,omitempty"`
}

type InspectionResult struct {
	HasFindings bool               `json:"has_findings"`
	Findings    []DetectionFinding `json:"findings"`
}

type ContentInspector interface {
	Inspect(ctx context.Context, deploymentID string, tenantID string, normalizedText string, metadata map[string]interface{}, rules []DetectionRule) (InspectionResult, error)
}

type inspector struct {
	llm    shared.LLMClient
	logger *zap.Logger
}

func NewInspector(llm shared.LLMClient, logger *zap.Logger) ContentInspector {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &inspector{llm: llm, logger: logger.Named("content-inspector")}
}

// Inspect runs deterministic regex rules then LLM rules.
func (i *inspector) Inspect(ctx context.Context, deploymentID string, tenantID string, normalizedText string, metadata map[string]interface{}, rules []DetectionRule) (InspectionResult, error) {
	var findings []DetectionFinding
	text := normalizedText

	for _, rule := range rules {
		// regex-based
		for _, rx := range rule.Regexps {
			re, err := regexp.Compile(rx)
			if err != nil {
				continue
			}
			locs := re.FindAllStringIndex(text, 3)
			if len(locs) > 0 {
				var snippets []string
				for _, loc := range locs {
					sn := snippet(text, loc[0], loc[1])
					snippets = append(snippets, sn)
				}
				findings = append(findings, DetectionFinding{
					RuleID:     rule.ID,
					RuleName:   rule.Name,
					Severity:   rule.Severity,
					Categories: rule.Categories,
					Summary:    rule.Description,
					Snippets:   snippets,
				})
				break
			}
		}

		// LLM-based: if no regex or prompt exists
		if rule.Prompt != "" {
			answer, err := i.llm.GenerateAnswer(ctx, rule.Prompt+"\n\nTexte:\n"+text+"\n\nDonne un score 0-1 et un court résumé. Format: SCORE=<float>; SUMMARY=<texte>", 200)
			if err != nil {
				i.logger.Warn("LLM inspection failed", zap.Error(err))
				continue
			}
			score := parseScore(answer)
			summary := answer
			sev := rule.Severity
			findings = append(findings, DetectionFinding{
				RuleID:                  rule.ID,
				RuleName:                rule.Name,
				Severity:                sev,
				Categories:              rule.Categories,
				Summary:                 summary,
				IdentificationRiskScore: score,
			})
		}
	}

	return InspectionResult{
		HasFindings: len(findings) > 0,
		Findings:    findings,
	}, nil
}

func snippet(text string, start, end int) string {
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	s := text[start:end]
	if len(s) > 80 {
		s = s[:80]
	}
	return strings.TrimSpace(s)
}

func parseScore(ans string) float64 {
	ans = strings.TrimSpace(ans)
	idx := strings.Index(ans, "SCORE=")
	if idx == -1 {
		return 0
	}
	sub := ans[idx+6:]
	parts := strings.Split(sub, ";")
	if len(parts) == 0 {
		return 0
	}
	val := strings.TrimSpace(parts[0])
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0
	}
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	return f
}

// Built-in profiles
func DefaultProfile(profile string) []DetectionRule {
	switch profile {
	case "pii_basic":
		return []DetectionRule{
			{ID: "pii_email", Name: "Email", Type: RuleTypePII, Description: "Email detected", Categories: []string{"email"}, Regexps: []string{`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`}, Severity: "warn"},
			{ID: "pii_phone", Name: "Phone", Type: RuleTypePII, Description: "Phone detected", Categories: []string{"phone"}, Regexps: []string{`\\+?[0-9][0-9 .-]{7,}[0-9]`}, Severity: "warn"},
			{ID: "pii_iban", Name: "IBAN", Type: RuleTypePII, Description: "IBAN detected", Categories: []string{"iban"}, Regexps: []string{`[A-Z]{2}[0-9A-Z]{13,30}`}, Severity: "warn"},
		}
	case "pii_strict":
		return []DetectionRule{
			{ID: "pii_email", Name: "Email", Type: RuleTypePII, Description: "Email detected", Categories: []string{"email"}, Regexps: []string{`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`}, Severity: "block"},
			{ID: "pii_phone", Name: "Phone", Type: RuleTypePII, Description: "Phone detected", Categories: []string{"phone"}, Regexps: []string{`\\+?[0-9][0-9 .-]{7,}[0-9]`}, Severity: "block"},
			{ID: "pii_iban", Name: "IBAN", Type: RuleTypePII, Description: "IBAN detected", Categories: []string{"iban"}, Regexps: []string{`[A-Z]{2}[0-9A-Z]{13,30}`}, Severity: "block"},
		}
	case "cv_identifiability":
		return []DetectionRule{
			{
				ID:          "cv_identifiability",
				Name:        "CV identifiability risk",
				Type:        RuleTypePII,
				Description: "Estimate if the CV can re-identify a person",
				Categories:  []string{"cv", "identifiability"},
				Severity:    "warn",
				Prompt:      "Analyse ce CV. Donne SCORE=0..1 sur la possibilité de ré-identifier la personne, puis un court résumé.",
			},
		}
	default:
		return nil
	}
}
