package debug

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	bodyPreviewLimit           = 4096
	multipartFieldPreviewLimit = 2048
)

// BodyInfo stores a safe, structured view of a request or response body.
type BodyInfo struct {
	Kind        string         `json:"kind"`
	ContentType string         `json:"contentType,omitempty"`
	Size        int            `json:"size"`
	MD5         string         `json:"md5,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	Preview     string         `json:"preview,omitempty"`
	Truncated   bool           `json:"truncated,omitempty"`
	Error       string         `json:"error,omitempty"`
	Multipart   *MultipartInfo `json:"multipart,omitempty"`
}

// MultipartInfo summarizes multipart form fields and files without dumping raw bytes.
type MultipartInfo struct {
	Fields []MultipartFieldInfo `json:"fields,omitempty"`
	Files  []MultipartFileInfo  `json:"files,omitempty"`
}

// MultipartFieldInfo stores a text form field preview.
type MultipartFieldInfo struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType,omitempty"`
	Size        int    `json:"size"`
	MD5         string `json:"md5,omitempty"`
	Preview     string `json:"preview,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// MultipartFileInfo stores file metadata for quick integrity checks.
type MultipartFileInfo struct {
	Name        string `json:"name"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Size        int    `json:"size"`
	MD5         string `json:"md5,omitempty"`
}

// BuildBodyInfo converts raw body bytes into a compact summary for the debug UI.
func BuildBodyInfo(rawContentType string, body []byte) BodyInfo {
	info := BodyInfo{
		Kind: "empty",
		Size: len(body),
	}

	if len(body) == 0 {
		info.Summary = "Empty body"
		return info
	}

	info.MD5 = md5Hex(body)
	info.ContentType = normalizeContentType(rawContentType)
	if info.ContentType == "" {
		info.ContentType = normalizeContentType(http.DetectContentType(sniffBytes(body)))
	}

	switch {
	case strings.HasPrefix(info.ContentType, "multipart/form-data"):
		info.Kind = "multipart"
		multipartInfo, err := parseMultipartInfo(rawContentType, body)
		if err != nil {
			info.Error = err.Error()
			info.Summary = fmt.Sprintf("Multipart body, %d bytes", len(body))
			return info
		}
		info.Multipart = multipartInfo
		info.Summary = fmt.Sprintf(
			"Multipart body, %d field(s), %d file(s)",
			len(multipartInfo.Fields),
			len(multipartInfo.Files),
		)
	case isLikelyText(info.ContentType, body):
		info.Kind = bodyKindForText(info.ContentType)
		info.Preview, info.Truncated = buildTextPreview(body, info.ContentType, bodyPreviewLimit)
		info.Summary = fmt.Sprintf("%s body, %d bytes", friendlyKind(info.Kind), len(body))
	default:
		info.Kind = "binary"
		info.Summary = fmt.Sprintf("Binary body, %d bytes", len(body))
	}

	return info
}

func parseMultipartInfo(rawContentType string, body []byte) (*MultipartInfo, error) {
	_, params, err := mime.ParseMediaType(rawContentType)
	if err != nil {
		return nil, err
	}

	boundary := params["boundary"]
	if boundary == "" {
		return nil, fmt.Errorf("multipart boundary missing")
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	info := &MultipartInfo{}

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return info, nil
		}
		if err != nil {
			return nil, err
		}

		partBody, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}

		partContentType := normalizeContentType(part.Header.Get("Content-Type"))
		if partContentType == "" {
			partContentType = normalizeContentType(http.DetectContentType(sniffBytes(partBody)))
		}

		if filename := part.FileName(); filename != "" || !isLikelyText(partContentType, partBody) {
			info.Files = append(info.Files, MultipartFileInfo{
				Name:        fallbackPartName(part.FormName(), "file"),
				Filename:    filename,
				ContentType: partContentType,
				Size:        len(partBody),
				MD5:         md5Hex(partBody),
			})
			continue
		}

		preview, truncated := buildTextPreview(partBody, partContentType, multipartFieldPreviewLimit)
		info.Fields = append(info.Fields, MultipartFieldInfo{
			Name:        fallbackPartName(part.FormName(), "field"),
			ContentType: partContentType,
			Size:        len(partBody),
			MD5:         md5Hex(partBody),
			Preview:     preview,
			Truncated:   truncated,
		})
	}
}

func buildTextPreview(body []byte, contentType string, limit int) (string, bool) {
	previewBytes := body
	if shouldPrettyPrintJSON(contentType, body) {
		var formatted bytes.Buffer
		if err := json.Indent(&formatted, bytes.TrimSpace(body), "", "  "); err == nil {
			previewBytes = formatted.Bytes()
		}
	}

	return trimPreview(string(previewBytes), limit)
}

func shouldPrettyPrintJSON(contentType string, body []byte) bool {
	if strings.HasSuffix(contentType, "+json") || strings.Contains(contentType, "json") {
		return json.Valid(bytes.TrimSpace(body))
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return false
	}

	first := trimmed[0]
	if first != '{' && first != '[' {
		return false
	}

	return json.Valid(trimmed)
}

func bodyKindForText(contentType string) string {
	switch {
	case strings.HasSuffix(contentType, "+json") || strings.Contains(contentType, "json"):
		return "json"
	case strings.HasSuffix(contentType, "+xml") || strings.Contains(contentType, "xml"):
		return "xml"
	default:
		return "text"
	}
}

func friendlyKind(kind string) string {
	switch kind {
	case "json":
		return "JSON"
	case "xml":
		return "XML"
	case "text":
		return "Text"
	default:
		if kind == "" {
			return "Unknown"
		}
		return strings.ToUpper(kind[:1]) + kind[1:]
	}
}

func isLikelyText(contentType string, body []byte) bool {
	switch {
	case strings.HasPrefix(contentType, "text/"):
		return true
	case strings.Contains(contentType, "json"),
		strings.Contains(contentType, "xml"),
		strings.Contains(contentType, "javascript"),
		strings.Contains(contentType, "x-www-form-urlencoded"):
		return true
	case strings.HasPrefix(contentType, "image/"),
		strings.HasPrefix(contentType, "video/"),
		strings.HasPrefix(contentType, "audio/"),
		strings.Contains(contentType, "octet-stream"),
		strings.HasPrefix(contentType, "multipart/"):
		return false
	}

	if len(body) == 0 || !utf8.Valid(body) {
		return false
	}

	nonPrintable := 0
	totalRunes := 0
	for _, r := range string(body) {
		totalRunes++
		switch {
		case unicode.IsPrint(r), unicode.IsSpace(r):
		default:
			nonPrintable++
		}
	}

	if totalRunes == 0 {
		return true
	}

	return float64(nonPrintable)/float64(totalRunes) < 0.1
}

func normalizeContentType(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return ""
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil {
		return strings.ToLower(mediaType)
	}

	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

func trimPreview(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}

	return string(runes[:limit]) + "\n... [truncated]", true
}

func md5Hex(body []byte) string {
	sum := md5.Sum(body)
	return hex.EncodeToString(sum[:])
}

func sniffBytes(body []byte) []byte {
	if len(body) <= 512 {
		return body
	}
	return body[:512]
}

func fallbackPartName(name string, fallback string) string {
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return name
}
