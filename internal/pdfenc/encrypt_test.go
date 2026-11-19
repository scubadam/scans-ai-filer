package pdfenc

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func makeTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: 30, G: 120, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buf.Bytes()
}

func makeTestJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	return buf.Bytes()
}

func TestEncryptImageAndPDFRoundTrip(t *testing.T) {
	ctx := context.Background()
	const pw = "test-strong-passphrase-2026!"

	// 1. Wrap PNG image into AES-256 encrypted PDF
	encPNG, err := EncryptDocument(ctx, makeTestPNG(t), "image/png", pw, "")
	if err != nil {
		t.Fatalf("EncryptDocument PNG failed: %v", err)
	}
	if !bytes.HasPrefix(encPNG, []byte("%PDF-")) {
		t.Fatalf("expected PDF header, got %q", encPNG[:min(16, len(encPNG))])
	}

	// Decrypting with wrong password must fail
	if _, err := DecryptDocument(ctx, encPNG, "wrong-password"); err == nil {
		t.Fatalf("expected decryption with wrong password to fail")
	}

	// Decrypting with right password must succeed
	plainPDF, err := DecryptDocument(ctx, encPNG, pw)
	if err != nil {
		t.Fatalf("DecryptDocument with valid password failed: %v", err)
	}
	if !bytes.HasPrefix(plainPDF, []byte("%PDF-")) {
		t.Fatalf("decrypted output missing PDF header")
	}

	// 2. Encrypt existing PDF directly with AES-256
	reEncrypted, err := EncryptDocument(ctx, plainPDF, "application/pdf", pw, "owner-pw-2026!")
	if err != nil {
		t.Fatalf("EncryptDocument PDF failed: %v", err)
	}
	if _, err := DecryptDocument(ctx, reEncrypted, pw); err != nil {
		t.Fatalf("DecryptDocument on re-encrypted PDF failed: %v", err)
	}

	// 3. Wrap JPEG image into AES-256 encrypted PDF
	encJPG, err := EncryptDocument(ctx, makeTestJPEG(t), "image/jpeg", pw, "")
	if err != nil {
		t.Fatalf("EncryptDocument JPEG failed: %v", err)
	}
	if _, err := DecryptDocument(ctx, encJPG, pw); err != nil {
		t.Fatalf("DecryptDocument on encrypted JPEG-PDF failed: %v", err)
	}
}
