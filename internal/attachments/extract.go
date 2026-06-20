package attachments

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	ErrUnsupportedType = errors.New("attachment type is not supported for text extraction")
	ErrTooLarge        = errors.New("attachment exceeds extraction size limit")
	ErrBinaryContent   = errors.New("attachment appears to contain binary content")
)

type ExtractRequest struct {
	Filename    string
	ContentType string
	Data        []byte
	MaxBytes    int
}

func ExtractText(request ExtractRequest) (string, error) {
	maxBytes := request.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	if len(request.Data) > maxBytes {
		return "", ErrTooLarge
	}
	if !supported(request.Filename, request.ContentType) {
		return "", ErrUnsupportedType
	}
	if bytes.IndexByte(request.Data, 0) >= 0 || !utf8.Valid(request.Data) {
		return "", ErrBinaryContent
	}
	return strings.TrimSpace(string(request.Data)), nil
}

func supported(filename, contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if strings.HasPrefix(contentType, "text/") {
		return true
	}
	switch contentType {
	case "application/json", "application/x-ndjson", "application/xml", "application/yaml", "application/x-yaml":
		return true
	}

	switch strings.ToLower(filepath.Ext(filename)) {
	case ".txt", ".md", ".markdown", ".log", ".json", ".jsonl", ".csv", ".tsv", ".xml", ".yaml", ".yml":
		return true
	default:
		return false
	}
}
