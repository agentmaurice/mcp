package guard

import (
	"strings"
	"testing"
)

func TestScanDoesNotCopySensitiveValues(t *testing.T) {
	text := "contact alice@example.com with api_key=abcdefghijklmnop"
	findings := Scan(text)
	if len(findings) != 2 || findings[0].Type != "email" || findings[1].Type != "token" {
		t.Fatalf("unexpected findings: %#v", findings)
	}
}

func TestRedactMasksAndClassifies(t *testing.T) {
	redacted, findings, err := Redact("mail alice@example.com", "mask")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redacted, "alice@example.com") || !strings.Contains(redacted, "[REDACTED:email]") {
		t.Fatalf("unexpected redaction: %q", redacted)
	}
	if got := Classify(findings); got != "confidential" {
		t.Fatalf("classification = %q", got)
	}
}

func TestScanRejectsInvalidIPv4(t *testing.T) {
	for _, finding := range Scan("999.12.12.12") {
		if finding.Type == "ipv4" {
			t.Fatalf("invalid IPv4 detected: %#v", finding)
		}
	}
}
