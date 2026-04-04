package debug

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestDebugManager() *DebugManager {
	return &DebugManager{
		enabled:    true,
		maxRecords: 10,
		records:    make(map[string]HTTPRecord),
	}
}

func TestDebugManagerRecordsIncomingRequestsAndResponses(t *testing.T) {
	dm := newTestDebugManager()
	body := []byte(`{"hello":"world"}`)
	req := httptest.NewRequest(http.MethodPost, "/predict?trace=1", strings.NewReader(string(body)))
	req.Header["Content-Type"] = []string{"application/json"}
	req.Header["X-Test"] = []string{"first", "second"}

	dm.RecordIncomingRequest("trace-1", req, body)
	dm.RecordIncomingResponse("trace-1", http.StatusAccepted, http.Header{
		"Content-Type": []string{"text/plain"},
	}, []byte("ok"))

	record, ok := dm.GetRecord("trace-1")
	if !ok {
		t.Fatal("expected incoming record to exist")
	}
	if record.TraceID != "trace-1" {
		t.Fatalf("expected trace id trace-1, got %q", record.TraceID)
	}
	if record.Request.Body.Kind != "json" {
		t.Fatalf("expected request body kind json, got %q", record.Request.Body.Kind)
	}
	if got := record.Request.Headers["X-Test"]; got != "first, second" {
		t.Fatalf("expected flattened header, got %q", got)
	}
	if record.Response.StatusCode != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, record.Response.StatusCode)
	}
	if record.Response.Body.Kind != "text" {
		t.Fatalf("expected response body kind text, got %q", record.Response.Body.Kind)
	}
}

func TestDebugManagerRecordsOutgoingRequestsAndErrors(t *testing.T) {
	dm := newTestDebugManager()

	dm.RecordOutgoingRequest("out-1", "", http.MethodPost, "http://backend/predict", http.Header{
		"Content-Type": []string{"application/json"},
		"X-Test":       []string{"alpha", "beta"},
	}, []byte(`{"outgoing":true}`))
	dm.RecordError("out-1", errors.New("backend failed"))
	dm.RecordOutgoingResponse("out-1", http.StatusBadGateway, http.Header{
		"Content-Type": []string{"application/json"},
	}, []byte(`{"error":"bad gateway"}`))

	record, ok := dm.GetRecord("out-1")
	if !ok {
		t.Fatal("expected outgoing record to exist")
	}
	if record.TraceID != "out-1" {
		t.Fatalf("expected empty trace id to fall back to record id, got %q", record.TraceID)
	}
	if record.Error != "backend failed" {
		t.Fatalf("expected error to be recorded, got %q", record.Error)
	}
	if got := record.Request.Headers["X-Test"]; got != "alpha, beta" {
		t.Fatalf("expected flattened outgoing header, got %q", got)
	}
	if record.Response.Body.Kind != "json" {
		t.Fatalf("expected json response body, got %q", record.Response.Body.Kind)
	}
}

func TestDebugManagerTrimsSortsAndClearsRecords(t *testing.T) {
	dm := newTestDebugManager()

	dm.SetMaxRecords(2)
	if got := dm.GetMaxRecords(); got != 2 {
		t.Fatalf("expected max records 2, got %d", got)
	}

	dm.addRecord(HTTPRecord{ID: "old", Timestamp: time.Unix(10, 0)})
	dm.addRecord(HTTPRecord{ID: "mid", Timestamp: time.Unix(20, 0)})
	dm.addRecord(HTTPRecord{ID: "new", Timestamp: time.Unix(30, 0)})

	records := dm.GetRecords()
	if len(records) != 2 {
		t.Fatalf("expected 2 records after trimming, got %d", len(records))
	}
	if records[0].ID != "new" || records[1].ID != "mid" {
		t.Fatalf("expected newest-first ordering after trim, got %+v", records)
	}

	status := dm.GetStatus()
	if got := status["recordCount"].(int); got != 2 {
		t.Fatalf("expected recordCount 2, got %d", got)
	}
	if got := status["enabled"].(bool); !got {
		t.Fatal("expected debug manager to remain enabled")
	}

	dm.ClearRecords()
	if len(dm.GetRecords()) != 0 {
		t.Fatal("expected records to be cleared")
	}

	dm.SetEnabled(false)
	if dm.IsEnabled() {
		t.Fatal("expected debug manager to be disabled")
	}
}

func TestGenerateIDAndRandomString(t *testing.T) {
	id := GenerateID()
	if !strings.Contains(id, "-") {
		t.Fatalf("expected generated id to contain separator, got %q", id)
	}

	value := randomString(16)
	if len(value) != 16 {
		t.Fatalf("expected random string length 16, got %d", len(value))
	}
	for _, r := range value {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789", r) {
			t.Fatalf("unexpected rune %q in random string", r)
		}
	}
}
