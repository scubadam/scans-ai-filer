package webui

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/scubadam/scans-ai-filer/internal/maint"
	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

//go:embed static/*
var staticFS embed.FS

// Hooks provides optional runtime callbacks wired by main.go for live actions.
type Hooks struct {
	TriggerScan func() bool
	RunTidy     func(ctx context.Context, dryRun bool) (*maint.TidyResult, error)
	RunReorg    func(ctx context.Context, dryRun, sanitizeNames bool) (*maint.ReorgResult, error)
	ConfigInfo  map[string]any
}

// Server serves the embedded Taxonomy Browser/Editor PWA and REST API.
type Server struct {
	Store  taxonomy.AdminStore
	Hooks  Hooks
	Logger *slog.Logger
}

// Handler constructs the http.Handler for the PWA and API endpoints.
func (s *Server) Handler() http.Handler {
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/rules", s.handleListRules)
	mux.HandleFunc("POST /api/rules", s.handleCreateRule)
	mux.HandleFunc("PUT /api/rules/{id}", s.handleUpdateRule)
	mux.HandleFunc("DELETE /api/rules/{id}", s.handleDeleteRule)
	mux.HandleFunc("POST /api/rules/merge", s.handleMergeRules)
	mux.HandleFunc("GET /api/history", s.handleListHistory)
	mux.HandleFunc("POST /api/history/{id}/review", s.handleMarkReviewed)
	mux.HandleFunc("POST /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/export.csv", s.handleExportCSV)
	mux.HandleFunc("POST /api/actions/scan-now", s.handleScanNow)
	mux.HandleFunc("POST /api/actions/tidy", s.handleTidy)
	mux.HandleFunc("POST /api/actions/reorg", s.handleReorg)

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
	return mux
}

// ListenAndServe starts the Web UI HTTP server and shuts down cleanly when ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	if strings.TrimSpace(addr) == "" {
		addr = ":8080"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	s.Logger.Info("starting taxonomy web UI", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "taxonomy database not configured")
		return
	}
	st, err := s.Store.Stats(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stats":  st,
		"config": s.Hooks.ConfigInfo,
	})
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "taxonomy database not configured")
		return
	}
	rules, err := s.Store.ListRuleRecords(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rules == nil {
		rules = []taxonomy.RuleRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

type rulePayload struct {
	Category     string `json:"category"`
	Subcategory  string `json:"subcategory"`
	Organization string `json:"organization"`
	Keywords     string `json:"keywords"`
	PathTemplate string `json:"path_template"`
	Description  string `json:"description"`
	Confirmed    *bool  `json:"confirmed"`
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "taxonomy database not configured")
		return
	}
	var p rulePayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(p.Category) == "" || strings.TrimSpace(p.Organization) == "" {
		writeErr(w, http.StatusBadRequest, "category and organization are required")
		return
	}
	confirmed := true
	if p.Confirmed != nil {
		confirmed = *p.Confirmed
	}
	err := s.Store.UpsertRuleWithConfidence(r.Context(), taxonomy.Rule{
		Category:     strings.TrimSpace(p.Category),
		Subcategory:  strings.TrimSpace(p.Subcategory),
		Organization: strings.TrimSpace(p.Organization),
		Keywords:     strings.TrimSpace(p.Keywords),
		PathTemplate: strings.TrimSpace(p.PathTemplate),
		Description:  strings.TrimSpace(p.Description),
	}, "ui", 1.0, confirmed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "saved"})
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "taxonomy database not configured")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	var p rulePayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	confirmed := true
	if p.Confirmed != nil {
		confirmed = *p.Confirmed
	}
	err = s.Store.UpdateRuleByID(r.Context(), id, taxonomy.Rule{
		Category:     strings.TrimSpace(p.Category),
		Subcategory:  strings.TrimSpace(p.Subcategory),
		Organization: strings.TrimSpace(p.Organization),
		Keywords:     strings.TrimSpace(p.Keywords),
		PathTemplate: strings.TrimSpace(p.PathTemplate),
		Description:  strings.TrimSpace(p.Description),
	}, confirmed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "taxonomy database not configured")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	if err := s.Store.DeleteRuleByID(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleMergeRules(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "taxonomy database not configured")
		return
	}
	var req struct {
		SourceID int64 `json:"source_id"`
		TargetID int64 `json:"target_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SourceID <= 0 || req.TargetID <= 0 {
		writeErr(w, http.StatusBadRequest, "source_id and target_id are required")
		return
	}
	if err := s.Store.MergeRules(r.Context(), req.SourceID, req.TargetID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "merged"})
}

func (s *Server) handleListHistory(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "taxonomy database not configured")
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	items, err := s.Store.ListHistoryRecords(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []taxonomy.HistoryRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": items})
}

func (s *Server) handleMarkReviewed(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "taxonomy database not configured")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid history id")
		return
	}
	if err := s.Store.MarkHistoryReviewed(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reviewed"})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "taxonomy database not configured")
		return
	}
	var req struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Query) == "" {
		writeErr(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.TopK <= 0 {
		req.TopK = 8
	}
	res, err := s.Store.Search(r.Context(), req.Query, req.TopK, 5)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "taxonomy database not configured")
		return
	}
	csvBytes, err := s.Store.ExportCSV(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="taxonomy.csv"`)
	_, _ = w.Write(csvBytes)
}

func (s *Server) handleScanNow(w http.ResponseWriter, _ *http.Request) {
	if s.Hooks.TriggerScan == nil {
		writeErr(w, http.StatusNotImplemented, "scan trigger not available in this mode")
		return
	}
	triggered := s.Hooks.TriggerScan()
	writeJSON(w, http.StatusOK, map[string]any{"triggered": triggered})
}

func (s *Server) handleTidy(w http.ResponseWriter, r *http.Request) {
	if s.Hooks.RunTidy == nil {
		writeErr(w, http.StatusNotImplemented, "AI tidy not configured")
		return
	}
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	res, err := s.Hooks.RunTidy(r.Context(), req.DryRun)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"actions": res.Actions,
		"applied": res.Applied,
		"report":  maint.FormatTidyReport(res, req.DryRun),
	})
}

func (s *Server) handleReorg(w http.ResponseWriter, r *http.Request) {
	if s.Hooks.RunReorg == nil {
		writeErr(w, http.StatusNotImplemented, "storage reorg not configured")
		return
	}
	var req struct {
		DryRun            bool `json:"dry_run"`
		SanitizeFilenames bool `json:"sanitize_filenames"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	res, err := s.Hooks.RunReorg(r.Context(), req.DryRun, req.SanitizeFilenames)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"result": res,
		"report": maint.FormatReorgReport(res, req.DryRun),
	})
}
