package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds runtime configuration for scans-ai-filer.
type Config struct {
	// Storage provider configuration
	StorageProvider string
	ScansFolderName string
	InboxFolderName string

	// Google Drive credentials (file path or raw JSON)
	GoogleCredentialsPath string
	GoogleCredentialsJSON string

	// AI provider configuration
	AIProvider   string
	GeminiAPIKey string
	GeminiModel  string

	// Taxonomy and batch processing
	TaxonomyCSVPath string
	BatchSize       int
	PauseSeconds    int
	DryRun          bool

	// Optional AES-256 PDF password encryption before filing
	PDFPassword      string
	PDFOwnerPassword string

	// Optional sanitized webhook notification (compatible with Home Assistant webhooks)
	WebhookURL string
}

// Load reads configuration from environment variables with sensible defaults.
func Load(dryRunFlag bool) (*Config, error) {
	batchSize := 10
	if raw := strings.TrimSpace(os.Getenv("BATCH_SIZE")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid BATCH_SIZE %q: must be a positive integer", raw)
		}
		batchSize = v
	}

	pauseSec := 1
	if raw := strings.TrimSpace(os.Getenv("PAUSE_SECONDS")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return nil, fmt.Errorf("invalid PAUSE_SECONDS %q: must be >= 0", raw)
		}
		pauseSec = v
	}

	dryRun := dryRunFlag
	if !dryRun {
		v := strings.ToLower(strings.TrimSpace(os.Getenv("DRY_RUN")))
		dryRun = v == "1" || v == "true" || v == "yes"
	}

	webhookURL := strings.TrimSpace(os.Getenv("WEBHOOK_URL"))
	if webhookURL == "" {
		// Backward-compatible env var name
		webhookURL = strings.TrimSpace(os.Getenv("HA_WEBHOOK_URL"))
	}

	cfg := &Config{
		StorageProvider:       envOrDefault("STORAGE_PROVIDER", "gdrive"),
		ScansFolderName:       envOrDefault("SCANS_FOLDER_NAME", "Scans"),
		InboxFolderName:       envOrDefault("INBOX_FOLDER_NAME", "Inbox"),
		GoogleCredentialsPath: envOrDefault("GOOGLE_APPLICATION_CREDENTIALS", "/etc/gdrive/gdrive-sa.json"),
		GoogleCredentialsJSON: strings.TrimSpace(os.Getenv("GDRIVE_SA_JSON")),
		AIProvider:            envOrDefault("AI_PROVIDER", "gemini"),
		GeminiAPIKey:          strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
		GeminiModel:           envOrDefault("GEMINI_MODEL", "gemini-2.5-flash"),
		TaxonomyCSVPath:       strings.TrimSpace(os.Getenv("TAXONOMY_CSV_PATH")),
		BatchSize:             batchSize,
		PauseSeconds:          pauseSec,
		DryRun:                dryRun,
		PDFPassword:           strings.TrimSpace(os.Getenv("PDF_PASSWORD")),
		PDFOwnerPassword:      strings.TrimSpace(os.Getenv("PDF_OWNER_PASSWORD")),
		WebhookURL:            webhookURL,
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks required settings for the chosen providers.
func (c *Config) Validate() error {
	switch strings.ToLower(c.StorageProvider) {
	case "gdrive":
		if c.GoogleCredentialsJSON == "" && c.GoogleCredentialsPath == "" {
			return fmt.Errorf("gdrive storage requires GOOGLE_APPLICATION_CREDENTIALS or GDRIVE_SA_JSON")
		}
	default:
		return fmt.Errorf("unsupported STORAGE_PROVIDER %q (supported: gdrive)", c.StorageProvider)
	}

	switch strings.ToLower(c.AIProvider) {
	case "gemini":
		if c.GeminiAPIKey == "" {
			return fmt.Errorf("gemini AI provider requires GEMINI_API_KEY")
		}
	default:
		return fmt.Errorf("unsupported AI_PROVIDER %q (supported: gemini)", c.AIProvider)
	}
	return nil
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
