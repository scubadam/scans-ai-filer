package taxonomy

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Rule defines a single filing taxonomy catalog entry.
type Rule struct {
	Category     string
	Subcategory  string
	Organization string
	Keywords     string
	PathTemplate string
	Description  string
}

var (
	punctRe      = regexp.MustCompile(`['’"\.&,]`)
	andRe        = regexp.MustCompile(`\band\b`)
	suffixRe     = regexp.MustCompile(`\b(ltd|limited|plc|llc|estates|trust|school)\b`)
	invalidFnRe  = regexp.MustCompile(`[\\/*?:"<>|]`)
	multiSpaceRe = regexp.MustCompile(`\s+`)
	extRe        = regexp.MustCompile(`\.[a-zA-Z0-9]{2,5}$`)
	yearRe       = regexp.MustCompile(`^\d{4}$`)
	nonWordRe    = regexp.MustCompile(`\W+`)
)

// Load searches candidate paths for a taxonomy CSV file and parses all valid rules.
// If no file is found, it returns an empty slice and nil error (fallback taxonomy mode).
func Load(explicitPath string) ([]Rule, string, error) {
	candidates := []string{
		explicitPath,
		"/app/taxonomy.csv",
		"/etc/scans-ai-filer/taxonomy.csv",
		"taxonomy.local.csv",
		"taxonomy.csv",
	}

	var actualPath string
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			actualPath = p
			break
		}
	}
	if actualPath == "" {
		return nil, "", nil
	}

	f, err := os.Open(actualPath)
	if err != nil {
		return nil, actualPath, fmt.Errorf("open taxonomy CSV %q: %w", actualPath, err)
	}
	defer f.Close()

	rules, err := Parse(f)
	if err != nil {
		return nil, actualPath, fmt.Errorf("parse taxonomy CSV %q: %w", actualPath, err)
	}
	return rules, actualPath, nil
}

// Parse reads taxonomy rules from CSV content.
func Parse(r io.Reader) ([]Rule, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, err
	}

	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.TrimSpace(h)] = i
	}

	get := func(row []string, col string) string {
		i, ok := idx[col]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	var rules []Rule
	for {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rule := Rule{
			Category:     get(row, "Category"),
			Subcategory:  get(row, "Subcategory"),
			Organization: get(row, "Organization"),
			Keywords:     get(row, "Keywords"),
			PathTemplate: get(row, "Path_Template"),
			Description:  get(row, "Description"),
		}
		if rule.Category != "" && rule.Organization != "" {
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

// FormatForPrompt renders the taxonomy catalog as markdown instructions for an AI provider.
func FormatForPrompt(rules []Rule) string {
	if len(rules) == 0 {
		return "No pre-defined taxonomy provided. Use sensible categories (Finance, Housing, Education, Vehicle, Utilities, Personal, etc.)."
	}

	var b strings.Builder
	b.WriteString("TAXONOMY CATALOG (Matched entities MUST use the exact Category, Organization, and Path_Template):\n")
	for _, r := range rules {
		fmt.Fprintf(&b, "- [%s > %s] Organization: %q | Keywords: %s | Path: %q",
			r.Category, r.Subcategory, r.Organization, r.Keywords, r.PathTemplate)
		if r.Description != "" {
			fmt.Fprintf(&b, " (%s)", r.Description)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// NormalizeName normalizes folder/entity names for deduplication matching.
func NormalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = punctRe.ReplaceAllString(s, " ")
	s = andRe.ReplaceAllString(s, "")
	s = suffixRe.ReplaceAllString(s, "")

	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, w := range fields {
		if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			w = w[:len(w)-1]
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// IsYear reports whether s is a 4-digit year string.
func IsYear(s string) bool {
	return yearRe.MatchString(strings.TrimSpace(s))
}

// SanitizeFilename strips unsafe filesystem/Drive characters and ensures an extension.
func SanitizeFilename(name, fallbackExt string) string {
	if fallbackExt == "" {
		fallbackExt = ".pdf"
	}
	if !strings.HasPrefix(fallbackExt, ".") {
		fallbackExt = "." + fallbackExt
	}
	cleaned := invalidFnRe.ReplaceAllString(name, "")
	cleaned = multiSpaceRe.ReplaceAllString(cleaned, " ")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		cleaned = "Document"
	}
	if !extRe.MatchString(cleaned) {
		cleaned += fallbackExt
	}
	return cleaned
}

// SplitExt splits a filename into base and extension (matching os.path.splitext).
func SplitExt(name string) (string, string) {
	ext := filepath.Ext(name)
	return strings.TrimSuffix(name, ext), ext
}

// SanitizeTag strips non-alphanumeric characters for hashtag formatting.
func SanitizeTag(s string) string {
	return nonWordRe.ReplaceAllString(s, "")
}
