package pdfenc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// EncryptDocument encrypts a PDF (or wraps a JPEG/PNG/WebP/TIFF image into a single-page PDF)
// using standard PDF AES-256 password protection compatible with Google Drive's native PDF viewer.
func EncryptDocument(ctx context.Context, rawBytes []byte, mimeType, userPW, ownerPW string) ([]byte, error) {
	if strings.TrimSpace(userPW) == "" {
		return nil, fmt.Errorf("user password must not be empty")
	}
	if strings.TrimSpace(ownerPW) == "" {
		ownerPW = userPW
	}

	pdfBytes := rawBytes
	normalizedMime := strings.ToLower(strings.TrimSpace(mimeType))

	if normalizedMime != "application/pdf" {
		if !isSupportedImageMime(normalizedMime) {
			return nil, fmt.Errorf("unsupported mime type for PDF encryption: %q", mimeType)
		}
		wrapped, err := wrapImageAsPDF(ctx, rawBytes)
		if err != nil {
			return nil, fmt.Errorf("wrap image as PDF: %w", err)
		}
		pdfBytes = wrapped
	}

	encConf, err := newStatelessAES256Config(userPW, ownerPW)
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	if err := api.Encrypt(ctx, bytes.NewReader(pdfBytes), &out, encConf); err != nil {
		return nil, fmt.Errorf("encrypt PDF with AES-256: %w", err)
	}
	return out.Bytes(), nil
}

// DecryptDocument decrypts an AES-encrypted PDF (primarily used for verification & testing).
func DecryptDocument(ctx context.Context, encBytes []byte, password string) ([]byte, error) {
	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil {
		return nil, fmt.Errorf("load stateless pdfcpu config: %w", err)
	}
	conf.ValidationMode = model.ValidationRelaxed
	conf.UserPW = password
	conf.OwnerPW = password

	var out bytes.Buffer
	if err := api.Decrypt(ctx, bytes.NewReader(encBytes), &out, conf); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func wrapImageAsPDF(ctx context.Context, imgBytes []byte) ([]byte, error) {
	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil {
		return nil, fmt.Errorf("load stateless pdfcpu config: %w", err)
	}
	conf.ValidationMode = model.ValidationRelaxed

	var pdfBuf bytes.Buffer
	if err := api.ImportImages(ctx, nil, &pdfBuf, []io.Reader{bytes.NewReader(imgBytes)}, nil, conf); err != nil {
		return nil, err
	}
	return pdfBuf.Bytes(), nil
}

func newStatelessAES256Config(userPW, ownerPW string) (*model.Configuration, error) {
	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil {
		return nil, fmt.Errorf("load stateless pdfcpu config: %w", err)
	}
	conf.ValidationMode = model.ValidationRelaxed
	conf.EncryptUsingAES = true
	conf.EncryptKeyLength = 256
	conf.UserPW = userPW
	conf.OwnerPW = ownerPW
	conf.Permissions = model.PermissionsAll
	return conf, nil
}

func isSupportedImageMime(mimeType string) bool {
	switch mimeType {
	case "image/jpeg", "image/jpg", "image/png", "image/webp", "image/tiff":
		return true
	default:
		return false
	}
}
