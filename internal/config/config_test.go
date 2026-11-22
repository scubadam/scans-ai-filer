package config

import "testing"

func TestParseInboxRoutes(t *testing.T) {
	routes, err := ParseInboxRoutes("Inbox:gemini, Inbox-Local:ollama")
	if err != nil {
		t.Fatalf("ParseInboxRoutes failed: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	if routes[0].InboxFolder != "Inbox" || routes[0].AIProvider != "gemini" {
		t.Errorf("unexpected route[0]: %+v", routes[0])
	}
	if routes[1].InboxFolder != "Inbox-Local" || routes[1].AIProvider != "ollama" {
		t.Errorf("unexpected route[1]: %+v", routes[1])
	}

	if _, err := ParseInboxRoutes("InvalidEntry"); err == nil {
		t.Errorf("expected error for malformed route")
	}
}

func TestLoadLocalStorage(t *testing.T) {
	t.Setenv("STORAGE_PROVIDER", "local")
	t.Setenv("LOCAL_ROOT_PATH", "/mnt/nas/scans")
	t.Setenv("AI_PROVIDER", "ollama")
	t.Setenv("OLLAMA_BASE_URL", "http://localhost:11434/v1")

	cfg, err := Load(false)
	if err != nil {
		t.Fatalf("Load(local) failed: %v", err)
	}
	if cfg.StorageProvider != "local" || cfg.LocalRootPath != "/mnt/nas/scans" {
		t.Errorf("unexpected local storage config: provider=%q root=%q", cfg.StorageProvider, cfg.LocalRootPath)
	}
}

func TestLoadNotifyConfig(t *testing.T) {
	t.Setenv("STORAGE_PROVIDER", "local")
	t.Setenv("LOCAL_ROOT_PATH", "/tmp/scans")
	t.Setenv("AI_PROVIDER", "ollama")
	t.Setenv("OLLAMA_BASE_URL", "http://localhost:11434/v1")
	t.Setenv("NOTIFY_ON_SUCCESS", "false")
	t.Setenv("NOTIFY_ON_ERROR", "true")
	t.Setenv("NTFY_URL", "http://ntfy.ntfy.svc.cluster.local")
	t.Setenv("NTFY_TOPIC", "scans-filer")
	t.Setenv("NTFY_ERROR_TOPIC", "scans-errors")
	t.Setenv("NTFY_ON_SUCCESS", "true")
	t.Setenv("WEBHOOK_URL", "http://ha.local/webhook")

	cfg, err := Load(false)
	if err != nil {
		t.Fatalf("Load(notify) failed: %v", err)
	}
	if cfg.NotifyOnSuccess || !cfg.NotifyOnError {
		t.Errorf("unexpected global notify flags: success=%v error=%v", cfg.NotifyOnSuccess, cfg.NotifyOnError)
	}
	if !cfg.NtfyOnSuccess || !cfg.NtfyOnError {
		t.Errorf("expected NTFY_ON_SUCCESS override=true, error=true; got %v/%v", cfg.NtfyOnSuccess, cfg.NtfyOnError)
	}
	if cfg.WebhookOnSuccess || !cfg.WebhookOnError {
		t.Errorf("expected webhook to inherit global false/true; got %v/%v", cfg.WebhookOnSuccess, cfg.WebhookOnError)
	}
	if cfg.NtfyTopic != "scans-filer" || cfg.NtfyErrorTopic != "scans-errors" {
		t.Errorf("unexpected ntfy topics: %q / %q", cfg.NtfyTopic, cfg.NtfyErrorTopic)
	}
}
