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
	"github.com/scubadam/scans-ai-filer/internal/filer"
	"github.com/scubadam/scans-ai-filer/internal/notify"
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

func buildAI(cfg *config.Config, providerName string, logger *slog.Logger) (ai.Provider, error) {
	switch strings.ToLower(providerName) {
	case "gemini":
		return ai.NewGeminiProvider(cfg.GeminiAPIKey, cfg.GeminiModel, logger), nil
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
		return p, nil
	default:
		return nil, fmt.Errorf("unsupported AI provider %q", providerName)
	}
}

func main() {
	dryRunFlag := flag.Bool("dry-run", false, "Analyze documents without modifying storage or sending notifications")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load(*dryRunFlag)
	if err != nil {
		logger.Error("configuration error", "err", err)
		os.Exit(1)
	}

	rules, taxPath, err := taxonomy.Load(cfg.TaxonomyCSVPath)
	if err != nil {
		logger.Error("failed to load taxonomy", "err", err)
		os.Exit(1)
	}
	if taxPath != "" {
		logger.Info("loaded taxonomy catalog", "path", taxPath, "rules", len(rules))
	} else {
		logger.Warn("no taxonomy.csv found; using fallback general taxonomy")
	}

	notifier := notify.NewWebhookNotifier(cfg.WebhookURL, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var anyErr error
	for _, route := range cfg.InboxRoutes {
		logger.Info("running inbox route", "inbox", route.InboxFolder, "ai_provider", route.AIProvider)

		store, err := buildStorage(cfg, route.InboxFolder, logger)
		if err != nil {
			logger.Error("failed to build storage provider", "inbox", route.InboxFolder, "err", err)
			anyErr = err
			continue
		}

		aiProvider, err := buildAI(cfg, route.AIProvider, logger)
		if err != nil {
			logger.Error("failed to build AI provider", "inbox", route.InboxFolder, "err", err)
			anyErr = err
			continue
		}

		f := &filer.Filer{
			Storage:  store,
			AI:       aiProvider,
			Notifier: notifier,
			Logger:   logger,
		}

		_, err = f.Run(ctx, filer.Options{
			ScansFolderName:  cfg.ScansFolderName,
			BatchSize:        cfg.BatchSize,
			PauseDuration:    time.Duration(cfg.PauseSeconds) * time.Second,
			DryRun:           cfg.DryRun,
			PDFPassword:      cfg.PDFPassword,
			PDFOwnerPassword: cfg.PDFOwnerPassword,
			Taxonomy:         rules,
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

	if anyErr != nil {
		os.Exit(1)
	}
}
