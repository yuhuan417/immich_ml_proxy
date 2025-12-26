package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

type BackendStatus struct {
	URL    string `json:"url"`
	Status string `json:"status"` // "healthy" or "unhealthy"
	Error  string `json:"error,omitempty"`
}

// ForwardRequest forwards the HTTP request to the specified backend server
func ForwardRequest(backendURL string, method string, path string, header http.Header, body io.Reader) (*http.Response, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	targetURL := backendURL + path
	req, err := http.NewRequest(method, targetURL, body)
	if err != nil {
		return nil, err
	}

	// Copy headers, excluding hop-by-hop headers
	for key, values := range header {
		if key == "Host" || key == "Content-Length" {
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	return client.Do(req)
}

// CheckBackendHealth checks if a backend server is healthy by calling its /ping endpoint
func CheckBackendHealth(backendURL string) BackendStatus {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(backendURL + "/ping")
	if err != nil {
		return BackendStatus{
			URL:    backendURL,
			Status: "unhealthy",
			Error:  err.Error(),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if string(body) == "pong" {
			return BackendStatus{
				URL:    backendURL,
				Status: "healthy",
			}
		}
	}

	return BackendStatus{
		URL:    backendURL,
		Status: "unhealthy",
		Error:  fmt.Sprintf("unexpected response: %s", resp.Status),
	}
}

// ParseEntriesFromRequest parses the entries form field from the predict request
func ParseEntriesFromRequest(r *http.Request) (map[string]interface{}, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, err
	}

	entriesStr := r.FormValue("entries")
	if entriesStr == "" {
		return nil, fmt.Errorf("entries field is required")
	}

	var entries map[string]interface{}
	if err := json.Unmarshal([]byte(entriesStr), &entries); err != nil {
		return nil, err
	}

	return entries, nil
}

// ExtractTaskTypes extracts task types from the entries map
func ExtractTaskTypes(entries map[string]interface{}) []string {
	tasks := make([]string, 0, len(entries))
	for task := range entries {
		tasks = append(tasks, task)
	}
	return tasks
}

// Entry represents a single inference entry with task and type information
type Entry struct {
	Task string
	Type string
	// The original nested structure
	EntryData interface{}
	// Index in the original order
	Index int
}

// ParseEntries parses entries and returns a flattened list with task, type, and order information
func ParseEntries(entries map[string]interface{}) ([]Entry, error) {
	var result []Entry
	index := 0

	for task, types := range entries {
		typesMap, ok := types.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid types structure for task: %s", task)
		}

		for typeKey, typeValue := range typesMap {
			result = append(result, Entry{
				Task:      task,
				Type:      typeKey,
				EntryData: typeValue,
				Index:     index,
			})
			index++
		}
	}

	return result, nil
}

// GroupEntriesByType groups entries by their type
func GroupEntriesByType(entries []Entry) map[string][]Entry {
	grouped := make(map[string][]Entry)
	for _, entry := range entries {
		grouped[entry.Type] = append(grouped[entry.Type], entry)
	}
	return grouped
}

// BuildEntriesForType builds the entries JSON structure for a specific type
func BuildEntriesForType(entries []Entry) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	for _, entry := range entries {
		if result[entry.Task] == nil {
			result[entry.Task] = make(map[string]interface{})
		}
		taskTypes := result[entry.Task].(map[string]interface{})
		taskTypes[entry.Type] = entry.EntryData
	}

	return result, nil
}

// GetBackendURLForType determines the backend URL for a specific type
// It checks if any entry of this type has a task with specific routing
func GetBackendURLForType(entries []Entry, getBackendURL func(task string) string) string {
	// Check if any entry's task has specific routing
	for _, entry := range entries {
		if url := getBackendURL(entry.Task); url != "" {
			return url
		}
	}
	// Fall back to default backend
	return getBackendURL("")
}

// ForwardPredictRequest forwards the predict request to the appropriate backend based on task type
func ForwardPredictRequest(backendURL string, r *http.Request) (*http.Response, error) {
	client := &http.Client{
		Timeout: 60 * time.Second,
	}

	targetURL := backendURL + "/predict"

	// Parse multipart form to access form data
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, err
	}

	// Reconstruct multipart form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Copy form fields
	for key, values := range r.MultipartForm.Value {
		for _, value := range values {
			if err := writer.WriteField(key, value); err != nil {
				return nil, err
			}
		}
	}

	// Copy form files
	for key, files := range r.MultipartForm.File {
		for _, fileHeader := range files {
			file, err := fileHeader.Open()
			if err != nil {
				return nil, err
			}
			defer file.Close()

			part, err := writer.CreateFormFile(key, fileHeader.Filename)
			if err != nil {
				return nil, err
			}
			if _, err := io.Copy(part, file); err != nil {
				return nil, err
			}
		}
	}

	if err := writer.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", targetURL, body)
	if err != nil {
		return nil, err
	}

	// Copy headers
	req.Header = r.Header.Clone()
	req.Header.Set("Content-Type", writer.FormDataContentType())

	return client.Do(req)
}

// ForwardPredictRequestWithType forwards the predict request with custom entries JSON
func ForwardPredictRequestWithType(backendURL string, r *http.Request, entriesJSON string) (*http.Response, error) {
	client := &http.Client{
		Timeout: 60 * time.Second,
	}

	targetURL := backendURL + "/predict"

	// Reconstruct multipart form with custom entries
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Write custom entries
	if err := writer.WriteField("entries", entriesJSON); err != nil {
		return nil, err
	}

	// Parse original multipart form to get files
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, err
	}

	// Copy form files (image, text, etc.)
	for key, files := range r.MultipartForm.File {
		for _, fileHeader := range files {
			file, err := fileHeader.Open()
			if err != nil {
				return nil, err
			}
			defer file.Close()

			part, err := writer.CreateFormFile(key, fileHeader.Filename)
			if err != nil {
				return nil, err
			}
			if _, err := io.Copy(part, file); err != nil {
				return nil, err
			}
		}
	}

	if err := writer.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", targetURL, body)
	if err != nil {
		return nil, err
	}

	// Copy headers
	req.Header = r.Header.Clone()
	req.Header.Set("Content-Type", writer.FormDataContentType())

	return client.Do(req)
}