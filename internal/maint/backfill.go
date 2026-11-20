package maint

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/scubadam/scans-ai-filer/internal/ocr"
	"github.com/scubadam/scans-ai-filer/internal/pdfenc"
	"github.com/scubadam/scans-ai-filer/internal/storage"
	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

// BackfillOptions configures BackfillPDFs.
type BackfillOptions struct {
	DryRun           bool
	Limit            int
	EmbedOCR         bool
	PDFPassword      string
	PDFOwnerPassword string
}

// BackfillResult summarizes a searchable-OCR + AES-256 encryption backfill run.
type BackfillResult struct {
	TotalScanned int
	Candidates   int
	Processed    int
	Skipped      int
	Errors       int
	PreviewPaths []string
}

// BackfillPDFs idempotently walks existing filed documents in storage and upgrades any
// file missing searchable OCR embedding (when EmbedOCR is true) and/or AES-256 encryption
// (when PDFPassword is set), saving updated bytes in place.
func BackfillPDFs(ctx context.Context, baseStore storage.Provider, maintStore storage.MaintenanceProvider, ocrProv ocr.Provider, opts BackfillOptions, logger *slog.Logger) (*BackfillResult, error) {
	if maintStore == nil {
		return nil, fmt.Errorf("storage provider does not support maintenance backfill")
	}
	if !opts.EmbedOCR && strings.TrimSpace(opts.PDFPassword) == "" {
		return nil, fmt.Errorf("backfill requires OCR_EMBED_PDF=true and/or PDF_PASSWORD")
	}
	if logger == nil {
		logger = slog.Default()
	}

	var pdfBuilder ocr.PDFBuilder
	if opts.EmbedOCR && ocrProv != nil {
		if b, ok := ocrProv.(ocr.PDFBuilder); ok {
			pdfBuilder = b
		}
	}

	docs, err := maintStore.ListFiledDocuments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list filed documents: %w", err)
	}

	wantEncrypt := strings.TrimSpace(opts.PDFPassword) != ""
	res := &BackfillResult{TotalScanned: len(docs)}

	for _, doc := range docs {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		mime := strings.ToLower(strings.TrimSpace(doc.MimeType))
		if mime != "application/pdf" && !strings.HasPrefix(mime, "image/") {
			res.Skipped++
			continue
		}

		alreadyEncrypted := doc.Properties["encrypted"] == "aes-256"
		alreadySearchable := doc.Properties["searchable_ocr"] == "true"

		needsEncrypt := wantEncrypt && !alreadyEncrypted
		needsOCR := pdfBuilder != nil && !alreadySearchable

		if !needsEncrypt && !needsOCR {
			res.Skipped++
			continue
		}

		res.Candidates++
		if opts.DryRun {
			if len(res.PreviewPaths) < 25 {
				res.PreviewPaths = append(res.PreviewPaths, fmt.Sprintf("%s/%s (ocr=%v encrypt=%v)", doc.FolderPath, doc.Name, needsOCR, needsEncrypt))
			}
			if opts.Limit > 0 && res.Candidates >= opts.Limit {
				break
			}
			continue
		}

		data, err := baseStore.Download(ctx, doc.ID)
		if err != nil {
			logger.Warn("backfill download failed", "file", doc.Name, "err", err)
			res.Errors++
			continue
		}

		// If the PDF is already encrypted on disk, decrypt in memory before OCR/re-encryption
		workBytes := data
		workMime := mime
		isPDFEncrypted := workMime == "application/pdf" && bytes.Contains(workBytes, []byte("/Encrypt"))
		if isPDFEncrypted {
			if !wantEncrypt {
				logger.Warn("skipping already-encrypted PDF without PDF_PASSWORD", "file", doc.Name)
				res.Skipped++
				continue
			}
			dec, decErr := pdfenc.DecryptDocument(ctx, workBytes, opts.PDFPassword)
			if decErr != nil {
				logger.Warn("failed to unlock encrypted PDF during backfill; skipping", "file", doc.Name, "err", decErr)
				res.Errors++
				continue
			}
			workBytes = dec
		}

		props := make(map[string]string, len(doc.Properties)+2)
		for k, v := range doc.Properties {
			props[k] = v
		}

		modified := false
		if needsOCR {
			searchable, changed, ocrErr := ocr.EmbedSearchableTextLayer(ctx, workBytes, workMime, pdfBuilder, logger)
			if ocrErr != nil {
				logger.Warn("backfill searchable OCR failed; continuing", "file", doc.Name, "err", ocrErr)
			} else if changed {
				workBytes = searchable
				workMime = "application/pdf"
				modified = true
			}
			// Mark searchable_ocr=true even for digital PDFs that already had a text layer so we don't re-download them
			if ocrErr == nil {
				props["searchable_ocr"] = "true"
			}
		}

		if wantEncrypt {
			enc, encErr := pdfenc.EncryptDocument(ctx, workBytes, workMime, opts.PDFPassword, opts.PDFOwnerPassword)
			if encErr != nil {
				logger.Warn("backfill AES-256 encryption failed", "file", doc.Name, "err", encErr)
				res.Errors++
				continue
			}
			workBytes = enc
			workMime = "application/pdf"
			props["encrypted"] = "aes-256"
			modified = true
		}

		if !modified && props["searchable_ocr"] == doc.Properties["searchable_ocr"] {
			res.Skipped++
			continue
		}

		newName := doc.Name
		if workMime == "application/pdf" && !strings.HasSuffix(strings.ToLower(newName), ".pdf") {
			base, _ := taxonomy.SplitExt(newName)
			newName = base + ".pdf"
		}

		if err := maintStore.UpdateFiledContent(ctx, doc.ID, newName, workMime, workBytes, props); err != nil {
			logger.Warn("backfill upload failed", "file", newName, "err", err)
			res.Errors++
			continue
		}
		res.Processed++
		logger.Info("backfilled document",
			"index", res.Processed,
			"name", newName,
			"folder", doc.FolderPath,
			"searchable_ocr", props["searchable_ocr"],
			"encrypted", props["encrypted"],
		)

		if opts.Limit > 0 && res.Processed >= opts.Limit {
			break
		}
	}
	return res, nil
}

// FormatBackfillReport renders a concise summary of a backfill run.
func FormatBackfillReport(res *BackfillResult, dryRun bool) string {
	var b strings.Builder
	mode := "EXECUTED"
	if dryRun {
		mode = "DRY-RUN"
	}
	fmt.Fprintf(&b, "PDF Backfill (%s): scanned=%d candidates=%d processed=%d skipped=%d errors=%d\n",
		mode, res.TotalScanned, res.Candidates, res.Processed, res.Skipped, res.Errors)
	for _, p := range res.PreviewPaths {
		fmt.Fprintf(&b, "  - %s\n", p)
	}
	if res.Candidates > len(res.PreviewPaths) && dryRun {
		fmt.Fprintf(&b, "  ... and %d more\n", res.Candidates-len(res.PreviewPaths))
	}
	return b.String()
}
