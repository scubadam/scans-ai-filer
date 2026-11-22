package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// InboxRoute maps a storage inbox folder name to a specific AI provider (e.g. "Inbox:gemini", "Inbox-Local:ollama").
type InboxRoute struct {
	InboxFolder string
	AIProvider  string
}

// Config holds runtime configuration for scans-ai-filer.
type Config struct {
	// Storage provider configuration
	StorageProvider string
	ScansFolderName string
	InboxFolderName string
	LocalRootPath   string

	// Multi-inbox routing (defaults to single route InboxFolderName -> AIProvider when INBOX_ROUTES is unset)
	InboxRoutes []InboxRoute

	// Google Drive credentials (file path or raw JSON)
	GoogleCredentialsPath string
	GoogleCredentialsJSON string

	// AI provider configuration
	AIProvider           string
	GeminiAPIKey         string
	GeminiModel          string
	OllamaBaseURL        string
	OllamaAPIKey         string
	OllamaModel          string
	OllamaTextModel      string
	OllamaTimeoutSeconds int
	OllamaMaxPages       int
	OllamaMaxImageDim    int

	// Hybrid deterministic OCR + low-res visual glance configuration
	OCRProvider        string
	OCREndpointURL     string
	OCRLang            string
	OCRMinChars        int
	OCRVisualGlanceDim int
	OCRHybridRemote    bool
	OCREmbedPDF        bool

	// Taxonomy, vector store, and batch processing
	TaxonomyCSVPath   string
	TaxonomyDBPath    string
	EmbeddingProvider string
	EmbeddingModel    string
	TaxonomyTopK      int
	BatchSize           int
	PauseSeconds        int
	PollIntervalSeconds int
	DryRun              bool

	// Optional AES-256 PDF password encryption before filing
	PDFPassword      string
	PDFOwnerPassword string

	// Notification configuration (multi-target fan-out with per-channel success/error filtering)
	NotifyOnSuccess   bool
	NotifyOnError     bool
	WebhookURL        string
	WebhookOnSuccess  bool
	WebhookOnError    bool
	NtfyURL           string
	NtfyTopic         string
	NtfyErrorTopic    string
	NtfyToken         string
	NtfyUsername      string
	NtfyPassword      string
	NtfyPriority      string
	NtfyErrorPriority string
	NtfyClickURL      string
	NtfyOnSuccess     bool
	NtfyOnError       bool

	// Optional embedded Taxonomy Browser/Editor PWA (used in Deployment daemon mode)
	WebUIEnabled bool
	WebUIAddr    string
}

// ParseInboxRoutes parses a comma-separated list of "FolderName:provider" pairs.
func ParseInboxRoutes(raw string) ([]InboxRoute, error) {
	var routes []InboxRoute
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return nil, fmt.Errorf("invalid INBOX_ROUTES entry %q: expected 'FolderName:provider'", entry)
		}
		routes = append(routes, InboxRoute{
			InboxFolder: strings.TrimSpace(parts[0]),
			AIProvider:  strings.ToLower(strings.TrimSpace(parts[1])),
		})
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("INBOX_ROUTES did not contain any valid routes")
	}
	return routes, nil
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

	pollIntervalSec := 0
	if raw := strings.TrimSpace(os.Getenv("POLL_INTERVAL_SECONDS")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return nil, fmt.Errorf("invalid POLL_INTERVAL_SECONDS %q: must be >= 0", raw)
		}
		pollIntervalSec = v
	}

	ollamaTimeout := 300
	if raw := strings.TrimSpace(os.Getenv("OLLAMA_TIMEOUT_SECONDS")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid OLLAMA_TIMEOUT_SECONDS %q: must be > 0", raw)
		}
		ollamaTimeout = v
	}

	ollamaMaxPages := 5
	if raw := strings.TrimSpace(os.Getenv("OLLAMA_MAX_PAGES")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid OLLAMA_MAX_PAGES %q: must be > 0", raw)
		}
		ollamaMaxPages = v
	}

	ollamaMaxImageDim := 1536
	if raw := strings.TrimSpace(os.Getenv("OLLAMA_MAX_IMAGE_DIM")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return nil, fmt.Errorf("invalid OLLAMA_MAX_IMAGE_DIM %q: must be >= 0", raw)
		}
		ollamaMaxImageDim = v
	}

	taxonomyTopK := 8
	if raw := strings.TrimSpace(os.Getenv("TAXONOMY_TOP_K")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid TAXONOMY_TOP_K %q: must be > 0", raw)
		}
		taxonomyTopK = v
	}

	ocrMinChars := 80
	if raw := strings.TrimSpace(os.Getenv("OCR_MIN_CHARS")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return nil, fmt.Errorf("invalid OCR_MIN_CHARS %q: must be >= 0", raw)
		}
		ocrMinChars = v
	}

	ocrVisualGlanceDim := 640
	if raw := strings.TrimSpace(os.Getenv("OCR_VISUAL_GLANCE_DIM")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return nil, fmt.Errorf("invalid OCR_VISUAL_GLANCE_DIM %q: must be >= 0", raw)
		}
		ocrVisualGlanceDim = v
	}

	ocrHybridRemote := false
	if raw := strings.ToLower(strings.TrimSpace(os.Getenv("OCR_HYBRID_REMOTE"))); raw == "1" || raw == "true" || raw == "yes" {
		ocrHybridRemote = true
	}

	ocrEmbedPDF := false
	if raw := strings.ToLower(strings.TrimSpace(os.Getenv("OCR_EMBED_PDF"))); raw == "1" || raw == "true" || raw == "yes" {
		ocrEmbedPDF = true
	}

	webUIEnabled := false
	if raw := strings.ToLower(strings.TrimSpace(os.Getenv("WEB_UI_ENABLED"))); raw == "1" || raw == "true" || raw == "yes" {
		webUIEnabled = true
	}

	dryRun := dryRunFlag
	if !dryRun {
		v := strings.ToLower(strings.TrimSpace(os.Getenv("DRY_RUN")))
		dryRun = v == "1" || v == "true" || v == "yes"
	}

	notifyOnSuccess := envBoolOrDefault("NOTIFY_ON_SUCCESS", true)
	notifyOnError := envBoolOrDefault("NOTIFY_ON_ERROR", true)
	webhookOnSuccess := envBoolOrDefault("WEBHOOK_ON_SUCCESS", notifyOnSuccess)
	webhookOnError := envBoolOrDefault("WEBHOOK_ON_ERROR", notifyOnError)
	ntfyOnSuccess := envBoolOrDefault("NTFY_ON_SUCCESS", notifyOnSuccess)
	ntfyOnError := envBoolOrDefault("NTFY_ON_ERROR", notifyOnError)

	webhookURL := strings.TrimSpace(os.Getenv("WEBHOOK_URL"))
	if webhookURL == "" {
		webhookURL = strings.TrimSpace(os.Getenv("HA_WEBHOOK_URL"))
	}

	inboxFolder := envOrDefault("INBOX_FOLDER_NAME", "Inbox")
	aiProvider := strings.ToLower(envOrDefault("AI_PROVIDER", "gemini"))

	var routes []InboxRoute
	if rawRoutes := strings.TrimSpace(os.Getenv("INBOX_ROUTES")); rawRoutes != "" {
		parsed, err := ParseInboxRoutes(rawRoutes)
		if err != nil {
			return nil, err
		}
		routes = parsed
	} else {
		routes = []InboxRoute{{InboxFolder: inboxFolder, AIProvider: aiProvider}}
	}

	ollamaBaseURL := envOrDefault("OLLAMA_BASE_URL", envOrDefault("OPENAI_BASE_URL", "http://ollama-service.default.svc.cluster.local:11434/v1"))
	ollamaModel := envOrDefault("OLLAMA_MODEL", envOrDefault("OPENAI_MODEL", "qwen2.5vl:3b"))
	ollamaTextModel := strings.TrimSpace(os.Getenv("OLLAMA_TEXT_MODEL"))
	ollamaAPIKey := strings.TrimSpace(os.Getenv("OLLAMA_API_KEY"))
	if ollamaAPIKey == "" {
		ollamaAPIKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}

	cfg := &Config{
		StorageProvider:       envOrDefault("STORAGE_PROVIDER", "gdrive"),
		ScansFolderName:       envOrDefault("SCANS_FOLDER_NAME", "Scans"),
		InboxFolderName:       inboxFolder,
		LocalRootPath:         envOrDefault("LOCAL_ROOT_PATH", "/data/scans"),
		InboxRoutes:           routes,
		GoogleCredentialsPath: envOrDefault("GOOGLE_APPLICATION_CREDENTIALS", "/etc/gdrive/gdrive-sa.json"),
		GoogleCredentialsJSON: strings.TrimSpace(os.Getenv("GDRIVE_SA_JSON")),
		AIProvider:            aiProvider,
		GeminiAPIKey:          strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
		GeminiModel:           envOrDefault("GEMINI_MODEL", "gemini-2.5-flash"),
		OllamaBaseURL:         ollamaBaseURL,
		OllamaAPIKey:          ollamaAPIKey,
		OllamaModel:           ollamaModel,
		OllamaTextModel:       ollamaTextModel,
		OllamaTimeoutSeconds:  ollamaTimeout,
		OllamaMaxPages:        ollamaMaxPages,
		OllamaMaxImageDim:     ollamaMaxImageDim,
		OCRProvider:           strings.ToLower(envOrDefault("OCR_PROVIDER", "auto")),
		OCREndpointURL:        strings.TrimSpace(os.Getenv("OCR_ENDPOINT_URL")),
		OCRLang:               envOrDefault("OCR_LANG", "eng"),
		OCRMinChars:           ocrMinChars,
		OCRVisualGlanceDim:    ocrVisualGlanceDim,
		OCRHybridRemote:       ocrHybridRemote,
		OCREmbedPDF:           ocrEmbedPDF,
		TaxonomyCSVPath:       strings.TrimSpace(os.Getenv("TAXONOMY_CSV_PATH")),
		TaxonomyDBPath:        strings.TrimSpace(os.Getenv("TAXONOMY_DB_PATH")),
		EmbeddingProvider:     strings.ToLower(envOrDefault("EMBEDDING_PROVIDER", "auto")),
		EmbeddingModel:        strings.TrimSpace(os.Getenv("EMBEDDING_MODEL")),
		TaxonomyTopK:          taxonomyTopK,
		BatchSize:             batchSize,
		PauseSeconds:          pauseSec,
		PollIntervalSeconds:   pollIntervalSec,
		DryRun:                dryRun,
		PDFPassword:           strings.TrimSpace(os.Getenv("PDF_PASSWORD")),
		PDFOwnerPassword:      strings.TrimSpace(os.Getenv("PDF_OWNER_PASSWORD")),
		NotifyOnSuccess:       notifyOnSuccess,
		NotifyOnError:         notifyOnError,
		WebhookURL:            webhookURL,
		WebhookOnSuccess:      webhookOnSuccess,
		WebhookOnError:        webhookOnError,
		NtfyURL:               strings.TrimSpace(os.Getenv("NTFY_URL")),
		NtfyTopic:             strings.TrimSpace(os.Getenv("NTFY_TOPIC")),
		NtfyErrorTopic:        strings.TrimSpace(os.Getenv("NTFY_ERROR_TOPIC")),
		NtfyToken:             strings.TrimSpace(os.Getenv("NTFY_TOKEN")),
		NtfyUsername:          strings.TrimSpace(os.Getenv("NTFY_USERNAME")),
		NtfyPassword:          strings.TrimSpace(os.Getenv("NTFY_PASSWORD")),
		NtfyPriority:          envOrDefault("NTFY_PRIORITY", "default"),
		NtfyErrorPriority:     envOrDefault("NTFY_ERROR_PRIORITY", "high"),
		NtfyClickURL:          strings.TrimSpace(os.Getenv("NTFY_CLICK_URL")),
		NtfyOnSuccess:         ntfyOnSuccess,
		NtfyOnError:           ntfyOnError,
		WebUIEnabled:          webUIEnabled,
		WebUIAddr:             envOrDefault("WEB_UI_ADDR", ":8080"),
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks required settings for all configured storage and AI providers.
func (c *Config) Validate() error {
	switch strings.ToLower(c.StorageProvider) {
	case "gdrive":
		if c.GoogleCredentialsJSON == "" && c.GoogleCredentialsPath == "" {
			return fmt.Errorf("gdrive storage requires GOOGLE_APPLICATION_CREDENTIALS or GDRIVE_SA_JSON")
		}
	case "local":
		if strings.TrimSpace(c.LocalRootPath) == "" {
			return fmt.Errorf("local storage requires LOCAL_ROOT_PATH")
		}
	default:
		return fmt.Errorf("unsupported STORAGE_PROVIDER %q (supported: gdrive, local)", c.StorageProvider)
	}

	if len(c.InboxRoutes) == 0 {
		return fmt.Errorf("at least one inbox route must be configured")
	}

	for _, r := range c.InboxRoutes {
		switch strings.ToLower(r.AIProvider) {
		case "gemini":
			if c.GeminiAPIKey == "" {
				return fmt.Errorf("route %q uses gemini AI provider which requires GEMINI_API_KEY", r.InboxFolder)
			}
		case "ollama", "openai":
			if c.OllamaBaseURL == "" {
				return fmt.Errorf("route %q uses %s AI provider which requires OLLAMA_BASE_URL", r.InboxFolder, r.AIProvider)
			}
		default:
			return fmt.Errorf("unsupported AI provider %q in route %q (supported: gemini, ollama, openai)", r.AIProvider, r.InboxFolder)
		}
	}
	if c.NtfyURL != "" && c.NtfyTopic == "" && c.NtfyErrorTopic == "" {
		return fmt.Errorf("ntfy notifications require NTFY_TOPIC (or NTFY_ERROR_TOPIC) when NTFY_URL is set")
	}
	return nil
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envBoolOrDefault(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
