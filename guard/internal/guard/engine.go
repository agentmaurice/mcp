package guard

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type Finding struct {
	Type  string `json:"type"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

type pattern struct {
	kind string
	re   *regexp.Regexp
}

var patterns = []pattern{
	{kind: "private_key", re: regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`)},
	{kind: "token", re: regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|secret)\s*[:=]\s*[A-Za-z0-9_./+\-=]{12,}`)},
	{kind: "email", re: regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)},
	{kind: "iban", re: regexp.MustCompile(`\b[A-Z]{2}[0-9]{2}(?:[ ]?[A-Z0-9]){11,30}\b`)},
	{kind: "payment_card", re: regexp.MustCompile(`\b(?:[0-9][ -]?){13,19}\b`)},
	{kind: "ipv4", re: regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)},
	{kind: "phone", re: regexp.MustCompile(`\b(?:\+[0-9]{1,3}[ .-]?)?(?:[0-9][ .-]?){8,14}[0-9]\b`)},
}

func Scan(text string) []Finding {
	findings := make([]Finding, 0)
	for _, p := range patterns {
		for _, loc := range p.re.FindAllStringIndex(text, -1) {
			if p.kind == "ipv4" && !validIPv4(text[loc[0]:loc[1]]) {
				continue
			}
			findings = append(findings, Finding{Type: p.kind, Start: loc[0], End: loc[1]})
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Start != findings[j].Start {
			return findings[i].Start < findings[j].Start
		}
		return findings[i].End > findings[j].End
	})
	return removeOverlaps(findings)
}

func Redact(text, mode string) (string, []Finding, error) {
	if mode == "" {
		mode = "mask"
	}
	if mode != "mask" && mode != "hash" && mode != "remove" {
		return "", nil, fmt.Errorf("unsupported redaction mode %q", mode)
	}
	findings := Scan(text)
	var out strings.Builder
	last := 0
	for _, finding := range findings {
		out.WriteString(text[last:finding.Start])
		switch mode {
		case "mask":
			out.WriteString("[REDACTED:" + finding.Type + "]")
		case "hash":
			hash := sha256.Sum256([]byte(text[finding.Start:finding.End]))
			out.WriteString(fmt.Sprintf("[HASH:%x]", hash[:6]))
		case "remove":
		}
		last = finding.End
	}
	out.WriteString(text[last:])
	return out.String(), findings, nil
}

func Classify(findings []Finding) string {
	level := 0
	for _, finding := range findings {
		candidate := 1
		switch finding.Type {
		case "private_key", "token", "payment_card":
			candidate = 3
		case "iban", "email", "phone":
			candidate = 2
		}
		if candidate > level {
			level = candidate
		}
	}
	return []string{"public", "internal", "confidential", "restricted"}[level]
}

func removeOverlaps(findings []Finding) []Finding {
	result := make([]Finding, 0, len(findings))
	for _, finding := range findings {
		if len(result) > 0 && finding.Start < result[len(result)-1].End {
			continue
		}
		result = append(result, finding)
	}
	return result
}

func validIPv4(value string) bool {
	parts := strings.Split(value, ".")
	for _, part := range parts {
		var value int
		if _, err := fmt.Sscanf(part, "%d", &value); err != nil || value > 255 {
			return false
		}
	}
	return true
}
