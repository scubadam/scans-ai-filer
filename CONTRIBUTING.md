# Contributing to `scans-ai-filer`

Thank you for your interest in contributing!

## Guidelines
- Keep the core binary lightweight (`CGO_ENABLED=0`) and prefer the Go standard library wherever practical.
- Never commit personal taxonomy files (`taxonomy.local.csv`), API keys, or service account JSON files.
- Ensure all new AI prompts and metadata fields preserve strict PII sanitization (no account numbers, balances, IDs, or medical specifics in plaintext filenames, descriptions, or notifications).
- Run `go vet ./...`, `go test -v -race ./internal/...`, `go test -tags=integration -v -race ./tests/integration/...`, and `helm lint charts/scans-ai-filer` before opening a pull request.

## Adding a New Storage Provider

`scans-ai-filer` ships with `gdrive` (`internal/storage/gdrive`) and `local` (`internal/storage/local`), plus a compile-checked reference stub at **`internal/storage/s3/s3.go`** for S3/MinIO/R2 (and as a template for WebDAV/Nextcloud, SMB, etc.).

1. **Implement `storage.Provider`** (and optionally `storage.MaintenanceProvider` so `--bootstrap-taxonomy`, `--reorg-storage`, `--backfill-pdfs`, and the embedded Taxonomy Web UI work with your backend). Start from `internal/storage/s3/s3.go`.
2. **Add config & validation** in `internal/config/config.go` (`Config`, `Load`, and `Validate`).
3. **Wire the constructor** into `buildStorage()` in `cmd/filer/main.go`.
4. **Add tests:** unit tests in `internal/storage/<name>/<name>_test.go` and an E2E pipeline test in `tests/integration/`.

## Adding a New AI Provider

`scans-ai-filer` ships with `gemini` (`internal/ai/gemini.go`) and `ollama`/`openai` (`internal/ai/ollama.go`), plus a compile-checked reference stub at **`internal/ai/anthropic.go`** for additional cloud AI backends (Anthropic Claude, AWS Bedrock, Azure OpenAI, etc.).

1. **Implement `ai.Provider`** (and optionally `ai.TextCompleter` for AI Taxonomy Gardener and filename PII audits). Start from `internal/ai/anthropic.go` and reuse `BuildClassificationPrompt(input)` to preserve taxonomy rules and PII sanitization.
2. **Add config & validation** in `internal/config/config.go` (`Config`, `Load`, and `Validate`).
3. **Wire the constructor** into `buildAI()` in `cmd/filer/main.go`.
4. **Add unit tests** using `net/http/httptest` (see `internal/ai/gemini_test.go` and `internal/ai/ollama_test.go`).
