package filer

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/scubadam/scans-ai-filer/internal/ai"
	"github.com/scubadam/scans-ai-filer/internal/notify"
	"github.com/scubadam/scans-ai-filer/internal/pdfenc"
	"github.com/scubadam/scans-ai-filer/internal/storage"
	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

var (
	dateInFilenameRe      = regexp.MustCompile(`(\d{4})[-_]?(\d{2})[-_]?(\d{2})`)
	leadingShortDateRe    = regexp.MustCompile(`^\d{1,2}[-/]\d{1,2}[-/]\d{2,4}(\s+-\s+)`)
	leadingTemplateDateRe = regexp.MustCompile(`^(?i)(?:yyyy-mm-dd|\{date\})(\s+-\s+)`)
)

// Options configures a batch run of Filer.
type Options struct {
	ScansFolderName  string
	BatchSize        int
	PauseDuration    time.Duration
	DryRun           bool
	PDFPassword      string
	PDFOwnerPassword string
	Taxonomy         []taxonomy.Rule
}

// Result summarizes the outcome of a batch run.
type Result struct {
	Succeeded int
	Failed    int
	Skipped   int
	Filed     []notify.FiledItem
}

// Filer orchestrates downloading, AI classification, metadata sanitization, and filing.
type Filer struct {
	Storage       storage.Provider
	AI            ai.Provider
	Notifier      notify.Notifier
	TaxonomyStore taxonomy.Store
	Logger        *slog.Logger
	Now           func() time.Time
}

// Run executes a single filing batch.
func (f *Filer) Run(ctx context.Context, opts Options) (*Result, error) {
	if f.Logger == nil {
		f.Logger = slog.Default()
	}
	if f.Now == nil {
		f.Now = time.Now
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 10
	}

	start := f.Now()
	f.Logger.Info("starting scans-ai-filer batch",
		"storage", f.Storage.Name(),
		"ai", f.AI.Name(),
		"batch_size", opts.BatchSize,
		"taxonomy_rules", len(opts.Taxonomy),
		"pdf_encryption", opts.PDFPassword != "",
		"dry_run", opts.DryRun,
	)

	if err := f.Storage.Init(ctx); err != nil {
		return nil, fmt.Errorf("initialize storage %q: %w", f.Storage.Name(), err)
	}

	files, err := f.Storage.ListInbox(ctx, opts.BatchSize)
	if err != nil {
		return nil, fmt.Errorf("list inbox: %w", err)
	}
	if len(files) == 0 {
		f.Logger.Info("inbox is empty; nothing to process")
		return &Result{}, nil
	}

	f.Logger.Info("found inbox files to process", "count", len(files))

	res := &Result{}
	for idx, item := range files {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}

		f.Logger.Info("processing file",
			"index", idx+1,
			"total", len(files),
			"name", item.Name,
			"id", item.ID,
			"size_bytes", item.Size,
		)

		filedItem, skipped, err := f.processOne(ctx, item, opts)
		if err != nil {
			f.Logger.Error("failed to process file", "name", item.Name, "id", item.ID, "err", err)
			res.Failed++
		} else if skipped {
			res.Skipped++
		} else {
			res.Succeeded++
			res.Filed = append(res.Filed, filedItem)
		}

		if opts.PauseDuration > 0 && idx < len(files)-1 {
			t := time.NewTimer(opts.PauseDuration)
			select {
			case <-ctx.Done():
				t.Stop()
				return res, ctx.Err()
			case <-t.C:
			}
		}
	}

	f.Logger.Info("batch complete",
		"duration", f.Now().Sub(start).Round(time.Millisecond),
		"succeeded", res.Succeeded,
		"failed", res.Failed,
		"skipped", res.Skipped,
	)

	if f.Notifier != nil && !opts.DryRun && (res.Succeeded > 0 || res.Failed > 0) {
		if err := f.Notifier.Send(ctx, res.Filed, res.Failed); err != nil {
			f.Logger.Warn("failed to send batch notification", "err", err)
		}
	}

	if res.Failed > 0 && res.Succeeded == 0 {
		return res, fmt.Errorf("all %d document(s) in batch failed", res.Failed)
	}
	return res, nil
}

func (f *Filer) processOne(ctx context.Context, item storage.InboxFile, opts Options) (notify.FiledItem, bool, error) {
	inInbox, err := f.Storage.StillInInbox(ctx, item.ID)
	if err != nil {
		return notify.FiledItem{}, false, fmt.Errorf("check inbox membership: %w", err)
	}
	if !inInbox {
		f.Logger.Info("file already moved out of inbox; skipping", "name", item.Name)
		return notify.FiledItem{}, true, nil
	}

	data, err := f.Storage.Download(ctx, item.ID)
	if err != nil {
		return notify.FiledItem{}, false, fmt.Errorf("download: %w", err)
	}

	fallbackExt := filepath.Ext(item.Name)
	if fallbackExt == "" {
		if item.MimeType == "application/pdf" {
			fallbackExt = ".pdf"
		} else {
			fallbackExt = ".jpg"
		}
	}
	fallbackDate := deriveFallbackDate(item, f.Now())

	analysis, err := f.AI.AnalyzeDocument(ctx, ai.DocumentInput{
		Bytes:            data,
		MimeType:         item.MimeType,
		OriginalFilename: item.Name,
		FallbackExt:      fallbackExt,
		FallbackDate:     fallbackDate,
		Taxonomy:         opts.Taxonomy,
	})
	if err != nil {
		return notify.FiledItem{}, false, fmt.Errorf("AI analysis: %w", err)
	}

	docDate := normalizeDocumentDate(analysis.DocumentDate, fallbackDate)
	docYear := docDate[:4]

	rawFilename := strings.TrimSpace(analysis.SuggestedFilename)
	if rawFilename == "" {
		rawFilename = item.Name
	}
	// Replace non-ISO leading dates (e.g. "13-03-40 - ...") or literal placeholders ("YYYY-MM-DD - ...") with normalized YYYY-MM-DD
	if leadingShortDateRe.MatchString(rawFilename) {
		rawFilename = leadingShortDateRe.ReplaceAllString(rawFilename, docDate+"$1")
	} else if leadingTemplateDateRe.MatchString(rawFilename) {
		rawFilename = leadingTemplateDateRe.ReplaceAllString(rawFilename, docDate+"$1")
	}
	encryptEnabled := strings.TrimSpace(opts.PDFPassword) != ""
	if encryptEnabled {
		// Encrypted files are always stored as AES-256 PDFs (images are wrapped into a single-page PDF)
		base, _ := taxonomy.SplitExt(taxonomy.SanitizeFilename(rawFilename, ".pdf"))
		rawFilename = base + ".pdf"
		fallbackExt = ".pdf"
	}
	cleanFilename := taxonomy.SanitizeFilename(rawFilename, fallbackExt)

	targetFolderPath := sanitizeFolderPath(analysis.SuggestedFolderPath, docYear)

	category := defaultString(analysis.Category, "General")
	subcategory := strings.TrimSpace(analysis.Subcategory)
	organization := defaultString(analysis.Organization, "Unknown")
	docType := defaultString(analysis.DocumentType, "Document")
	summary := strings.TrimSpace(analysis.Summary)

	description, props := buildSanitizedMetadata(category, subcategory, organization, docType, docDate, summary, f.AI.Name(), analysis.IsTaxonomyMatch, encryptEnabled)

	f.Logger.Info("classified document",
		"taxonomy_match", analysis.IsTaxonomyMatch,
		"new_name", cleanFilename,
		"target_dir", opts.ScansFolderName+"/"+targetFolderPath,
		"summary", summary,
		"encrypted", encryptEnabled,
	)

	if opts.DryRun {
		f.Logger.Info("dry-run enabled; skipping storage mutation", "name", cleanFilename)
		return notify.FiledItem{
			Filename: cleanFilename,
			Folder:   opts.ScansFolderName + "/" + targetFolderPath,
		}, false, nil
	}

	var encryptedBytes []byte
	if encryptEnabled {
		enc, err := pdfenc.EncryptDocument(ctx, data, item.MimeType, opts.PDFPassword, opts.PDFOwnerPassword)
		if err != nil {
			return notify.FiledItem{}, false, fmt.Errorf("encrypt document: %w", err)
		}
		encryptedBytes = enc
	}

	destFolderID, err := f.Storage.EnsureFolderPath(ctx, targetFolderPath)
	if err != nil {
		return notify.FiledItem{}, false, fmt.Errorf("ensure folder path %q: %w", targetFolderPath, err)
	}

	finalFilename, err := f.Storage.ResolveUniqueFilename(ctx, destFolderID, cleanFilename)
	if err != nil {
		return notify.FiledItem{}, false, fmt.Errorf("resolve unique filename: %w", err)
	}

	// Race-safety re-check before moving
	stillThere, err := f.Storage.StillInInbox(ctx, item.ID)
	if err != nil {
		return notify.FiledItem{}, false, fmt.Errorf("re-check inbox membership: %w", err)
	}
	if !stillThere {
		f.Logger.Info("file moved by another process during analysis; skipping", "name", item.Name)
		return notify.FiledItem{}, true, nil
	}

	meta := storage.FileMetadata{
		NewName:     finalFilename,
		Description: description,
		Properties:  props,
	}
	if encryptEnabled {
		if err := f.Storage.FileEncryptedDocument(ctx, item.ID, destFolderID, encryptedBytes, "application/pdf", meta); err != nil {
			return notify.FiledItem{}, false, fmt.Errorf("file encrypted document: %w", err)
		}
	} else {
		if err := f.Storage.FileDocument(ctx, item.ID, destFolderID, meta); err != nil {
			return notify.FiledItem{}, false, fmt.Errorf("file document: %w", err)
		}
	}

	f.Logger.Info("filed document successfully", "name", finalFilename, "folder", opts.ScansFolderName+"/"+targetFolderPath)

	// Self-learning write-back: index strictly PII-free filing metadata into the taxonomy vector store
	if f.TaxonomyStore != nil && !strings.EqualFold(targetFolderPath, "Unsorted") {
		tplPath := templatizeFolderYear(targetFolderPath)
		if err := f.TaxonomyStore.RecordFiling(ctx, taxonomy.FilingExample{
			Category:     category,
			Subcategory:  subcategory,
			Organization: organization,
			DocumentType: docType,
			Summary:      summary,
			FolderPath:   tplPath,
		}); err != nil {
			f.Logger.Warn("failed to record filing in taxonomy store", "err", err)
		}
		if !analysis.IsTaxonomyMatch && organization != "Unknown" {
			if err := f.TaxonomyStore.UpsertRule(ctx, taxonomy.Rule{
				Category:     category,
				Subcategory:  subcategory,
				Organization: organization,
				Keywords:     docType,
				PathTemplate: tplPath,
			}, "learned"); err != nil {
				f.Logger.Warn("failed to learn new taxonomy rule", "org", organization, "err", err)
			}
		}
	}

	return notify.FiledItem{
		Filename: finalFilename,
		Folder:   opts.ScansFolderName + "/" + targetFolderPath,
	}, false, nil
}

func deriveFallbackDate(item storage.InboxFile, now time.Time) string {
	if m := dateInFilenameRe.FindStringSubmatch(item.Name); len(m) == 4 {
		yr, err := strconv.Atoi(m[1])
		if err == nil && yr >= 1990 && yr <= 2100 {
			return fmt.Sprintf("%s-%s-%s", m[1], m[2], m[3])
		}
	}
	if !item.CreatedTime.IsZero() {
		return item.CreatedTime.UTC().Format("2006-01-02")
	}
	return now.UTC().Format("2006-01-02")
}

// buildSanitizedMetadata constructs plaintext-safe search description and properties (no PII/details).
func buildSanitizedMetadata(category, subcategory, organization, docType, docDate, summary, aiProviderName string, isMatch, encrypted bool) (string, map[string]string) {
	var tags []string
	for _, raw := range []string{category, organization, docType, subcategory} {
		if t := taxonomy.SanitizeTag(raw); t != "" {
			tags = append(tags, "#"+t)
		}
	}

	description := fmt.Sprintf(
		"Category: %s\nSubcategory: %s\nIssuer: %s\nType: %s\nDate: %s\nSummary: %s\nTags: %s",
		category, subcategory, organization, docType, docDate, summary, strings.Join(tags, " "),
	)

	matched := "false"
	if isMatch {
		matched = "true"
	}
	props := map[string]string{
		"category":         category,
		"subcategory":      subcategory,
		"organization":     organization,
		"doc_type":         docType,
		"doc_date":         docDate,
		"processed_by":     "scans-ai-filer",
		"ai_provider":      aiProviderName,
		"taxonomy_matched": matched,
	}
	if encrypted {
		props["encrypted"] = "aes-256"
	}
	return description, props
}

func defaultString(s, fallback string) string {
	if t := strings.TrimSpace(s); t != "" {
		return t
	}
	return fallback
}

// normalizeDocumentDate coerces common 4-digit-year date strings into strict YYYY-MM-DD,
// falling back to fallback (which is guaranteed YYYY-MM-DD) when the model returns an
// ambiguous 2-digit year, template placeholder, or unparseable date.
func normalizeDocumentDate(raw, fallback string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fallback
	}
	layouts := []string{
		"2006-01-02",
		"2006/01/02",
		"02-01-2006",
		"02/01/2006",
		"2/1/2006",
		"02 Jan 2006",
		"2 Jan 2006",
		"02 January 2006",
		"2 January 2006",
		"Jan 2, 2006",
		"January 2, 2006",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			if yr := t.Year(); yr >= 1900 && yr <= 2100 {
				return t.Format("2006-01-02")
			}
		}
	}
	return fallback
}

// sanitizeFolderPath replaces literal template year placeholders (e.g. "{year}", "YYYY")
// with the real 4-digit year and strips stray braces so smaller VLMs never create literal
// placeholder folders in storage.
func sanitizeFolderPath(rawPath, year string) string {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "Unsorted"
	}
	var cleaned []string
	for _, seg := range strings.Split(rawPath, "/") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		switch strings.ToLower(seg) {
		case "{year}", "{yyyy}", "yyyy", "<year>", "<yyyy>":
			seg = year
		default:
			seg = strings.NewReplacer(
				"{year}", year, "{YEAR}", year,
				"{yyyy}", year, "{YYYY}", year,
				"{", "", "}", "",
			).Replace(seg)
			seg = strings.TrimSpace(seg)
		}
		if seg != "" {
			cleaned = append(cleaned, seg)
		}
	}
	if len(cleaned) == 0 {
		return "Unsorted"
	}
	return strings.Join(cleaned, "/")
}

func templatizeFolderYear(folderPath string) string {
	segs := strings.Split(folderPath, "/")
	for i, s := range segs {
		if taxonomy.IsYear(s) {
			segs[i] = "{year}"
		}
	}
	return strings.Join(segs, "/")
}
