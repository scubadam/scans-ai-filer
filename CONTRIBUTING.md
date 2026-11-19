# Contributing to `scans-ai-filer`

Thank you for your interest in contributing!

## Guidelines
- Keep the core binary zero-dependency (Go standard library) wherever practical.
- Never commit personal taxonomy files (`taxonomy.local.csv`), API keys, or service account JSON files.
- Ensure all new AI prompts and metadata fields preserve strict PII sanitization (no account numbers, balances, IDs, or medical specifics in plaintext filenames, descriptions, or notifications).
- Run `go vet ./...`, `go test -v -race ./...`, and `helm lint charts/scans-ai-filer` before opening a pull request.
