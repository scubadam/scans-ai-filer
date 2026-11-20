package maint

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/scubadam/scans-ai-filer/internal/ai"
	"github.com/scubadam/scans-ai-filer/internal/storage"
	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

// ReorgMove represents a single planned or executed folder move and/or filename sanitization.
type ReorgMove struct {
	FileID      string
	OldPath     string
	NewPath     string
	OldName     string
	NewName     string
	MatchedOrg  string
	MoveFolder  bool
	RenameFile  bool
	RenameWhy   string
	TargetRule  taxonomy.Rule
	OldParentID string
}

// ReorgResult summarizes a storage reorganization run.
type ReorgResult struct {
	TotalScanned  int
	PlannedMoves  []ReorgMove
	MovedCount    int
	RenamedCount  int
	PrunedFolders int
	Errors        int
}

// ReorgOptions configures ReorgStorage.
type ReorgOptions struct {
	DryRun            bool
	SanitizeFilenames bool
	PruneEmpty        bool
	Limit             int
}

var yearInPathRe = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)

// ReorgStorage matches existing filed documents in storage against canonical taxonomy rules,
// optionally runs a single-pass AI filename PII audit, moves/renames mismatched files, and
// prunes empty leftover folders.
func ReorgStorage(ctx context.Context, baseStore storage.Provider, maintStore storage.MaintenanceProvider, taxStore taxonomy.Store, completer ai.TextCompleter, opts ReorgOptions, logger *slog.Logger) (*ReorgResult, error) {
	if maintStore == nil {
		return nil, fmt.Errorf("storage provider does not support maintenance reorganization")
	}
	if taxStore == nil {
		return nil, fmt.Errorf("taxonomy store is required for --reorg-storage")
	}
	if logger == nil {
		logger = slog.Default()
	}

	rules, err := taxStore.ListRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("list taxonomy rules: %w", err)
	}
	docs, err := maintStore.ListFiledDocuments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list filed documents: %w", err)
	}

	// Optional batch filename PII audit via AI (single fast text call over all filenames, zero PDF downloads)
	renameMap := make(map[string]struct {
		newName string
		reason  string
	})
	if opts.SanitizeFilenames && completer != nil && len(docs) > 0 {
		renames, err := auditFilenamesForPII(ctx, docs, completer)
		if err != nil {
			logger.Warn("filename PII audit failed; proceeding with folder reorg only", "err", err)
		} else {
			for _, r := range renames {
				if r.ID != "" && strings.TrimSpace(r.NewName) != "" {
					renameMap[r.ID] = struct {
						newName string
						reason  string
					}{newName: strings.TrimSpace(r.NewName), reason: r.Reason}
				}
			}
		}
	}

	res := &ReorgResult{TotalScanned: len(docs)}
	for _, doc := range docs {
		rule, matched := matchDocToRule(doc, rules)
		var moveFolder bool
		targetFolder := strings.Trim(doc.FolderPath, "/")

		if matched && strings.TrimSpace(rule.PathTemplate) != "" {
			yr := extractDocYear(doc)
			candidate := substitutePathYear(rule.PathTemplate, yr)
			if !strings.EqualFold(candidate, targetFolder) {
				targetFolder = candidate
				moveFolder = true
			}
		}

		newName := doc.Name
		renameFile := false
		renameWhy := ""
		if rn, ok := renameMap[doc.ID]; ok && rn.newName != doc.Name {
			newName = taxonomy.SanitizeFilename(rn.newName, ".pdf")
			renameFile = true
			renameWhy = rn.reason
		}

		if !moveFolder && !renameFile {
			continue
		}

		res.PlannedMoves = append(res.PlannedMoves, ReorgMove{
			FileID:      doc.ID,
			OldPath:     doc.FolderPath,
			NewPath:     targetFolder,
			OldName:     doc.Name,
			NewName:     newName,
			MatchedOrg:  rule.Organization,
			MoveFolder:  moveFolder,
			RenameFile:  renameFile,
			RenameWhy:   renameWhy,
			TargetRule:  rule,
			OldParentID: doc.ParentID,
		})
		if opts.Limit > 0 && len(res.PlannedMoves) >= opts.Limit {
			break
		}
	}

	if opts.DryRun {
		return res, nil
	}

	for _, mv := range res.PlannedMoves {
		destFolderID := mv.OldParentID
		if mv.MoveFolder {
			var err error
			destFolderID, err = baseStore.EnsureFolderPath(ctx, mv.NewPath)
			if err != nil {
				logger.Warn("failed to ensure target folder during reorg", "path", mv.NewPath, "err", err)
				res.Errors++
				continue
			}
		}
		finalName := mv.NewName
		if mv.MoveFolder || mv.RenameFile {
			if resolved, err := baseStore.ResolveUniqueFilename(ctx, destFolderID, mv.NewName); err == nil {
				finalName = resolved
			}
		}
		props := map[string]string{}
		if mv.TargetRule.Organization != "" {
			props["category"] = mv.TargetRule.Category
			props["subcategory"] = mv.TargetRule.Subcategory
			props["organization"] = mv.TargetRule.Organization
			props["taxonomy_matched"] = "true"
		}
		if err := maintStore.MoveFiledDocument(ctx, mv.FileID, mv.OldParentID, destFolderID, finalName, props); err != nil {
			logger.Warn("failed to move/rename filed document", "file", mv.OldName, "err", err)
			res.Errors++
			continue
		}
		if mv.MoveFolder {
			res.MovedCount++
		}
		if mv.RenameFile {
			res.RenamedCount++
		}
	}

	if opts.PruneEmpty && res.MovedCount > 0 {
		pruned, err := maintStore.PruneEmptyFolders(ctx)
		if err != nil {
			logger.Warn("failed to prune empty folders", "err", err)
		} else {
			res.PrunedFolders = pruned
		}
	}
	return res, nil
}

// FormatReorgReport renders a concise preview or summary of storage reorganization actions.
func FormatReorgReport(res *ReorgResult, dryRun bool) string {
	var b strings.Builder
	mode := "EXECUTED"
	if dryRun {
		mode = "DRY-RUN"
	}
	fmt.Fprintf(&b, "Storage Reorg (%s): scanned=%d planned=%d moved=%d renamed=%d pruned_folders=%d errors=%d\n",
		mode, res.TotalScanned, len(res.PlannedMoves), res.MovedCount, res.RenamedCount, res.PrunedFolders, res.Errors)
	for i, mv := range res.PlannedMoves {
		fmt.Fprintf(&b, "  %d. %s/%s\n", i+1, mv.OldPath, mv.OldName)
		if mv.MoveFolder {
			fmt.Fprintf(&b, "     MOVE   -> %s/ (rule: %s)\n", mv.NewPath, mv.MatchedOrg)
		}
		if mv.RenameFile {
			fmt.Fprintf(&b, "     RENAME -> %s (%s)\n", mv.NewName, mv.RenameWhy)
		}
	}
	return b.String()
}

func matchDocToRule(doc taxonomy.FiledDocument, rules []taxonomy.Rule) (taxonomy.Rule, bool) {
	// Extract candidate org names from properties, description Issuer, and folder path segments
	var candidates []string
	if v := strings.TrimSpace(doc.Properties["organization"]); v != "" {
		candidates = append(candidates, v)
	}
	docCat := strings.TrimSpace(doc.Properties["category"])
	docSub := strings.TrimSpace(doc.Properties["subcategory"])
	for _, line := range strings.Split(doc.Description, "\n") {
		low := strings.ToLower(strings.TrimSpace(line))
		if idx := strings.IndexByte(line, ':'); idx > 0 {
			val := strings.TrimSpace(line[idx+1:])
			switch {
			case strings.HasPrefix(low, "issuer:") && val != "":
				candidates = append(candidates, val)
			case strings.HasPrefix(low, "category:") && docCat == "" && val != "":
				docCat = val
			case strings.HasPrefix(low, "subcategory:") && docSub == "" && val != "":
				docSub = val
			}
		}
	}
	pathSegs := strings.Split(doc.FolderPath, "/")
	for i, seg := range pathSegs {
		seg = strings.TrimSpace(seg)
		if seg == "" || taxonomy.IsYear(seg) {
			continue
		}
		if i == 0 && docCat == "" {
			docCat = seg
		}
		if i == 1 && docSub == "" && len(pathSegs) >= 3 {
			docSub = seg
		}
		candidates = append(candidates, seg)
	}

	// Collect ALL matching rules (an organization can legitimately appear in multiple categories,
	// e.g. Direct Line in Vehicle > Insurance AND Housing > Insurance, or an NHS Trust in Employment AND Healthcare).
	var exactMatches []taxonomy.Rule
	seenExact := make(map[int]bool)
	for _, cand := range candidates {
		nc := taxonomy.NormalizeName(cand)
		if nc == "" {
			continue
		}
		for i, r := range rules {
			if !seenExact[i] && taxonomy.NormalizeName(r.Organization) == nc {
				exactMatches = append(exactMatches, r)
				seenExact[i] = true
			}
		}
	}

	if len(exactMatches) == 1 {
		return exactMatches[0], true
	}
	if len(exactMatches) > 1 {
		return pickBestRuleForDoc(doc, docCat, docSub, exactMatches), true
	}

	// Pass 2: alias match in comma-separated Keywords (e.g. "Kingsland House" merged into "Barchester Healthcare")
	var aliasMatches []taxonomy.Rule
	seenAlias := make(map[int]bool)
	for _, cand := range candidates {
		nc := taxonomy.NormalizeName(cand)
		if len(nc) < 4 {
			continue
		}
		for i, r := range rules {
			if seenAlias[i] {
				continue
			}
			for _, kw := range strings.Split(r.Keywords, ",") {
				if taxonomy.NormalizeName(kw) == nc {
					aliasMatches = append(aliasMatches, r)
					seenAlias[i] = true
					break
				}
			}
		}
	}
	if len(aliasMatches) == 1 {
		return aliasMatches[0], true
	}
	if len(aliasMatches) > 1 {
		return pickBestRuleForDoc(doc, docCat, docSub, aliasMatches), true
	}
	return taxonomy.Rule{}, false
}

// pickBestRuleForDoc disambiguates multi-category organizations using the document's existing
// category, subcategory, folder path, and filename/description keyword overlap.
func pickBestRuleForDoc(doc taxonomy.FiledDocument, docCat, docSub string, candidates []taxonomy.Rule) taxonomy.Rule {
	normDocCat := taxonomy.NormalizeName(docCat)
	normDocSub := taxonomy.NormalizeName(docSub)
	yr := extractDocYear(doc)
	haystack := strings.ToLower(doc.FolderPath + " " + doc.Name + " " + doc.Description)

	best := candidates[0]
	bestScore := -1
	for _, r := range candidates {
		score := 0
		// Strongest signal: document is already in this exact rule's folder
		if strings.EqualFold(substitutePathYear(r.PathTemplate, yr), strings.Trim(doc.FolderPath, "/")) {
			score += 50
		}
		if normDocCat != "" && taxonomy.NormalizeName(r.Category) == normDocCat {
			score += 20
		}
		if normDocSub != "" && taxonomy.NormalizeName(r.Subcategory) == normDocSub {
			score += 10
		}
		for _, kw := range strings.Split(r.Keywords, ",") {
			kw = strings.ToLower(strings.TrimSpace(kw))
			if len(kw) >= 3 && strings.Contains(haystack, kw) {
				score += 3
			}
		}
		if score > bestScore {
			bestScore = score
			best = r
		}
	}
	return best
}

func extractDocYear(doc taxonomy.FiledDocument) string {
	if d := strings.TrimSpace(doc.Properties["doc_date"]); len(d) >= 4 && taxonomy.IsYear(d[:4]) {
		return d[:4]
	}
	if m := yearInPathRe.FindString(doc.Name); m != "" {
		return m
	}
	for _, seg := range strings.Split(doc.FolderPath, "/") {
		if taxonomy.IsYear(seg) {
			return strings.TrimSpace(seg)
		}
	}
	return ""
}

func substitutePathYear(tpl, year string) string {
	segs := strings.Split(strings.Trim(tpl, "/"), "/")
	var out []string
	for _, s := range segs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		switch strings.ToLower(s) {
		case "{year}", "{yyyy}", "yyyy":
			if year != "" {
				out = append(out, year)
			}
		default:
			out = append(out, s)
		}
	}
	return strings.Join(out, "/")
}

type filenameAuditItem struct {
	ID      string `json:"id"`
	NewName string `json:"new_name"`
	Reason  string `json:"reason"`
}

func auditFilenamesForPII(ctx context.Context, docs []taxonomy.FiledDocument, completer ai.TextCompleter) ([]filenameAuditItem, error) {
	var sb strings.Builder
	for _, d := range docs {
		fmt.Fprintf(&sb, "- id=%q name=%q\n", d.ID, d.Name)
	}
	prompt := fmt.Sprintf(`Review these document filenames and flag ONLY filenames that contain sensitive personal PII (e.g., personal full names like "John Smith", account/policy/NI/NHS/passport numbers, street addresses, dates of birth, or specific medical diagnoses).
Do NOT flag organization names, company names, hospital names, council names, or generic document types.

FILENAMES:
%s

For each flagged filename, propose a clean, privacy-safe replacement preserving the exact date prefix, organization, document type, and file extension (replacing only the PII portion with a generic 2-5 word summary).
Return ONLY valid JSON:
{
  "renames": [
    {"id": "...", "new_name": "YYYY-MM-DD - Org - Type - Generic Summary.pdf", "reason": "Removed personal name"}
  ]
}`, sb.String())

	raw, err := completer.CompleteJSON(ctx, prompt)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Renames []filenameAuditItem `json:"renames"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("decode filename PII audit JSON: %w", err)
	}
	return parsed.Renames, nil
}
