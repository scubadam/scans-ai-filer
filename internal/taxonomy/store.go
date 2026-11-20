package taxonomy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/scubadam/scans-ai-filer/internal/embed"
	_ "modernc.org/sqlite"
)

// FilingExample is a strictly PII-free record of a previously filed document used for
// semantic few-shot retrieval, confidence tracking, and self-learning.
type FilingExample struct {
	Category         string  `json:"category"`
	Subcategory      string  `json:"subcategory"`
	Organization     string  `json:"organization"`
	DocumentType     string  `json:"document_type"`
	Summary          string  `json:"summary"`
	FolderPath       string  `json:"folder_path"`
	Confidence       float64 `json:"confidence"`
	ConfidenceReason string  `json:"confidence_reason"`
	Reviewed         bool    `json:"reviewed"`
}

// RuleRecord is a full database row for a taxonomy rule (exposed by the PWA API).
type RuleRecord struct {
	ID            int64   `json:"id"`
	Category      string  `json:"category"`
	Subcategory   string  `json:"subcategory"`
	Organization  string  `json:"organization"`
	Keywords      string  `json:"keywords"`
	PathTemplate  string  `json:"path_template"`
	Description   string  `json:"description"`
	Source        string  `json:"source"`
	Confidence    float64 `json:"confidence"`
	EvidenceCount int     `json:"evidence_count"`
	Confirmed     bool    `json:"confirmed"`
	UpdatedAt     string  `json:"updated_at"`
}

// HistoryRecord is a full database row for a PII-free filing history entry.
type HistoryRecord struct {
	ID               int64   `json:"id"`
	Category         string  `json:"category"`
	Subcategory      string  `json:"subcategory"`
	Organization     string  `json:"organization"`
	DocumentType     string  `json:"document_type"`
	Summary          string  `json:"summary"`
	FolderPath       string  `json:"folder_path"`
	Confidence       float64 `json:"confidence"`
	ConfidenceReason string  `json:"confidence_reason"`
	Reviewed         bool    `json:"reviewed"`
	CreatedAt        string  `json:"created_at"`
}

// ScoredRule pairs a taxonomy rule with its cosine similarity score.
type ScoredRule struct {
	Rule  Rule    `json:"rule"`
	Score float32 `json:"score"`
}

// ScoredExample pairs a past filing example with its cosine similarity score.
type ScoredExample struct {
	Example FilingExample `json:"example"`
	Score   float32       `json:"score"`
}

// SearchResult holds the top-K semantically relevant taxonomy rules and past filing examples.
type SearchResult struct {
	Rules          []Rule          `json:"rules"`
	Examples       []FilingExample `json:"examples"`
	ScoredRules    []ScoredRule    `json:"scored_rules,omitempty"`
	ScoredExamples []ScoredExample `json:"scored_examples,omitempty"`
}

// StoreStats summarizes what is currently stored in the taxonomy database.
type StoreStats struct {
	RuleCount          int `json:"rule_count"`
	HistoryCount       int `json:"history_count"`
	EmbeddedDocs       int `json:"embedded_docs"`
	UnconfirmedRules   int `json:"unconfirmed_rules"`
	LowConfUnreviewed  int `json:"low_conf_unreviewed"`
}

// Store abstracts persistent taxonomy rules, filing history, and semantic vector retrieval.
type Store interface {
	Close() error
	UpsertRule(ctx context.Context, rule Rule, source string) error
	DeleteRule(ctx context.Context, category, organization string) error
	SeedFromRules(ctx context.Context, rules []Rule, source string) (int, error)
	RecordFiling(ctx context.Context, ex FilingExample) error
	ListRules(ctx context.Context) ([]Rule, error)
	Search(ctx context.Context, queryText string, topKRules, topKExamples int) (SearchResult, error)
	Stats(ctx context.Context) (StoreStats, error)
}

// AdminStore extends Store with CRUD, merge, review-queue, and export capabilities used by the Web UI.
type AdminStore interface {
	Store
	UpsertRuleWithConfidence(ctx context.Context, rule Rule, source string, confidence float64, confirmed bool) error
	ListRuleRecords(ctx context.Context) ([]RuleRecord, error)
	UpdateRuleByID(ctx context.Context, id int64, rule Rule, confirmed bool) error
	DeleteRuleByID(ctx context.Context, id int64) error
	MergeRules(ctx context.Context, sourceID, targetID int64) error
	ListHistoryRecords(ctx context.Context, limit int) ([]HistoryRecord, error)
	MarkHistoryReviewed(ctx context.Context, id int64) error
	ExportCSV(ctx context.Context) ([]byte, error)
}

// SQLiteStore is a zero-sidecar, CGO-free SQLite + pure-Go exact cosine similarity vector store.
type SQLiteStore struct {
	db       *sql.DB
	embedder embed.Provider
}

// OpenSQLiteStore opens (or creates) a pure-Go SQLite taxonomy database at dbPath.
// embedder may be nil (disables vector embeddings and falls back to full-list retrieval).
func OpenSQLiteStore(dbPath string, embedder embed.Provider) (*SQLiteStore, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("taxonomy db path must not be empty")
	}
	if dbPath != ":memory:" && !strings.HasPrefix(dbPath, "file:") {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, fmt.Errorf("create taxonomy db directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite taxonomy db: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &SQLiteStore{db: db, embedder: embedder}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) migrate() error {
	const schema = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;

CREATE TABLE IF NOT EXISTS taxonomy_rules (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	norm_key       TEXT NOT NULL UNIQUE,
	category       TEXT NOT NULL,
	subcategory    TEXT NOT NULL DEFAULT '',
	organization   TEXT NOT NULL,
	keywords       TEXT NOT NULL DEFAULT '',
	path_template  TEXT NOT NULL DEFAULT '',
	description    TEXT NOT NULL DEFAULT '',
	source         TEXT NOT NULL DEFAULT 'csv',
	confidence     REAL NOT NULL DEFAULT 1.0,
	evidence_count INTEGER NOT NULL DEFAULT 1,
	confirmed      INTEGER NOT NULL DEFAULT 1,
	embed_text     TEXT NOT NULL DEFAULT '',
	embed_model    TEXT NOT NULL DEFAULT '',
	embedding      BLOB,
	updated_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS filing_history (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	dedupe_key        TEXT NOT NULL UNIQUE,
	category          TEXT NOT NULL,
	subcategory       TEXT NOT NULL DEFAULT '',
	organization      TEXT NOT NULL,
	doc_type          TEXT NOT NULL DEFAULT '',
	summary           TEXT NOT NULL DEFAULT '',
	folder_path       TEXT NOT NULL DEFAULT '',
	confidence        REAL NOT NULL DEFAULT 1.0,
	confidence_reason TEXT NOT NULL DEFAULT '',
	reviewed          INTEGER NOT NULL DEFAULT 0,
	embed_model       TEXT NOT NULL DEFAULT '',
	embedding         BLOB,
	created_at        TEXT NOT NULL
);
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Safe additive migrations for existing v0.4.x–v0.7.x SQLite databases
	alters := []string{
		`ALTER TABLE taxonomy_rules ADD COLUMN confidence REAL NOT NULL DEFAULT 1.0`,
		`ALTER TABLE taxonomy_rules ADD COLUMN evidence_count INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE taxonomy_rules ADD COLUMN confirmed INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE filing_history ADD COLUMN confidence REAL NOT NULL DEFAULT 1.0`,
		`ALTER TABLE filing_history ADD COLUMN confidence_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE filing_history ADD COLUMN reviewed INTEGER NOT NULL DEFAULT 0`,
	}
	for _, stmt := range alters {
		if _, err := s.db.Exec(stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	return nil
}

func ruleNormKey(r Rule) string {
	return NormalizeName(r.Category) + "|" + NormalizeName(r.Subcategory) + "|" + NormalizeName(r.Organization)
}

func legacyRuleNormKey(category, organization string) string {
	return NormalizeName(category) + "|" + NormalizeName(organization)
}

func ruleEmbedText(r Rule) string {
	parts := []string{r.Organization, r.Category, r.Subcategory, r.Keywords, r.Description}
	var nonEmpty []string
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}
	return strings.Join(nonEmpty, " | ")
}

func exampleEmbedText(ex FilingExample) string {
	parts := []string{ex.Organization, ex.DocumentType, ex.Category, ex.Subcategory, ex.Summary}
	var nonEmpty []string
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}
	return strings.Join(nonEmpty, " | ")
}

func (s *SQLiteStore) UpsertRule(ctx context.Context, r Rule, source string) error {
	confirmed := source != "learned"
	conf := 1.0
	if !confirmed {
		conf = 0.75
	}
	return s.UpsertRuleWithConfidence(ctx, r, source, conf, confirmed)
}

func (s *SQLiteStore) UpsertRuleWithConfidence(ctx context.Context, r Rule, source string, confidence float64, confirmed bool) error {
	if strings.TrimSpace(r.Category) == "" || strings.TrimSpace(r.Organization) == "" {
		return nil
	}
	if source == "" {
		source = "csv"
	}
	if confidence <= 0 || confidence > 1 {
		if confirmed {
			confidence = 1.0
		} else {
			confidence = 0.7
		}
	}
	confirmedInt := 0
	if confirmed {
		confirmedInt = 1
	}

	normKey := ruleNormKey(r)
	text := ruleEmbedText(r)
	modelName := ""
	if s.embedder != nil {
		modelName = s.embedder.Name()
	}

	var existingText, existingModel string
	var existingBlob []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT embed_text, embed_model, embedding FROM taxonomy_rules WHERE norm_key = ?`, normKey,
	).Scan(&existingText, &existingModel, &existingBlob)

	var vecBlob []byte
	if err == nil && existingText == text && existingModel == modelName && len(existingBlob) > 0 {
		vecBlob = existingBlob
	} else if s.embedder != nil {
		vec, embErr := s.embedder.Embed(ctx, text)
		if embErr != nil {
			return fmt.Errorf("embed taxonomy rule %q: %w", r.Organization, embErr)
		}
		vecBlob = embed.EncodeVector(vec)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `
INSERT INTO taxonomy_rules (norm_key, category, subcategory, organization, keywords, path_template, description, source, confidence, evidence_count, confirmed, embed_text, embed_model, embedding, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)
ON CONFLICT(norm_key) DO UPDATE SET
	category       = excluded.category,
	subcategory    = CASE WHEN excluded.subcategory != '' THEN excluded.subcategory ELSE taxonomy_rules.subcategory END,
	organization   = excluded.organization,
	keywords       = CASE WHEN excluded.keywords != '' THEN excluded.keywords ELSE taxonomy_rules.keywords END,
	path_template  = CASE WHEN excluded.path_template != '' THEN excluded.path_template ELSE taxonomy_rules.path_template END,
	description    = CASE WHEN excluded.description != '' THEN excluded.description ELSE taxonomy_rules.description END,
	source         = excluded.source,
	confidence     = MAX(taxonomy_rules.confidence, excluded.confidence),
	evidence_count = taxonomy_rules.evidence_count + 1,
	confirmed      = MAX(taxonomy_rules.confirmed, excluded.confirmed),
	embed_text     = excluded.embed_text,
	embed_model    = excluded.embed_model,
	embedding      = excluded.embedding,
	updated_at     = excluded.updated_at
`, normKey, r.Category, r.Subcategory, r.Organization, r.Keywords, r.PathTemplate, r.Description, source, confidence, confirmedInt, text, modelName, vecBlob, now)
	return err
}

func (s *SQLiteStore) DeleteRule(ctx context.Context, category, organization string) error {
	legKey := legacyRuleNormKey(category, organization)
	normCat := NormalizeName(category)
	normOrg := NormalizeName(organization)
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM taxonomy_rules WHERE norm_key = ? OR (lower(category) = lower(?) AND lower(organization) = lower(?)) OR norm_key LIKE ?`,
		legKey, category, organization, normCat+"|%|"+normOrg,
	)
	return err
}

func (s *SQLiteStore) DeleteRuleByID(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM taxonomy_rules WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) UpdateRuleByID(ctx context.Context, id int64, r Rule, confirmed bool) error {
	if strings.TrimSpace(r.Category) == "" || strings.TrimSpace(r.Organization) == "" {
		return fmt.Errorf("category and organization are required")
	}
	normKey := ruleNormKey(r)
	text := ruleEmbedText(r)
	modelName := ""
	var vecBlob []byte
	if s.embedder != nil {
		modelName = s.embedder.Name()
		vec, err := s.embedder.Embed(ctx, text)
		if err != nil {
			return fmt.Errorf("embed updated rule %q: %w", r.Organization, err)
		}
		vecBlob = embed.EncodeVector(vec)
	}
	confirmedInt := 0
	conf := 0.8
	if confirmed {
		confirmedInt = 1
		conf = 1.0
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
UPDATE taxonomy_rules
SET norm_key = ?, category = ?, subcategory = ?, organization = ?, keywords = ?,
    path_template = ?, description = ?, confirmed = ?, confidence = MAX(confidence, ?),
    embed_text = ?, embed_model = ?, embedding = ?, updated_at = ?
WHERE id = ?
`, normKey, r.Category, r.Subcategory, r.Organization, r.Keywords, r.PathTemplate, r.Description,
		confirmedInt, conf, text, modelName, vecBlob, now, id)
	return err
}

func (s *SQLiteStore) MergeRules(ctx context.Context, sourceID, targetID int64) error {
	if sourceID == targetID {
		return fmt.Errorf("cannot merge a rule into itself")
	}
	var srcOrg, srcKW string
	var srcEv int
	if err := s.db.QueryRowContext(ctx,
		`SELECT organization, keywords, evidence_count FROM taxonomy_rules WHERE id = ?`, sourceID,
	).Scan(&srcOrg, &srcKW, &srcEv); err != nil {
		return fmt.Errorf("load source rule %d: %w", sourceID, err)
	}

	var tgt Rule
	if err := s.db.QueryRowContext(ctx,
		`SELECT category, subcategory, organization, keywords, path_template, description FROM taxonomy_rules WHERE id = ?`, targetID,
	).Scan(&tgt.Category, &tgt.Subcategory, &tgt.Organization, &tgt.Keywords, &tgt.PathTemplate, &tgt.Description); err != nil {
		return fmt.Errorf("load target rule %d: %w", targetID, err)
	}

	// Combine keywords preserving source organization as an alias
	kwSet := make(map[string]struct{})
	var merged []string
	addKW := func(raw string) {
		for _, k := range strings.Split(raw, ",") {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			low := strings.ToLower(k)
			if _, ok := kwSet[low]; !ok {
				kwSet[low] = struct{}{}
				merged = append(merged, k)
			}
		}
	}
	addKW(tgt.Keywords)
	addKW(srcOrg)
	addKW(srcKW)
	tgt.Keywords = strings.Join(merged, ", ")

	if err := s.UpdateRuleByID(ctx, targetID, tgt, true); err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE taxonomy_rules SET evidence_count = evidence_count + ? WHERE id = ?`, srcEv, targetID)
	return s.DeleteRuleByID(ctx, sourceID)
}

func (s *SQLiteStore) SeedFromRules(ctx context.Context, rules []Rule, source string) (int, error) {
	count := 0
	for _, r := range rules {
		if err := s.UpsertRule(ctx, r, source); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *SQLiteStore) RecordFiling(ctx context.Context, ex FilingExample) error {
	if strings.TrimSpace(ex.Organization) == "" || strings.TrimSpace(ex.Category) == "" {
		return nil
	}
	conf := ex.Confidence
	if conf <= 0 || conf > 1 {
		conf = 0.85
	}
	revInt := 0
	if ex.Reviewed {
		revInt = 1
	}

	dedupeKey := strings.ToLower(strings.TrimSpace(ex.Organization)) + "|" +
		strings.ToLower(strings.TrimSpace(ex.DocumentType)) + "|" +
		strings.ToLower(strings.TrimSpace(ex.Summary)) + "|" +
		strings.ToLower(strings.TrimSpace(ex.FolderPath))

	modelName := ""
	var vecBlob []byte
	if s.embedder != nil {
		modelName = s.embedder.Name()
		var existingModel string
		var existingBlob []byte
		err := s.db.QueryRowContext(ctx,
			`SELECT embed_model, embedding FROM filing_history WHERE dedupe_key = ?`, dedupeKey,
		).Scan(&existingModel, &existingBlob)
		if err == nil && existingModel == modelName && len(existingBlob) > 0 {
			vecBlob = existingBlob
		} else {
			vec, embErr := s.embedder.Embed(ctx, exampleEmbedText(ex))
			if embErr != nil {
				return fmt.Errorf("embed filing history %q: %w", ex.Organization, embErr)
			}
			vecBlob = embed.EncodeVector(vec)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO filing_history (dedupe_key, category, subcategory, organization, doc_type, summary, folder_path, confidence, confidence_reason, reviewed, embed_model, embedding, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(dedupe_key) DO UPDATE SET
	confidence        = excluded.confidence,
	confidence_reason = CASE WHEN excluded.confidence_reason != '' THEN excluded.confidence_reason ELSE filing_history.confidence_reason END,
	embed_model       = excluded.embed_model,
	embedding         = excluded.embedding,
	created_at        = excluded.created_at
`, dedupeKey, ex.Category, ex.Subcategory, ex.Organization, ex.DocumentType, ex.Summary, ex.FolderPath, conf, ex.ConfidenceReason, revInt, modelName, vecBlob, now)
	return err
}

func (s *SQLiteStore) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT category, subcategory, organization, keywords, path_template, description
FROM taxonomy_rules
ORDER BY category, subcategory, organization
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.Category, &r.Subcategory, &r.Organization, &r.Keywords, &r.PathTemplate, &r.Description); err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

func (s *SQLiteStore) ListRuleRecords(ctx context.Context) ([]RuleRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, category, subcategory, organization, keywords, path_template, description, source, confidence, evidence_count, confirmed, updated_at
FROM taxonomy_rules
ORDER BY confirmed ASC, confidence ASC, category ASC, organization ASC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RuleRecord
	for rows.Next() {
		var r RuleRecord
		var confInt int
		if err := rows.Scan(&r.ID, &r.Category, &r.Subcategory, &r.Organization, &r.Keywords, &r.PathTemplate, &r.Description, &r.Source, &r.Confidence, &r.EvidenceCount, &confInt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Confirmed = confInt != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListHistoryRecords(ctx context.Context, limit int) ([]HistoryRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, category, subcategory, organization, doc_type, summary, folder_path, confidence, confidence_reason, reviewed, created_at
FROM filing_history
ORDER BY reviewed ASC, confidence ASC, id DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []HistoryRecord
	for rows.Next() {
		var h HistoryRecord
		var revInt int
		if err := rows.Scan(&h.ID, &h.Category, &h.Subcategory, &h.Organization, &h.DocumentType, &h.Summary, &h.FolderPath, &h.Confidence, &h.ConfidenceReason, &revInt, &h.CreatedAt); err != nil {
			return nil, err
		}
		h.Reviewed = revInt != 0
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) MarkHistoryReviewed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE filing_history SET reviewed = 1 WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) ExportCSV(ctx context.Context) ([]byte, error) {
	rules, err := s.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"Category", "Subcategory", "Organization", "Keywords", "Path_Template", "Description"})
	for _, r := range rules {
		_ = w.Write([]string{r.Category, r.Subcategory, r.Organization, r.Keywords, r.PathTemplate, r.Description})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func (s *SQLiteStore) Search(ctx context.Context, queryText string, topKRules, topKExamples int) (SearchResult, error) {
	if topKRules <= 0 {
		topKRules = 8
	}
	if topKExamples < 0 {
		topKExamples = 0
	}

	if s.embedder == nil || strings.TrimSpace(queryText) == "" {
		rules, err := s.ListRules(ctx)
		if err != nil {
			return SearchResult{}, err
		}
		return SearchResult{Rules: rules}, nil
	}

	queryVec, err := s.embedder.Embed(ctx, queryText)
	if err != nil {
		return SearchResult{}, fmt.Errorf("embed search query: %w", err)
	}

	var scoredRules []ScoredRule
	rows, err := s.db.QueryContext(ctx, `
SELECT category, subcategory, organization, keywords, path_template, description, embedding
FROM taxonomy_rules
WHERE embedding IS NOT NULL AND length(embedding) > 0
`)
	if err != nil {
		return SearchResult{}, err
	}
	for rows.Next() {
		var r Rule
		var blob []byte
		if err := rows.Scan(&r.Category, &r.Subcategory, &r.Organization, &r.Keywords, &r.PathTemplate, &r.Description, &blob); err != nil {
			_ = rows.Close()
			return SearchResult{}, err
		}
		vec := embed.DecodeVector(blob)
		if len(vec) == len(queryVec) {
			scoredRules = append(scoredRules, ScoredRule{Rule: r, Score: embed.CosineSimilarity(queryVec, vec)})
		}
	}
	_ = rows.Close()

	sort.Slice(scoredRules, func(i, j int) bool { return scoredRules[i].Score > scoredRules[j].Score })
	if len(scoredRules) > topKRules {
		scoredRules = scoredRules[:topKRules]
	}
	res := SearchResult{
		Rules:       make([]Rule, 0, len(scoredRules)),
		ScoredRules: scoredRules,
	}
	for _, sr := range scoredRules {
		res.Rules = append(res.Rules, sr.Rule)
	}

	if topKExamples > 0 {
		var scoredExs []ScoredExample
		hrows, err := s.db.QueryContext(ctx, `
SELECT category, subcategory, organization, doc_type, summary, folder_path, confidence, confidence_reason, reviewed, embedding
FROM filing_history
WHERE embedding IS NOT NULL AND length(embedding) > 0
`)
		if err != nil {
			return res, err
		}
		for hrows.Next() {
			var ex FilingExample
			var revInt int
			var blob []byte
			if err := hrows.Scan(&ex.Category, &ex.Subcategory, &ex.Organization, &ex.DocumentType, &ex.Summary, &ex.FolderPath, &ex.Confidence, &ex.ConfidenceReason, &revInt, &blob); err != nil {
				_ = hrows.Close()
				return res, err
			}
			ex.Reviewed = revInt != 0
			vec := embed.DecodeVector(blob)
			if len(vec) == len(queryVec) {
				sim := embed.CosineSimilarity(queryVec, vec)
				if sim >= 0.35 {
					scoredExs = append(scoredExs, ScoredExample{Example: ex, Score: sim})
				}
			}
		}
		_ = hrows.Close()

		sort.Slice(scoredExs, func(i, j int) bool { return scoredExs[i].Score > scoredExs[j].Score })
		if len(scoredExs) > topKExamples {
			scoredExs = scoredExs[:topKExamples]
		}
		res.ScoredExamples = scoredExs
		for _, se := range scoredExs {
			res.Examples = append(res.Examples, se.Example)
		}
	}

	return res, nil
}

func (s *SQLiteStore) Stats(ctx context.Context) (StoreStats, error) {
	var st StoreStats
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM taxonomy_rules`).Scan(&st.RuleCount); err != nil {
		return st, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM filing_history`).Scan(&st.HistoryCount); err != nil {
		return st, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM taxonomy_rules WHERE embedding IS NOT NULL AND length(embedding) > 0`).Scan(&st.EmbeddedDocs); err != nil {
		return st, err
	}
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM taxonomy_rules WHERE confirmed = 0`).Scan(&st.UnconfirmedRules)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM filing_history WHERE reviewed = 0 AND confidence < 0.75`).Scan(&st.LowConfUnreviewed)
	return st, nil
}

// FormatExamplesForPrompt renders semantically similar past PII-free filings as few-shot guidance.
func FormatExamplesForPrompt(examples []FilingExample) string {
	if len(examples) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("SIMILAR PAST FILINGS (use as consistency reference for category, organization, and folder structure):\n")
	for _, ex := range examples {
		fmt.Fprintf(&b, "- [%s > %s] Organization: %q | Type: %q | Summary: %q | Folder: %q\n",
			ex.Category, ex.Subcategory, ex.Organization, ex.DocumentType, ex.Summary, ex.FolderPath)
	}
	return strings.TrimRight(b.String(), "\n")
}
