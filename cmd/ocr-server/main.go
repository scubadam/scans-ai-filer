package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	addr := strings.TrimSpace(os.Getenv("OCR_LISTEN_ADDR"))
	if addr == "" {
		addr = ":8085"
	}
	defaultLang := strings.TrimSpace(os.Getenv("OCR_LANG"))
	if defaultLang == "" {
		defaultLang = "eng"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /ocr", func(w http.ResponseWriter, r *http.Request) {
		imgBytes, err := io.ReadAll(io.LimitReader(r.Body, 25<<20))
		if err != nil || len(imgBytes) == 0 {
			http.Error(w, "empty or unreadable image body", http.StatusBadRequest)
			return
		}
		lang := strings.TrimSpace(r.URL.Query().Get("lang"))
		if lang == "" {
			lang = defaultLang
		}

		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "tesseract", "stdin", "stdout", "-l", lang, "--psm", "3")
		cmd.Stdin = bytes.NewReader(imgBytes)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			logger.Error("tesseract failed", "err", err, "stderr", strings.TrimSpace(stderr.String()))
			http.Error(w, "ocr failed", http.StatusInternalServerError)
			return
		}

		text := strings.TrimSpace(stdout.String())
		logger.Info("ocr completed", "bytes_in", len(imgBytes), "chars_out", len(text), "lang", lang)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": text})
	})

	mux.HandleFunc("POST /ocr/pdf", func(w http.ResponseWriter, r *http.Request) {
		imgBytes, err := io.ReadAll(io.LimitReader(r.Body, 25<<20))
		if err != nil || len(imgBytes) == 0 {
			http.Error(w, "empty or unreadable image body", http.StatusBadRequest)
			return
		}
		lang := strings.TrimSpace(r.URL.Query().Get("lang"))
		if lang == "" {
			lang = defaultLang
		}

		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "tesseract", "stdin", "stdout", "-l", lang, "--psm", "3", "pdf")
		cmd.Stdin = bytes.NewReader(imgBytes)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			logger.Error("tesseract pdf failed", "err", err, "stderr", strings.TrimSpace(stderr.String()))
			http.Error(w, "ocr pdf failed", http.StatusInternalServerError)
			return
		}

		pdfBytes := stdout.Bytes()
		logger.Info("ocr pdf completed", "bytes_in", len(imgBytes), "pdf_bytes_out", len(pdfBytes), "lang", lang)
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdfBytes)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	logger.Info("starting tesseract ocr sidecar", "addr", addr, "lang", defaultLang)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("ocr server exited with error", "err", err)
		os.Exit(1)
	}
}
