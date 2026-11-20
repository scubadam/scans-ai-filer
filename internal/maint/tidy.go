package maint

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/scubadam/scans-ai-filer/internal/ai"
	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

// TidyAction describes a single AI-proposed taxonomy consolidation or cleanup operation.
type TidyAction struct {
	Action             string `json:"action"` // "merge" | "update" | "delete"
	SourceCategory     string `json:"source_category"`
	SourceOrganization string `json:"source_organization"`
	TargetCategory     string `json:"target_category"`
	TargetSubcategory  string `json:"target_subcategory"`
	TargetOrganization string `json:"target_organization"`
	TargetKeywords     string `json:"target_keywords"`
	TargetPathTemplate string `json:"target_path_template"`
	Reason             string `json:"reason"`
}

// TidyResult summarizes proposed and applied taxonomy tidy actions.
type TidyResult struct {
	Actions []TidyAction
	Applied int
}

// TidyTaxonomy asks an AI TextCompleter to review the current PII-free taxonomy rules
// and propose consolidations (merging subsidiaries/duplicates, fixing category drift, enriching keywords).
func TidyTaxonomy(ctx context.Context, store taxonomy.Store, completer ai.TextCompleter, dryRun bool, logger *slog.Logger) (*TidyResult, error) {
	if store == nil {
		return nil, fmt.Errorf("taxonomy store is required for --tidy-taxonomy")
	}
	if completer == nil {
		return nil, fmt.Errorf("AI provider does not support text JSON completions")
	}
	if logger == nil {
		logger = slog.Default()
	}

	rules, err := store.ListRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("list taxonomy rules: %w", err)
	}
	if len(rules) == 0 {
		return &TidyResult{}, nil
	}

	prompt := fmt.Sprintf(`You are an expert personal document taxonomy architect.
Review the following document filing taxonomy rules and identify ONLY clear, high-confidence cleanup actions:
1. "merge": Merge duplicate organizations, punctuation variants (e.g. "BCS, The Chartered Institute for IT" vs "BCS The Chartered Institute for IT", "CIBT (UK) Ltd." vs "CIBT", "Western Sussex Hospitals NHS Trust" vs "Western Sussex Hospitals NHS Foundation Trust"), or subsidiaries/facilities into their canonical parent (e.g. a care home name into its care group, preserving the facility/alias name in target_keywords).
2. "update": Fix obvious category/path inconsistencies or add helpful missing keywords.
3. "delete": Remove clearly erroneous or junk rules that shouldn't exist.

RULES TO REVIEW (%d entries):
%s

IMPORTANT:
- Keep existing well-formed rules untouched — only emit actions where there is a genuine duplicate, subsidiary, or misclassified entry.
- For "merge", set source_category and source_organization to the redundant rule to remove, and target_* fields to the canonical rule to keep/update (ALWAYS include source_organization in target_keywords so future documents match the canonical rule!).
- Preserve "{year}" in target_path_template where appropriate.
- Return ONLY a valid JSON object with exact shape:
{
  "actions": [
    {
      "action": "merge",
      "source_category": "Old Category",
      "source_organization": "Old Org Name",
      "target_category": "Canonical Category",
      "target_subcategory": "Canonical Subcategory",
      "target_organization": "Canonical Organization",
      "target_keywords": "existing keywords, Old Org Name",
      "target_path_template": "Canonical Category/Canonical Subcategory/Canonical Organization/{year}",
      "reason": "Short explanation"
    }
  ]
}`, len(rules), taxonomy.FormatForPrompt(rules))

	rawJSON, err := completer.CompleteJSON(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("AI tidy request failed: %w", err)
	}

	var parsed struct {
		Actions []TidyAction `json:"actions"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &parsed); err != nil {
		return nil, fmt.Errorf("decode AI tidy JSON (%q): %w", rawJSON, err)
	}

	res := &TidyResult{Actions: parsed.Actions}
	if dryRun {
		return res, nil
	}

	for _, act := range parsed.Actions {
		switch strings.ToLower(strings.TrimSpace(act.Action)) {
		case "merge":
			if act.TargetCategory != "" && act.TargetOrganization != "" {
				kw := ensureAliasInKeywords(act.TargetKeywords, act.SourceOrganization)
				if err := store.UpsertRule(ctx, taxonomy.Rule{
					Category:     act.TargetCategory,
					Subcategory:  act.TargetSubcategory,
					Organization: act.TargetOrganization,
					Keywords:     kw,
					PathTemplate: act.TargetPathTemplate,
				}, "tidy"); err != nil {
					logger.Warn("failed to upsert merge target rule", "org", act.TargetOrganization, "err", err)
					continue
				}
			}
			if act.SourceCategory != "" && act.SourceOrganization != "" {
				// Only delete source if its normalized key differs from target
				srcKey := taxonomy.NormalizeName(act.SourceCategory) + "|" + taxonomy.NormalizeName(act.SourceOrganization)
				dstKey := taxonomy.NormalizeName(act.TargetCategory) + "|" + taxonomy.NormalizeName(act.TargetOrganization)
				if srcKey != dstKey {
					if err := store.DeleteRule(ctx, act.SourceCategory, act.SourceOrganization); err != nil {
						logger.Warn("failed to delete merged source rule", "org", act.SourceOrganization, "err", err)
						continue
					}
				}
			}
			res.Applied++
		case "update":
			if act.TargetCategory == "" || act.TargetOrganization == "" {
				continue
			}
			if err := store.UpsertRule(ctx, taxonomy.Rule{
				Category:     act.TargetCategory,
				Subcategory:  act.TargetSubcategory,
				Organization: act.TargetOrganization,
				Keywords:     act.TargetKeywords,
				PathTemplate: act.TargetPathTemplate,
			}, "tidy"); err != nil {
				logger.Warn("failed to update rule", "org", act.TargetOrganization, "err", err)
				continue
			}
			res.Applied++
		case "delete":
			if act.SourceCategory == "" || act.SourceOrganization == "" {
				continue
			}
			if err := store.DeleteRule(ctx, act.SourceCategory, act.SourceOrganization); err != nil {
				logger.Warn("failed to delete rule", "org", act.SourceOrganization, "err", err)
				continue
			}
			res.Applied++
		}
	}
	return res, nil
}

// FormatTidyReport renders a human-readable summary of proposed taxonomy tidy actions.
func FormatTidyReport(res *TidyResult, dryRun bool) string {
	var b strings.Builder
	mode := "APPLIED"
	if dryRun {
		mode = "DRY-RUN"
	}
	fmt.Fprintf(&b, "AI Taxonomy Tidy (%s): %d proposed action(s)\n", mode, len(res.Actions))
	for i, a := range res.Actions {
		switch strings.ToLower(a.Action) {
		case "merge":
			fmt.Fprintf(&b, "  %d. [MERGE] [%s] %q -> [%s > %s] %q (%s)\n     Keywords: %s\n     Reason: %s\n",
				i+1, a.SourceCategory, a.SourceOrganization, a.TargetCategory, a.TargetSubcategory,
				a.TargetOrganization, a.TargetPathTemplate, a.TargetKeywords, a.Reason)
		case "update":
			fmt.Fprintf(&b, "  %d. [UPDATE] [%s > %s] %q -> %s\n     Keywords: %s\n     Reason: %s\n",
				i+1, a.TargetCategory, a.TargetSubcategory, a.TargetOrganization,
				a.TargetPathTemplate, a.TargetKeywords, a.Reason)
		case "delete":
			fmt.Fprintf(&b, "  %d. [DELETE] [%s] %q\n     Reason: %s\n",
				i+1, a.SourceCategory, a.SourceOrganization, a.Reason)
		}
	}
	return b.String()
}

func ensureAliasInKeywords(keywords, alias string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return strings.TrimSpace(keywords)
	}
	if strings.Contains(strings.ToLower(keywords), strings.ToLower(alias)) {
		return strings.TrimSpace(keywords)
	}
	if strings.TrimSpace(keywords) == "" {
		return alias
	}
	return strings.TrimSpace(keywords) + ", " + alias
}
