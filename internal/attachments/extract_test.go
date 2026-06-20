package attachments

import (
	"errors"
	"testing"
)

func TestExtractTextFromMarkdownFixture(t *testing.T) {
	text, err := ExtractText(ExtractRequest{
		Filename:    "notes.md",
		ContentType: "text/markdown; charset=utf-8",
		Data:        []byte("# Notes\n\nDeploy after review.\n"),
	})
	if err != nil {
		t.Fatalf("ExtractText returned error: %v", err)
	}
	if text != "# Notes\n\nDeploy after review." {
		t.Fatalf("unexpected text %q", text)
	}
}

func TestExtractTextRejectsBinaryContent(t *testing.T) {
	_, err := ExtractText(ExtractRequest{
		Filename: "image.txt",
		Data:     []byte{0x00, 0x01, 0x02},
	})
	if !errors.Is(err, ErrBinaryContent) {
		t.Fatalf("expected ErrBinaryContent, got %v", err)
	}
}

func TestExtractTextRejectsUnsupportedType(t *testing.T) {
	_, err := ExtractText(ExtractRequest{
		Filename:    "archive.zip",
		ContentType: "application/zip",
		Data:        []byte("not really a zip"),
	})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("expected ErrUnsupportedType, got %v", err)
	}
}

func TestExtractTextHonorsSizeLimit(t *testing.T) {
	_, err := ExtractText(ExtractRequest{
		Filename: "large.txt",
		Data:     []byte("too large"),
		MaxBytes: 3,
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}
