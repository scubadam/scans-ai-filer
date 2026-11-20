package taxonomy

import (
	"fmt"
	"sort"
	"strings"
)

// FiledDocument represents an already-filed document discovered in storage (e.g. Google Drive Scans/ tree)
// used to bootstrap or recalculate the taxonomy database and diff against taxonomy.csv.
type FiledDocument struct {
	ID          string            // Storage file identifier
	ParentID    string            // Immediate parent folder identifier
	MimeType    string            // MIME type (e.g. application/pdf, image/jpeg)
	FolderPath  string            // Relative to Scans root, e.g. "Utilities/Energy/Octopus Energy/2026"
	Name        string            // Filename
	Description string            // Raw storage description (legacy "Details:" lines are stripped automatically)
	Properties  map[string]string // Optional structured metadata properties
}

// DiffReport compares CSV taxonomy rules against rules derived from existing filed storage documents.
type DiffReport struct {
	OnlyInCSV     []Rule
	OnlyInStorage []Rule
	ChangedPaths  []PathDiff
	MatchedCount  int
}

// PathDiff records an organization present in both CSV and storage with different path templates.
type PathDiff struct {
	Organization string
	CSVPath      string
	StoragePath  string
}

// DeriveRulesFromFiledDocs infers deduplicated canonical taxonomy rules and PII-free filing history
// from existing filed documents in storage. Any legacy "Details:" lines in descriptions are ignored.
func DeriveRulesFromFiledDocs(docs []FiledDocument) ([]Rule, []FilingExample) {
	type agg struct {
		rule     Rule
		keywords map[string]struct{}
	}
	byKey := make(map[string]*agg)
	var examples []FilingExample

	for _, doc := range docs {
		relPath := strings.Trim(strings.TrimSpace(doc.FolderPath), "/")
		if relPath == "" {
			continue
		}
		segs := splitCleanPath(relPath)
		if len(segs) < 2 {
			continue
		}
		// Skip inbox / unsorted / system folders
		firstLower := strings.ToLower(segs[0])
		if strings.HasPrefix(firstLower, "inbox") || firstLower == "unsorted" || firstLower == "archive" {
			continue
		}

		// Replace 4-digit year segments with {year} for the canonical PathTemplate
		tplSegs := make([]string, len(segs))
		nonYear := make([]string, 0, len(segs))
		for i, seg := range segs {
			if IsYear(seg) {
				tplSegs[i] = "{year}"
			} else {
				tplSegs[i] = seg
				nonYear = append(nonYear, seg)
			}
		}
		if len(nonYear) < 2 {
			continue
		}
		pathTemplate := strings.Join(tplSegs, "/")

		// Extract sanitized fields from properties first, then sanitized description lines, then folder structure
		descFields := parseSanitizedDescription(doc.Description)
		category := firstNonEmpty(doc.Properties["category"], descFields["category"], nonYear[0])
		var subcategory, org string
		if len(nonYear) >= 3 {
			subcategory = firstNonEmpty(doc.Properties["subcategory"], descFields["subcategory"], nonYear[1])
			org = firstNonEmpty(doc.Properties["organization"], descFields["issuer"], nonYear[len(nonYear)-1])
		} else {
			subcategory = firstNonEmpty(doc.Properties["subcategory"], descFields["subcategory"])
			org = firstNonEmpty(doc.Properties["organization"], descFields["issuer"], nonYear[1])
		}
		docType := firstNonEmpty(doc.Properties["doc_type"], descFields["type"])
		summary := descFields["summary"]

		if category == "" || org == "" {
			continue
		}

		examples = append(examples, FilingExample{
			Category:     category,
			Subcategory:  subcategory,
			Organization: org,
			DocumentType: docType,
			Summary:      summary,
			FolderPath:   pathTemplate,
		})

		key := NormalizeName(category) + "|" + NormalizeName(org)
		entry, ok := byKey[key]
		if !ok {
			entry = &agg{
				rule: Rule{
					Category:     category,
					Subcategory:  subcategory,
					Organization: org,
					PathTemplate: pathTemplate,
				},
				keywords: make(map[string]struct{}),
			}
			byKey[key] = entry
		}
		if entry.rule.Subcategory == "" && subcategory != "" {
			entry.rule.Subcategory = subcategory
		}
		if docType != "" {
			entry.keywords[docType] = struct{}{}
		}
	}

	rules := make([]Rule, 0, len(byKey))
	for _, entry := range byKey {
		kws := make([]string, 0, len(entry.keywords))
		for k := range entry.keywords {
			kws = append(kws, k)
		}
		sort.Strings(kws)
		entry.rule.Keywords = strings.Join(kws, ", ")
		rules = append(rules, entry.rule)
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Category != rules[j].Category {
			return rules[i].Category < rules[j].Category
		}
		return rules[i].Organization < rules[j].Organization
	})
	return rules, examples
}

// DiffRules compares CSV taxonomy rules against storage-derived rules.
func DiffRules(csvRules, storageRules []Rule) DiffReport {
	csvMap := make(map[string]Rule, len(csvRules))
	for _, r := range csvRules {
		csvMap[ruleNormKey(r)] = r
	}
	storeMap := make(map[string]Rule, len(storageRules))
	for _, r := range storageRules {
		storeMap[ruleNormKey(r)] = r
	}

	var report DiffReport
	for k, cr := range csvMap {
		sr, ok := storeMap[k]
		if !ok {
			report.OnlyInCSV = append(report.OnlyInCSV, cr)
			continue
		}
		if !strings.EqualFold(strings.Trim(cr.PathTemplate, "/"), strings.Trim(sr.PathTemplate, "/")) {
			report.ChangedPaths = append(report.ChangedPaths, PathDiff{
				Organization: cr.Organization,
				CSVPath:      cr.PathTemplate,
				StoragePath:  sr.PathTemplate,
			})
		} else {
			report.MatchedCount++
		}
	}
	for k, sr := range storeMap {
		if _, ok := csvMap[k]; !ok {
			report.OnlyInStorage = append(report.OnlyInStorage, sr)
		}
	}

	sort.Slice(report.OnlyInCSV, func(i, j int) bool { return report.OnlyInCSV[i].Organization < report.OnlyInCSV[j].Organization })
	sort.Slice(report.OnlyInStorage, func(i, j int) bool { return report.OnlyInStorage[i].Organization < report.OnlyInStorage[j].Organization })
	sort.Slice(report.ChangedPaths, func(i, j int) bool { return report.ChangedPaths[i].Organization < report.ChangedPaths[j].Organization })
	return report
}

// FormatDiffReport renders a human-readable summary comparing taxonomy.csv and storage-derived rules.
func FormatDiffReport(d DiffReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Taxonomy Diff: %d exact matches | %d only in storage | %d only in CSV | %d path differences\n",
		d.MatchedCount, len(d.OnlyInStorage), len(d.OnlyInCSV), len(d.ChangedPaths))
	if len(d.OnlyInStorage) > 0 {
		b.WriteString("\nDiscovered in storage (not in CSV):\n")
		for _, r := range d.OnlyInStorage {
			fmt.Fprintf(&b, "  + [%s > %s] %s -> %s\n", r.Category, r.Subcategory, r.Organization, r.PathTemplate)
		}
	}
	if len(d.ChangedPaths) > 0 {
		b.WriteString("\nPath differences (CSV vs Storage):\n")
		for _, p := range d.ChangedPaths {
			fmt.Fprintf(&b, "  ~ %s:\n      CSV:     %s\n      Storage: %s\n", p.Organization, p.CSVPath, p.StoragePath)
		}
	}
	if len(d.OnlyInCSV) > 0 {
		b.WriteString("\nPresent in CSV (no files found in storage yet):\n")
		for _, r := range d.OnlyInCSV {
			fmt.Fprintf(&b, "  - [%s > %s] %s -> %s\n", r.Category, r.Subcategory, r.Organization, r.PathTemplate)
		}
	}
	return b.String()
}

// parseSanitizedDescription parses key: value lines from Drive descriptions while
// explicitly discarding legacy "Details:" lines that may contain sensitive PII.
func parseSanitizedDescription(desc string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(desc, "\n") {
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:idx]))
		val := strings.TrimSpace(line[idx+1:])
		switch key {
		case "category", "subcategory", "issuer", "type", "summary":
			if val != "" {
				out[key] = val
			}
		case "details":
			// Explicitly ignore legacy Details lines containing PII
		}
	}
	return out
}

func splitCleanPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
