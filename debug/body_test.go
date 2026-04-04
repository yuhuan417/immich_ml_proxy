package debug

import (
	"bytes"
	"io"
	"mime/multipart"
	"strings"
	"testing"
)

func TestBuildBodyInfoJSONPreview(t *testing.T) {
	body := []byte(`{"alpha":1,"nested":{"beta":true}}`)

	info := BuildBodyInfo("application/json; charset=utf-8", body)

	if info.Kind != "json" {
		t.Fatalf("expected json kind, got %q", info.Kind)
	}
	if info.ContentType != "application/json" {
		t.Fatalf("expected normalized content type, got %q", info.ContentType)
	}
	if info.Size != len(body) {
		t.Fatalf("expected size %d, got %d", len(body), info.Size)
	}
	if info.MD5 == "" {
		t.Fatal("expected md5 to be populated")
	}
	if !strings.Contains(info.Preview, "\"nested\": {") {
		t.Fatalf("expected pretty-printed preview, got %q", info.Preview)
	}
	if info.Truncated {
		t.Fatal("did not expect preview to be truncated")
	}
}

func TestBuildBodyInfoMultipartSummary(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := writer.WriteField("entries", `{"clip":{"textual":["hello"]}}`); err != nil {
		t.Fatalf("write field: %v", err)
	}

	part, err := writer.CreateFormFile("asset", "photo.jpg")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.WriteString(part, "image-bytes"); err != nil {
		t.Fatalf("write form file: %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	info := BuildBodyInfo(writer.FormDataContentType(), body.Bytes())

	if info.Kind != "multipart" {
		t.Fatalf("expected multipart kind, got %q", info.Kind)
	}
	if info.Multipart == nil {
		t.Fatal("expected multipart details to be populated")
	}
	if len(info.Multipart.Fields) != 1 {
		t.Fatalf("expected 1 multipart field, got %d", len(info.Multipart.Fields))
	}
	if len(info.Multipart.Files) != 1 {
		t.Fatalf("expected 1 multipart file, got %d", len(info.Multipart.Files))
	}
	if got := info.Multipart.Fields[0].Name; got != "entries" {
		t.Fatalf("expected field name entries, got %q", got)
	}
	if !strings.Contains(info.Multipart.Fields[0].Preview, "\"clip\": {") {
		t.Fatalf("expected JSON preview in multipart field, got %q", info.Multipart.Fields[0].Preview)
	}
	if got := info.Multipart.Files[0].Filename; got != "photo.jpg" {
		t.Fatalf("expected filename photo.jpg, got %q", got)
	}
	if !strings.Contains(info.Summary, "1 field(s), 1 file(s)") {
		t.Fatalf("unexpected summary: %q", info.Summary)
	}
}

func TestBuildBodyInfoBinaryAndTruncation(t *testing.T) {
	binaryInfo := BuildBodyInfo("image/png", []byte{0x89, 'P', 'N', 'G'})
	if binaryInfo.Kind != "binary" {
		t.Fatalf("expected binary kind, got %q", binaryInfo.Kind)
	}
	if binaryInfo.Preview != "" {
		t.Fatalf("did not expect preview for binary body, got %q", binaryInfo.Preview)
	}

	textInfo := BuildBodyInfo("text/plain", []byte(strings.Repeat("a", bodyPreviewLimit+32)))
	if !textInfo.Truncated {
		t.Fatal("expected long text preview to be truncated")
	}
	if !strings.Contains(textInfo.Preview, "[truncated]") {
		t.Fatalf("expected truncation marker in preview, got %q", textInfo.Preview)
	}
}
