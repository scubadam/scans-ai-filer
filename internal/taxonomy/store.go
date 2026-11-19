package taxonomy

import (
	"context"
	"database/sql"
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
// semantic few-shot retrieval and self-learning.
type FilingExample struct {
	Category     string
	Subcategory  string
	Organization string
	DocumentType string
	Summary      string
	FolderPath   string
}

// SearchResult holds the top-K semantically relevant taxonomy rules and past filing examples.
type SearchResult struct {
	Rules    []Rule
	Examples []FilingExample
}

// StoreStats summarizes what is currently stored in the taxonomy database.
type StoreStats struct {
	RuleCount    int
	HistoryCount int
	EmbeddedDocs int
}

// Store abstracts persistent taxonomy rules, filing history, and semantic vector retrieval.
type Store interface {
	Close() error
	UpsertRule(ctx context.Context, rule Rule, source string) error
	SeedFromRules(ctx context.Context, rules []Rule, source string) (int, error)
	RecordFiling(ctx context.Context, ex FilingExample) error
	ListRules(ctx context.Context) ([]Rule, error)
	Search(ctx context.Context, queryText string, topKRules, topKExamples int) (SearchResult, error)
	Stats(ctx context.Context) (StoreStats, error)
}

// SQLiteStore is a zero-sidecar, CGO-free SQLite + pure-Go exact cosine similarity vector store.
type SQLiteStore struct {
	db       *sql.DB
	embedder embed.Provider
}

// OpenSQLiteStore opens (or creates) a pure-Go SQLite taxonomy database at dbPath.
// embedder may be nil (disables vector embeddings and falls back to keyword/full-list retrieval).
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
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	norm_key      TEXT NOT NULL UNIQUE,
	category      TEXT NOT NULL,
	subcategory   TEXT NOT NULL DEFAULT '',
	organization  TEXT NOT NULL,
	keywords      TEXT NOT NULL DEFAULT '',
	path_template TEXT NOT NULL DEFAULT '',
	description   TEXT NOT NULL DEFAULT '',
	source        TEXT NOT NULL DEFAULT 'csv',
	embed_text    TEXT NOT NULL DEFAULT '',
	embed_model   TEXT NOT NULL DEFAULT '',
	embedding     BLOB,
	updated_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS filing_history (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	dedupe_key    TEXT NOT NULL UNIQUE,
	category      TEXT NOT NULL,
	subcategory   TEXT NOT NULL DEFAULT '',
	organization  TEXT NOT NULL,
	doc_type      TEXT NOT NULL DEFAULT '',
	summary       TEXT NOT NULL DEFAULT '',
	folder_path   TEXT NOT NULL DEFAULT '',
	embed_model   TEXT NOT NULL DEFAULT '',
	embedding     BLOB,
	created_at    TEXT NOT NULL
);
`
	_, err := s.db.Exec(schema)
	return err
}

func ruleNormKey(r Rule) string {
	return NormalizeName(r.Category) + "|" + NormalizeName(r.Organization)
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
	if strings.TrimSpace(r.Category) == "" || strings.TrimSpace(r.Organization) == "" {
		return nil
	}
	if source == "" {
		source = "csv"
	}
	normKey := ruleNormKey(r)
	text := ruleEmbedText(r)
	modelName := ""
	if s.embedder != nil {
		modelName = s.embedder.Name()
	}

	// Check if an existing embedding for the exact same text & model already exists
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
INSERT INTO taxonomy_rules (norm_key, category, subcategory, organization, keywords, path_template, description, source, embed_text, embed_model, embedding, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(norm_key) DO UPDATE SET
	category      = excluded.category,
	subcategory   = CASE WHEN excluded.subcategory != '' THEN excluded.subcategory ELSE taxonomy_rules.subcategory END,
	organization  = excluded.organization,
	keywords      = CASE WHEN excluded.keywords != '' THEN excluded.keywords ELSE taxonomy_rules.keywords END,
	path_template = CASE WHEN excluded.path_template != '' THEN excluded.path_template ELSE taxonomy_rules.path_template END,
	description   = CASE WHEN excluded.description != '' THEN excluded.description ELSE taxonomy_rules.description END,
	source        = excluded.source,
	embed_text    = excluded.embed_text,
	embed_model   = excluded.embed_model,
	embedding     = excluded.embedding,
	updated_at    = excluded.updated_at
`, normKey, r.Category, r.Subcategory, r.Organization, r.Keywords, r.PathTemplate, r.Description, source, text, modelName, vecBlob, now)
	return err
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
	dedupeKey := strings.ToLower(strings.TrimSpace(ex.Organization)) + "|" +
		strings.ToLower(strings.TrimSpace(ex.DocumentType)) + "|" +
		strings.ToLower(strings.TrimSpace(ex.Summary)) + "|" +
		strings.ToLower(strings.TrimSpace(ex.FolderPath))

	modelName := ""
	var vecBlob []byte
	if s.embedder != nil {
		modelName = s.embedder.Name()
		// Skip re-embedding if exact filing example already exists for this model
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
INSERT INTO filing_history (dedupe_key, category, subcategory, organization, doc_type, summary, folder_path, embed_model, embedding, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(dedupe_key) DO UPDATE SET
	embed_model = excluded.embed_model,
	embedding   = excluded.embedding,
	created_at  = excluded.created_at
`, dedupeKey, ex.Category, ex.Subcategory, ex.Organization, ex.DocumentType, ex.Summary, ex.FolderPath, modelName, vecBlob, now)
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

func (s *SQLiteStore) Search(ctx context.Context, queryText string, topKRules, topKExamples int) (SearchResult, error) {
	if topKRules <= 0 {
		topKRules = 8
	}
	if topKExamples < 0 {
		topKExamples = 0
	}

	// If no embedder is configured, return all rules up to topKRules (or full list)
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

	type scoredRule struct {
		rule  Rule
		score float32
	}
	var scoredRules []scoredRule
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
			scoredRules = append(scoredRules, scoredRule{rule: r, score: embed.CosineSimilarity(queryVec, vec)})
		}
	}
	_ = rows.Close()

	sort.Slice(scoredRules, func(i, j int) bool { return scoredRules[i].score > scoredRules[j].score })
	if len(scoredRules) > topKRules {
		scoredRules = scoredRules[:topKRules]
	}
	res := SearchResult{Rules: make([]Rule, 0, len(scoredRules))}
	for _, sr := range scoredRules {
		res.Rules = append(res.Rules, sr.rule)
	}

	if topKExamples > 0 {
		type scoredEx struct {
			ex    FilingExample
			score float32
		}
		var scoredExs []scoredEx
		hrows, err := s.db.QueryContext(ctx, `
SELECT category, subcategory, organization, doc_type, summary, folder_path, embedding
FROM filing_history
WHERE embedding IS NOT NULL AND length(embedding) > 0
`)
		if err != nil {
			return res, err
		}
		for hrows.Next() {
			var ex FilingExample
			var blob []byte
			if err := hrows.Scan(&ex.Category, &ex.Subcategory, &ex.Organization, &ex.DocumentType, &ex.Summary, &ex.FolderPath, &blob); err != nil {
				_ = hrows.Close()
				return res, err
			}
			vec := embed.DecodeVector(blob)
			if len(vec) == len(queryVec) {
				sim := embed.CosineSimilarity(queryVec, vec)
				if sim >= 0.35 {
					scoredExs = append(scoredExs, scoredEx{ex: ex, score: sim})
				}
			}
		}
		_ = hrows.Close()

		sort.Slice(scoredExs, func(i, j int) bool { return scoredExs[i].score > scoredExs[j].score })
		if len(scoredExs) > topKExamples {
			scoredExs = scoredExs[:topKExamples]
		}
		for _, se := range scoredExs {
			res.Examples = append(res.Examples, se.ex)
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
