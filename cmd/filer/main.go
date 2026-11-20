package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/scubadam/scans-ai-filer/internal/ai"
	"github.com/scubadam/scans-ai-filer/internal/config"
	"github.com/scubadam/scans-ai-filer/internal/embed"
	"github.com/scubadam/scans-ai-filer/internal/filer"
	"github.com/scubadam/scans-ai-filer/internal/notify"
	"github.com/scubadam/scans-ai-filer/internal/ocr"
	"github.com/scubadam/scans-ai-filer/internal/storage"
	"github.com/scubadam/scans-ai-filer/internal/storage/gdrive"
	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

func buildStorage(cfg *config.Config, inboxFolder string, logger *slog.Logger) (storage.Provider, error) {
	switch strings.ToLower(cfg.StorageProvider) {
	case "gdrive":
		if cfg.GoogleCredentialsJSON != "" {
			return gdrive.NewFromJSON([]byte(cfg.GoogleCredentialsJSON), cfg.ScansFolderName, inboxFolder, logger)
		}
		return gdrive.NewFromFile(cfg.GoogleCredentialsPath, cfg.ScansFolderName, inboxFolder, logger)
	default:
		return nil, fmt.Errorf("unsupported storage provider %q", cfg.StorageProvider)
	}
}

func buildEmbedder(cfg *config.Config) embed.Provider {
	prov := strings.ToLower(strings.TrimSpace(cfg.EmbeddingProvider))
	if prov == "none" || prov == "off" || prov == "false" {
		return nil
	}
	if prov == "" || prov == "auto" {
		// Prefer local Ollama embeddings whenever any inbox route uses local AI (keeps local docs 100% private)
		for _, r := range cfg.InboxRoutes {
			if r.AIProvider == "ollama" || r.AIProvider == "openai" {
				prov = "ollama"
				break
			}
		}
		if prov == "" || prov == "auto" {
			if cfg.GeminiAPIKey != "" {
				prov = "gemini"
			} else {
				return nil
			}
		}
	}
	switch prov {
	case "ollama", "openai":
		return embed.NewOllamaEmbedder(cfg.OllamaBaseURL, cfg.OllamaAPIKey, cfg.EmbeddingModel, 60*time.Second)
	case "gemini":
		if cfg.GeminiAPIKey == "" {
			return nil
		}
		return embed.NewGeminiEmbedder(cfg.GeminiAPIKey, cfg.EmbeddingModel, 30*time.Second)
	default:
		return nil
	}
}

func buildAI(cfg *config.Config, providerName string, taxStore taxonomy.Store, ocrProv ocr.Provider, logger *slog.Logger) (ai.Provider, error) {
	switch strings.ToLower(providerName) {
	case "gemini":
		g := ai.NewGeminiProvider(cfg.GeminiAPIKey, cfg.GeminiModel, logger)
		g.HybridOCR = cfg.OCRHybridRemote
		g.OCR = ocrProv
		g.OCRMinChars = cfg.OCRMinChars
		g.VisualGlanceDim = cfg.OCRVisualGlanceDim
		g.MaxPages = cfg.OllamaMaxPages
		g.TaxonomyStore = taxStore
		g.TopK = cfg.TaxonomyTopK
		return g, nil
	case "ollama", "openai":
		p := ai.NewOllamaOpenAIProvider(
			cfg.OllamaBaseURL,
			cfg.OllamaAPIKey,
			cfg.OllamaModel,
			time.Duration(cfg.OllamaTimeoutSeconds)*time.Second,
			logger,
		)
		p.TextModel = cfg.OllamaTextModel
		p.MaxPages = cfg.OllamaMaxPages
		p.MaxImageDim = cfg.OllamaMaxImageDim
		p.VisualGlanceDim = cfg.OCRVisualGlanceDim
		p.OCR = ocrProv
		p.OCRMinChars = cfg.OCRMinChars
		p.TaxonomyStore = taxStore
		p.TopK = cfg.TaxonomyTopK
		return p, nil
	default:
		return nil, fmt.Errorf("unsupported AI provider %q", providerName)
	}
}

func main() {
	dryRunFlag := flag.Bool("dry-run", false, "Analyze documents without modifying storage or sending notifications")
	bootstrapFlag := flag.Bool("bootstrap-taxonomy", false, "Walk already-filed documents in storage, print diff vs taxonomy.csv, and populate TAXONOMY_DB_PATH")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load(*dryRunFlag)
	if err != nil {
		logger.Error("configuration error", "err", err)
		os.Exit(1)
	}

	csvRules, taxPath, err := taxonomy.Load(cfg.TaxonomyCSVPath)
	if err != nil {
		logger.Error("failed to load taxonomy", "err", err)
		os.Exit(1)
	}
	if taxPath != "" {
		logger.Info("loaded taxonomy CSV", "path", taxPath, "rules", len(csvRules))
	} else {
		logger.Warn("no taxonomy.csv found; using fallback or database taxonomy")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var taxStore taxonomy.Store
	if cfg.TaxonomyDBPath != "" {
		embedder := buildEmbedder(cfg)
		embedName := "none"
		if embedder != nil {
			embedName = embedder.Name()
		}
		taxStore, err = taxonomy.OpenSQLiteStore(cfg.TaxonomyDBPath, embedder)
		if err != nil {
			logger.Error("failed to open taxonomy database", "path", cfg.TaxonomyDBPath, "err", err)
			os.Exit(1)
		}
		defer taxStore.Close()
		logger.Info("opened taxonomy vector store", "path", cfg.TaxonomyDBPath, "embedder", embedName)

		if len(csvRules) > 0 && !*bootstrapFlag {
			if n, seedErr := taxStore.SeedFromRules(ctx, csvRules, "csv"); seedErr != nil {
				logger.Warn("failed to seed taxonomy DB from CSV", "err", seedErr)
			} else {
				logger.Info("synced taxonomy CSV into vector store", "rules", n)
			}
		}
	}

	// Optional bootstrap mode: walk existing filed documents in storage, print diff vs CSV, and populate DB
	if *bootstrapFlag {
		store, err := buildStorage(cfg, cfg.InboxRoutes[0].InboxFolder, logger)
		if err != nil {
			logger.Error("failed to build storage provider for bootstrap", "err", err)
			os.Exit(1)
		}
		if err := store.Init(ctx); err != nil {
			logger.Error("failed to initialize storage for bootstrap", "err", err)
			os.Exit(1)
		}
		lister, ok := store.(storage.FiledDocumentLister)
		if !ok {
			logger.Error("storage provider does not support listing filed documents for bootstrap", "provider", store.Name())
			os.Exit(1)
		}
		docs, err := lister.ListFiledDocuments(ctx)
		if err != nil {
			logger.Error("failed to walk filed documents", "err", err)
			os.Exit(1)
		}
		derivedRules, examples := taxonomy.DeriveRulesFromFiledDocs(docs)
		logger.Info("analyzed existing filed documents", "files", len(docs), "derived_rules", len(derivedRules), "examples", len(examples))

		diff := taxonomy.DiffRules(csvRules, derivedRules)
		fmt.Println("\n" + taxonomy.FormatDiffReport(diff))

		if cfg.DryRun {
			logger.Info("dry-run enabled; skipping taxonomy DB write")
			return
		}
		if taxStore == nil {
			logger.Warn("TAXONOMY_DB_PATH is not set; diff printed only (no database written)")
			return
		}
		if _, err := taxStore.SeedFromRules(ctx, derivedRules, "storage"); err != nil {
			logger.Error("failed to seed derived rules into taxonomy DB", "err", err)
			os.Exit(1)
		}
		for _, ex := range examples {
			if err := taxStore.RecordFiling(ctx, ex); err != nil {
				logger.Warn("failed to record filing example", "org", ex.Organization, "err", err)
			}
		}
		stats, _ := taxStore.Stats(ctx)
		logger.Info("bootstrapped taxonomy DB from storage", "rules", stats.RuleCount, "history", stats.HistoryCount, "embedded", stats.EmbeddedDocs)
		return
	}

	ocrProv := ocr.AutoProvider(cfg.OCRProvider, cfg.OCREndpointURL, cfg.OCRLang, 60*time.Second)
	if ocrProv != nil {
		logger.Info("enabled deterministic OCR provider", "provider", ocrProv.Name(), "visual_glance_dim", cfg.OCRVisualGlanceDim, "hybrid_remote", cfg.OCRHybridRemote, "embed_pdf", cfg.OCREmbedPDF)
	}

	notifier := notify.NewWebhookNotifier(cfg.WebhookURL, logger)

	runCycle := func() error {
		curRules := csvRules
		if taxStore != nil {
			if dbRules, listErr := taxStore.ListRules(ctx); listErr == nil && len(dbRules) > 0 {
				curRules = dbRules
			}
		}

		var anyErr error
		for _, route := range cfg.InboxRoutes {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logger.Info("running inbox route", "inbox", route.InboxFolder, "ai_provider", route.AIProvider)

			store, err := buildStorage(cfg, route.InboxFolder, logger)
			if err != nil {
				logger.Error("failed to build storage provider", "inbox", route.InboxFolder, "err", err)
				anyErr = err
				continue
			}

			aiProvider, err := buildAI(cfg, route.AIProvider, taxStore, ocrProv, logger)
			if err != nil {
				logger.Error("failed to build AI provider", "inbox", route.InboxFolder, "err", err)
				anyErr = err
				continue
			}

			f := &filer.Filer{
				Storage:       store,
				AI:            aiProvider,
				Notifier:      notifier,
				TaxonomyStore: taxStore,
				OCR:           ocrProv,
				Logger:        logger,
			}

			_, err = f.Run(ctx, filer.Options{
				ScansFolderName:  cfg.ScansFolderName,
				BatchSize:        cfg.BatchSize,
				PauseDuration:    time.Duration(cfg.PauseSeconds) * time.Second,
				DryRun:           cfg.DryRun,
				PDFPassword:      cfg.PDFPassword,
				PDFOwnerPassword: cfg.PDFOwnerPassword,
				OCREmbedPDF:      cfg.OCREmbedPDF,
				Taxonomy:         curRules,
			})
			if err != nil {
				// If a secondary inbox folder simply hasn't been created yet in a multi-route setup, warn rather than aborting other routes
				if len(cfg.InboxRoutes) > 1 && strings.Contains(err.Error(), "inbox folder") && strings.Contains(err.Error(), "not found") {
					logger.Warn("secondary inbox folder not found; skipping route", "inbox", route.InboxFolder)
					continue
				}
				logger.Error("inbox route failed", "inbox", route.InboxFolder, "err", err)
				anyErr = err
			}
		}
		return anyErr
	}

	// Single-shot mode (CronJob / CLI)
	if cfg.PollIntervalSeconds <= 0 {
		if err := runCycle(); err != nil {
			os.Exit(1)
		}
		return
	}

	// Continuous daemon mode (Deployment): run sequentially, sleeping only AFTER each cycle finishes
	interval := time.Duration(cfg.PollIntervalSeconds) * time.Second
	logger.Info("starting daemon poll loop", "poll_interval", interval.String(), "routes", len(cfg.InboxRoutes))
	for {
		if err := runCycle(); err != nil && ctx.Err() == nil {
			logger.Warn("poll cycle completed with errors; will retry next interval", "err", err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			logger.Info("received shutdown signal; exiting cleanly")
			return
		case <-timer.C:
		}
	}
}
