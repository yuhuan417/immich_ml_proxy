package debug

import (
	"net/http"
	"sync"
	"time"
)

// HTTPRecord represents a single HTTP request/response record
type HTTPRecord struct {
	ID        string              `json:"id"`
	Timestamp time.Time           `json:"timestamp"`
	Type      string              `json:"type"` // "incoming" or "outgoing"
	Request   RequestInfo         `json:"request"`
	Response  ResponseInfo        `json:"response"`
	Error     string              `json:"error,omitempty"`
}

// RequestInfo stores request details
type RequestInfo struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// ResponseInfo stores response details
type ResponseInfo struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
}

// DebugManager manages debug records and settings
type DebugManager struct {
	enabled      bool
	maxRecords   int
	records      []HTTPRecord
	recordMap    map[string]int // id -> index in records slice
	mu           sync.RWMutex
}

var (
	instance *DebugManager
	once     sync.Once
)

// GetInstance returns the singleton DebugManager
func GetInstance() *DebugManager {
	once.Do(func() {
		instance = &DebugManager{
			enabled:    false,
			maxRecords: 100,
			records:    make([]HTTPRecord, 0),
			recordMap:  make(map[string]int),
		}
	})
	return instance
}

// IsEnabled returns whether debug is enabled
func (dm *DebugManager) IsEnabled() bool {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.enabled
}

// SetEnabled enables or disables debug
func (dm *DebugManager) SetEnabled(enabled bool) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.enabled = enabled
}

// GetMaxRecords returns the maximum number of records to keep
func (dm *DebugManager) GetMaxRecords() int {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.maxRecords
}

// SetMaxRecords sets the maximum number of records to keep
func (dm *DebugManager) SetMaxRecords(max int) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.maxRecords = max
	dm.trimRecords()
}

// RecordIncomingRequest records an incoming request
func (dm *DebugManager) RecordIncomingRequest(id string, r *http.Request, body []byte) {
	if !dm.IsEnabled() {
		return
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()

	headers := make(map[string]string)
	for key, values := range r.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	record := HTTPRecord{
		ID:        id,
		Timestamp: time.Now(),
		Type:      "incoming",
		Request: RequestInfo{
			Method:  r.Method,
			URL:     r.URL.String(),
			Headers: headers,
			Body:    string(body),
		},
	}

	dm.addRecord(record)
}

// RecordIncomingResponse records an incoming response
func (dm *DebugManager) RecordIncomingResponse(id string, statusCode int, headers http.Header, body []byte) {
	if !dm.IsEnabled() {
		return
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()

	idx, exists := dm.recordMap[id]
	if !exists {
		return
	}

	headerMap := make(map[string]string)
	for key, values := range headers {
		if len(values) > 0 {
			headerMap[key] = values[0]
		}
	}

	dm.records[idx].Response = ResponseInfo{
		StatusCode: statusCode,
		Headers:    headerMap,
		Body:       string(body),
	}
}

// RecordOutgoingRequest records an outgoing request to backend
func (dm *DebugManager) RecordOutgoingRequest(id string, method string, url string, headers http.Header, body []byte) {
	if !dm.IsEnabled() {
		return
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()

	headerMap := make(map[string]string)
	for key, values := range headers {
		if len(values) > 0 {
			headerMap[key] = values[0]
		}
	}

	record := HTTPRecord{
		ID:        id,
		Timestamp: time.Now(),
		Type:      "outgoing",
		Request: RequestInfo{
			Method:  method,
			URL:     url,
			Headers: headerMap,
			Body:    string(body),
		},
	}

	dm.addRecord(record)
}

// RecordOutgoingResponse records an outgoing response from backend
func (dm *DebugManager) RecordOutgoingResponse(id string, statusCode int, headers http.Header, body []byte) {
	if !dm.IsEnabled() {
		return
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()

	idx, exists := dm.recordMap[id]
	if !exists {
		return
	}

	headerMap := make(map[string]string)
	for key, values := range headers {
		if len(values) > 0 {
			headerMap[key] = values[0]
		}
	}

	dm.records[idx].Response = ResponseInfo{
		StatusCode: statusCode,
		Headers:    headerMap,
		Body:       string(body),
	}
}

// RecordError records an error
func (dm *DebugManager) RecordError(id string, err error) {
	if !dm.IsEnabled() {
		return
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()

	idx, exists := dm.recordMap[id]
	if !exists {
		return
	}

	dm.records[idx].Error = err.Error()
}

// GetRecords returns all records
func (dm *DebugManager) GetRecords() []HTTPRecord {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	result := make([]HTTPRecord, len(dm.records))
	copy(result, dm.records)
	return result
}

// GetRecord returns a specific record by ID
func (dm *DebugManager) GetRecord(id string) (HTTPRecord, bool) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	idx, exists := dm.recordMap[id]
	if !exists {
		return HTTPRecord{}, false
	}

	return dm.records[idx], true
}

// ClearRecords clears all records
func (dm *DebugManager) ClearRecords() {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	dm.records = make([]HTTPRecord, 0)
	dm.recordMap = make(map[string]int)
}

// GetStatus returns the current debug status
func (dm *DebugManager) GetStatus() map[string]interface{} {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	return map[string]interface{}{
		"enabled":    dm.enabled,
		"maxRecords": dm.maxRecords,
		"recordCount": len(dm.records),
	}
}

// addRecord adds a record and trims if necessary
func (dm *DebugManager) addRecord(record HTTPRecord) {
	dm.recordMap[record.ID] = len(dm.records)
	dm.records = append(dm.records, record)
	dm.trimRecords()
}

// trimRecords removes oldest records if exceeding max
func (dm *DebugManager) trimRecords() {
	for len(dm.records) > dm.maxRecords {
		oldestID := dm.records[0].ID
		delete(dm.recordMap, oldestID)
		dm.records = dm.records[1:]
	}
}

// GenerateID generates a unique record ID
func GenerateID() string {
	return time.Now().Format("20060102-150405.000") + "-" + randomString(6)
}

// randomString generates a random string of given length
func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
	}
	return string(b)
}