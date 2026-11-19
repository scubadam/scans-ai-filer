package main

import (
	"context"
	"flag"
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

	var store storage.Provider
	switch strings.ToLower(cfg.StorageProvider) {
	case "gdrive":
		if cfg.GoogleCredentialsJSON != "" {
			store, err = gdrive.NewFromJSON([]byte(cfg.GoogleCredentialsJSON), cfg.ScansFolderName, cfg.InboxFolderName, logger)
		} else {
			store, err = gdrive.NewFromFile(cfg.GoogleCredentialsPath, cfg.ScansFolderName, cfg.InboxFolderName, logger)
		}
		if err != nil {
			logger.Error("failed to initialize Google Drive provider", "err", err)
			os.Exit(1)
		}
	}

	var aiProvider ai.Provider
	switch strings.ToLower(cfg.AIProvider) {
	case "gemini":
		aiProvider = ai.NewGeminiProvider(cfg.GeminiAPIKey, cfg.GeminiModel, logger)
	}

	notifier := notify.NewWebhookNotifier(cfg.WebhookURL, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	f := &filer.Filer{
		Storage:  store,
		AI:       aiProvider,
		Notifier: notifier,
		Logger:   logger,
	}

	_, err = f.Run(ctx, filer.Options{
		ScansFolderName: cfg.ScansFolderName,
		BatchSize:       cfg.BatchSize,
		PauseDuration:   time.Duration(cfg.PauseSeconds) * time.Second,
		DryRun:          cfg.DryRun,
		Taxonomy:        rules,
	})
	if err != nil {
		logger.Error("filing batch failed", "err", err)
		os.Exit(1)
	}
}
