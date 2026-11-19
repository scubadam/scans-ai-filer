package taxonomy

import (
	"strings"
	"testing"
)

func TestParseAndFormat(t *testing.T) {
	csvData := `Category,Subcategory,Organization,Keywords,Path_Template,Description
Finance,Banking,Example Bank,"example bank,statement",Finance/Banking/Example Bank/{year},Bank statements
Utilities,Energy,Example Energy,"gas,electricity",Utilities/Energy/Example Energy/{year},Utility bills
`
	rules, err := Parse(strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
	if rules[0].Organization != "Example Bank" || rules[0].PathTemplate != "Finance/Banking/Example Bank/{year}" {
		t.Errorf("unexpected rule[0]: %+v", rules[0])
	}

	prompt := FormatForPrompt(rules)
	if !strings.Contains(prompt, `Organization: "Example Bank"`) {
		t.Errorf("prompt missing Example Bank: %s", prompt)
	}
	if !strings.Contains(FormatForPrompt(nil), "No pre-defined taxonomy") {
		t.Errorf("expected fallback message for empty taxonomy")
	}
}

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"Example Bank Ltd.":         "example bank",
		"Smith & Sons Limited":      "smith son",
		"British Gas Services PLC":  "british gas service",
		"  Alpha, Beta & Gamma LLC": "alpha beta gamma",
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	if got := SanitizeFilename(`2026-11-19 - Bank: "Statement" / Q4`, ".pdf"); got != "2026-11-19 - Bank Statement Q4.pdf" {
		t.Errorf("unexpected sanitized filename: %q", got)
	}
	if got := SanitizeFilename("2026-11-19 - Invoice.PDF", ".pdf"); got != "2026-11-19 - Invoice.PDF" {
		t.Errorf("should preserve existing extension, got %q", got)
	}
	if got := SanitizeFilename("   ", ".jpg"); got != "Document.jpg" {
		t.Errorf("empty filename fallback failed: %q", got)
	}
}
