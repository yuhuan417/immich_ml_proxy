package proxy

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"reflect"
	"slices"
	"strings"
	"testing"

	"immich_ml_proxy/tasks"
)

func buildMultipartRequest(t *testing.T, fields map[string]string, fileField string, fileHeader textproto.MIMEHeader, fileBody string) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}

	if fileField != "" {
		var (
			part io.Writer
			err  error
		)
		if len(fileHeader) > 0 {
			part, err = writer.CreatePart(fileHeader)
		} else {
			part, err = writer.CreateFormFile(fileField, "upload.bin")
		}
		if err != nil {
			t.Fatalf("create form file %s: %v", fileField, err)
		}
		if _, err := io.WriteString(part, fileBody); err != nil {
			t.Fatalf("write form file %s: %v", fileField, err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/predict", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestParseEntriesFromRequest(t *testing.T) {
	req := buildMultipartRequest(t, map[string]string{
		"entries": `{"clip":{"textual":["hello"]}}`,
	}, "", nil, "")

	entries, err := ParseEntriesFromRequest(req)
	if err != nil {
		t.Fatalf("parse entries: %v", err)
	}

	clipTask, ok := entries["clip"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected clip task map, got %#v", entries["clip"])
	}
	values, ok := clipTask["textual"].([]interface{})
	if !ok || len(values) != 1 || values[0] != "hello" {
		t.Fatalf("unexpected textual entries: %#v", clipTask["textual"])
	}
}

func TestParseEntriesFromRequestRequiresEntriesField(t *testing.T) {
	req := buildMultipartRequest(t, map[string]string{
		"note": "missing entries",
	}, "", nil, "")

	if _, err := ParseEntriesFromRequest(req); err == nil {
		t.Fatal("expected missing entries field to return an error")
	}
}

func TestParseEntriesAndBuildDispatchGroups(t *testing.T) {
	entriesMap := map[string]interface{}{
		tasks.LegacyFacialRecognitionTask: map[string]interface{}{
			"recognition": map[string]interface{}{"id": "face-1"},
		},
		tasks.ClipTask: map[string]interface{}{
			"textual": []interface{}{"hello"},
			"visual":  []interface{}{"image"},
		},
	}

	entries, err := ParseEntries(entriesMap)
	if err != nil {
		t.Fatalf("parse entries: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	normalized := false
	for _, entry := range entries {
		if entry.Task == tasks.FacialRecognitionTask {
			normalized = true
			break
		}
	}
	if !normalized {
		t.Fatal("expected legacy facial recognition task to be normalized")
	}

	groups := BuildDispatchGroups(entries)
	groupByKey := make(map[string]DispatchGroup, len(groups))
	for _, group := range groups {
		groupByKey[group.Key] = group
	}

	if group, ok := groupByKey["clip:textual"]; !ok || !group.Split {
		t.Fatalf("expected split textual clip group, got %#v", group)
	}
	if group, ok := groupByKey["clip:visual"]; !ok || !group.Split {
		t.Fatalf("expected split visual clip group, got %#v", group)
	}
	if group, ok := groupByKey[tasks.FacialRecognitionTask]; !ok || group.Split {
		t.Fatalf("expected grouped facial recognition dispatch, got %#v", group)
	}
}

func TestBuildEntriesForDispatchGroupAndBackendSelection(t *testing.T) {
	entries := []Entry{
		{Task: tasks.ClipTask, Type: "textual", EntryData: map[string]interface{}{"prompt": "hello"}},
		{Task: tasks.ClipTask, Type: "visual", EntryData: map[string]interface{}{"assetId": "img-1"}},
	}

	splitPayload, err := BuildEntriesForDispatchGroup(DispatchGroup{
		Key:     "clip:textual",
		Task:    tasks.ClipTask,
		Type:    "textual",
		Entries: entries[:1],
		Split:   true,
	})
	if err != nil {
		t.Fatalf("build split payload: %v", err)
	}

	expectedSplit := map[string]interface{}{
		tasks.ClipTask: map[string]interface{}{
			"textual": map[string]interface{}{"prompt": "hello"},
		},
	}
	if !reflect.DeepEqual(splitPayload, expectedSplit) {
		t.Fatalf("unexpected split payload: %#v", splitPayload)
	}

	groupedPayload, err := BuildEntriesForDispatchGroup(DispatchGroup{
		Key:     tasks.ClipTask,
		Task:    tasks.ClipTask,
		Entries: entries,
	})
	if err != nil {
		t.Fatalf("build grouped payload: %v", err)
	}

	expectedGrouped := map[string]interface{}{
		tasks.ClipTask: map[string]interface{}{
			"textual": map[string]interface{}{"prompt": "hello"},
			"visual":  map[string]interface{}{"assetId": "img-1"},
		},
	}
	if !reflect.DeepEqual(groupedPayload, expectedGrouped) {
		t.Fatalf("unexpected grouped payload: %#v", groupedPayload)
	}

	url := GetBackendURLForType(entries, func(task string) string {
		switch task {
		case tasks.ClipTask:
			return "http://task-backend"
		case "":
			return "http://default-backend"
		default:
			return ""
		}
	})
	if url != "http://task-backend" {
		t.Fatalf("expected task-specific backend url, got %q", url)
	}
}

func TestRoundRobinAndExtractTaskTypes(t *testing.T) {
	balancer := NewRoundRobinBalancer()
	backends := []string{"http://a", "http://b"}

	if got := balancer.GetNextBackend(tasks.ClipTask, backends); got != "http://a" {
		t.Fatalf("expected first backend http://a, got %q", got)
	}
	if got := balancer.GetNextBackend(tasks.ClipTask, backends); got != "http://b" {
		t.Fatalf("expected second backend http://b, got %q", got)
	}
	if got := balancer.GetNextBackend(tasks.ClipTask, backends); got != "http://a" {
		t.Fatalf("expected round-robin to wrap, got %q", got)
	}
	if got := balancer.GetNextBackend(tasks.ClipTask, []string{"http://a"}); got != "http://a" {
		t.Fatalf("expected round-robin to recover when backend list shrinks, got %q", got)
	}
	if got := GetNextBackend("unique-test-type", []string{"http://only"}); got != "http://only" {
		t.Fatalf("expected global round-robin helper to return only backend, got %q", got)
	}

	taskTypes := ExtractTaskTypes(map[string]interface{}{
		tasks.LegacyFacialRecognitionTask: map[string]interface{}{},
		tasks.ClipTask:                    map[string]interface{}{},
	})
	slices.Sort(taskTypes)
	if !reflect.DeepEqual(taskTypes, []string{tasks.ClipTask, tasks.FacialRecognitionTask}) {
		t.Fatalf("unexpected extracted task types: %#v", taskTypes)
	}
}

func TestCopyForwardHeadersAndCloneMIMEHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://example.com", nil)
	sourceHeaders := http.Header{
		"Content-Length": []string{"12"},
		"Content-Type":   []string{"text/plain"},
		"Host":           []string{"example.com"},
		"X-Test":         []string{"alpha", "beta"},
	}

	copyForwardHeaders(req, sourceHeaders, "multipart/form-data; boundary=test")

	if got := req.Header.Values("X-Test"); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("expected copied X-Test header, got %#v", got)
	}
	if got := req.Header.Get("Content-Type"); got != "multipart/form-data; boundary=test" {
		t.Fatalf("expected replacement content type, got %q", got)
	}
	if req.Header.Get("Content-Length") != "" {
		t.Fatal("did not expect Content-Length to be forwarded")
	}
	if req.Header.Get("Host") != "" {
		t.Fatal("did not expect Host header to be forwarded")
	}

	sourceHeaders.Set("X-Test", "changed")
	if got := req.Header.Values("X-Test"); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("expected forwarded headers to be a deep copy, got %#v", got)
	}

	header := textproto.MIMEHeader{
		"Content-Disposition": []string{`form-data; name="asset"; filename="photo.jpg"`},
		"Content-Type":        []string{"image/jpeg"},
	}
	cloned := cloneMIMEHeader(header)
	header.Set("Content-Type", "image/png")
	if got := cloned.Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("expected cloned MIME header to be independent, got %q", got)
	}
}

func TestForwardPredictRequestWithType(t *testing.T) {
	newEntriesJSON := `{"clip":{"textual":["updated"]}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/predict" {
			t.Fatalf("expected /predict path, got %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Test"); got != "proxy" {
			t.Fatalf("expected X-Test header to be forwarded, got %q", got)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
			t.Fatalf("expected multipart content type, got %q", r.Header.Get("Content-Type"))
		}

		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatalf("parse forwarded multipart form: %v", err)
		}
		if got := r.FormValue("entries"); got != newEntriesJSON {
			t.Fatalf("expected rewritten entries JSON, got %q", got)
		}
		if got := r.FormValue("note"); got != "keep-me" {
			t.Fatalf("expected note field to be preserved, got %q", got)
		}

		file, header, err := r.FormFile("asset")
		if err != nil {
			t.Fatalf("expected forwarded file: %v", err)
		}
		defer file.Close()

		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("read forwarded file: %v", err)
		}
		if header.Filename != "photo.jpg" {
			t.Fatalf("expected forwarded filename photo.jpg, got %q", header.Filename)
		}
		if string(data) != "image-bytes" {
			t.Fatalf("expected forwarded file body, got %q", string(data))
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	fileHeader := textproto.MIMEHeader{
		"Content-Disposition": []string{`form-data; name="asset"; filename="photo.jpg"`},
		"Content-Type":        []string{"image/jpeg"},
	}
	req := buildMultipartRequest(t, map[string]string{
		"entries": `{"clip":{"textual":["original"]}}`,
		"note":    "keep-me",
	}, "asset", fileHeader, "image-bytes")
	req.Header.Set("X-Test", "proxy")

	resp, bodyBytes, contentType, err := ForwardPredictRequestWithType(server.URL, req, newEntriesJSON)
	if err != nil {
		t.Fatalf("forward predict request with type: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if !strings.HasPrefix(contentType, "multipart/form-data; boundary=") {
		t.Fatalf("expected returned multipart content type, got %q", contentType)
	}
	if !bytes.Contains(bodyBytes, []byte(newEntriesJSON)) {
		t.Fatalf("expected forwarded body to contain rewritten entries, got %q", string(bodyBytes))
	}
	if bytes.Contains(bodyBytes, []byte(`{"clip":{"textual":["original"]}}`)) {
		t.Fatalf("did not expect forwarded body to contain original entries, got %q", string(bodyBytes))
	}
}

func TestForwardPredictRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/predict" {
			t.Fatalf("expected /predict path, got %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatalf("parse forwarded multipart form: %v", err)
		}
		if got := r.FormValue("entries"); got != `{"clip":{"textual":["original"]}}` {
			t.Fatalf("expected original entries to be forwarded, got %q", got)
		}
		if got := r.FormValue("note"); got != "keep-me" {
			t.Fatalf("expected note field to be preserved, got %q", got)
		}

		file, header, err := r.FormFile("asset")
		if err != nil {
			t.Fatalf("expected forwarded file: %v", err)
		}
		defer file.Close()

		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("read forwarded file: %v", err)
		}
		if header.Filename != "photo.jpg" {
			t.Fatalf("expected filename photo.jpg, got %q", header.Filename)
		}
		if string(data) != "image-bytes" {
			t.Fatalf("expected forwarded file body, got %q", string(data))
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	fileHeader := textproto.MIMEHeader{
		"Content-Disposition": []string{`form-data; name="asset"; filename="photo.jpg"`},
		"Content-Type":        []string{"image/jpeg"},
	}
	req := buildMultipartRequest(t, map[string]string{
		"entries": `{"clip":{"textual":["original"]}}`,
		"note":    "keep-me",
	}, "asset", fileHeader, "image-bytes")

	resp, err := ForwardPredictRequest(server.URL, req)
	if err != nil {
		t.Fatalf("forward predict request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestForwardRequestAndCheckBackendHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ping":
			_, _ = w.Write([]byte("pong"))
		case "/echo":
			if got := r.Header.Get("X-Test"); got != "value" {
				t.Fatalf("expected forwarded header, got %q", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read forwarded body: %v", err)
			}
			if string(body) != "payload" {
				t.Fatalf("expected forwarded body payload, got %q", string(body))
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	status := CheckBackendHealth(server.URL)
	if status.Status != "healthy" {
		t.Fatalf("expected healthy backend status, got %+v", status)
	}

	resp, err := ForwardRequest(server.URL, http.MethodPut, "/echo", http.Header{
		"X-Test": []string{"value"},
	}, strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("forward request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", resp.StatusCode)
	}
}
