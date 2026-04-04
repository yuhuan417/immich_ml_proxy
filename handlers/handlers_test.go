package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"immich_ml_proxy/config"
	"immich_ml_proxy/debug"
	"immich_ml_proxy/proxy"
	"immich_ml_proxy/tasks"

	"github.com/gin-gonic/gin"
)

func setTestConfig(t *testing.T, testCfg *config.Config) {
	t.Helper()

	previous := cfg
	cfg = testCfg
	t.Cleanup(func() {
		cfg = previous
	})
}

func TestCloneHeaderWithContentType(t *testing.T) {
	source := http.Header{
		"X-Test":       []string{"alpha", "beta"},
		"Content-Type": []string{"application/json"},
	}

	cloned := cloneHeaderWithContentType(source, "multipart/form-data; boundary=test")

	if got := cloned.Values("X-Test"); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("expected X-Test to be copied, got %#v", got)
	}
	if got := cloned.Get("Content-Type"); got != "multipart/form-data; boundary=test" {
		t.Fatalf("expected content type override, got %q", got)
	}

	source.Set("X-Test", "changed")
	if got := cloned.Values("X-Test"); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("expected cloned header to be independent, got %#v", got)
	}
}

func TestMergePredictResult(t *testing.T) {
	dst := map[string]interface{}{
		tasks.ClipTask: map[string]interface{}{
			"textual": "text-result",
		},
	}
	src := map[string]interface{}{
		tasks.ClipTask: map[string]interface{}{
			"visual": "image-result",
		},
		tasks.OCRTask: map[string]interface{}{
			"text": "ocr-result",
		},
	}

	if err := mergePredictResult(dst, src); err != nil {
		t.Fatalf("merge predict result: %v", err)
	}

	clipResult := dst[tasks.ClipTask].(map[string]interface{})
	if clipResult["textual"] != "text-result" || clipResult["visual"] != "image-result" {
		t.Fatalf("expected merged clip result, got %#v", clipResult)
	}
	if _, ok := dst[tasks.OCRTask]; !ok {
		t.Fatal("expected new task result to be added")
	}

	if err := mergePredictResult(map[string]interface{}{
		tasks.ClipTask: map[string]interface{}{"visual": "old"},
	}, map[string]interface{}{
		tasks.ClipTask: map[string]interface{}{"visual": "new"},
	}); err == nil {
		t.Fatal("expected duplicate task type merge to fail")
	}

	if err := mergePredictResult(map[string]interface{}{
		tasks.ClipTask: "non-object",
	}, map[string]interface{}{
		tasks.ClipTask: map[string]interface{}{"visual": "new"},
	}); err == nil {
		t.Fatal("expected object/non-object merge conflict to fail")
	}
}

func TestNormalizeTaskRoutingAndValidateRoutingBackends(t *testing.T) {
	routing := normalizeTaskRouting(map[string]string{
		tasks.LegacyFacialRecognitionTask: "face-backend",
	})
	if got := routing[tasks.FacialRecognitionTask]; got != "face-backend" {
		t.Fatalf("expected normalized task routing, got %#v", routing)
	}

	backends := []config.Backend{
		{Name: "default", URL: "http://default"},
		{Name: "face-backend", URL: "http://face"},
	}

	if err := validateRoutingBackends(backends, routing, map[string]string{"visual": "default"}); err != nil {
		t.Fatalf("expected valid routing, got %v", err)
	}
	if err := validateRoutingBackends(backends, routing, map[string]string{"visual": "missing"}); err == nil {
		t.Fatal("expected unknown modelType backend to fail validation")
	}
	if err := validateBackendDefinitions(backends); err != nil {
		t.Fatalf("expected unique backend definitions to be valid, got %v", err)
	}
	if err := validateBackendDefinitions([]config.Backend{
		{Name: "dup", URL: "http://one"},
		{Name: "dup", URL: "http://two"},
	}); err == nil {
		t.Fatal("expected duplicate backend definitions to fail validation")
	}
}

func TestSelectBackendForDispatchGroup(t *testing.T) {
	setTestConfig(t, &config.Config{
		DefaultBackend: "default",
		Backends: []config.Backend{
			{Name: "default", URL: "http://default"},
			{Name: "ocr", URL: "http://ocr"},
			{Name: "visual", URL: "http://visual"},
		},
		TaskRouting: map[string]string{
			tasks.OCRTask: "ocr",
		},
		ModelTypeRouting: map[string]string{
			"visual": "visual",
		},
		Health: map[string]config.BackendHealth{
			"ocr":     {Status: config.HealthStatusHealthy},
			"default": {Status: config.HealthStatusHealthy},
		},
	})

	backend, err := selectBackendForDispatchGroup(proxy.DispatchGroup{
		Key:   "clip:visual",
		Task:  tasks.ClipTask,
		Type:  "visual",
		Split: true,
	})
	if err != nil || backend.Name != "visual" {
		t.Fatalf("expected modelType-routed backend, got backend=%+v err=%v", backend, err)
	}

	backend, err = selectBackendForDispatchGroup(proxy.DispatchGroup{
		Key:  tasks.OCRTask,
		Task: tasks.OCRTask,
	})
	if err != nil || backend.Name != "ocr" {
		t.Fatalf("expected task-routed backend, got backend=%+v err=%v", backend, err)
	}

	backend, err = selectBackendForDispatchGroup(proxy.DispatchGroup{
		Key:  tasks.ClipTask,
		Task: tasks.ClipTask,
	})
	if err != nil || backend.Name != "default" {
		t.Fatalf("expected default backend, got backend=%+v err=%v", backend, err)
	}

	setTestConfig(t, &config.Config{
		TaskRouting:      make(map[string]string),
		ModelTypeRouting: make(map[string]string),
		Health:           make(map[string]config.BackendHealth),
	})
	if _, err := selectBackendForDispatchGroup(proxy.DispatchGroup{
		Key:  tasks.ClipTask,
		Task: tasks.ClipTask,
	}); err == nil {
		t.Fatal("expected missing backend configuration to fail")
	}
}

func TestDebugAPIHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dm := debug.GetInstance()
	previousEnabled := dm.IsEnabled()
	previousMaxRecords := dm.GetMaxRecords()
	dm.ClearRecords()
	dm.SetEnabled(false)
	dm.SetMaxRecords(100)
	t.Cleanup(func() {
		dm.ClearRecords()
		dm.SetEnabled(previousEnabled)
		dm.SetMaxRecords(previousMaxRecords)
	})

	dm.SetEnabled(true)
	dm.RecordOutgoingRequest("record-1", "trace-1", http.MethodPost, "http://backend/predict", http.Header{
		"Content-Type": []string{"application/json"},
	}, []byte(`{"ok":true}`))

	testCases := []struct {
		name       string
		method     string
		target     string
		body       string
		handler    gin.HandlerFunc
		statusCode int
		assertBody string
	}{
		{
			name:       "status",
			method:     http.MethodGet,
			target:     "/api/debug/status",
			handler:    DebugStatusHandler,
			statusCode: http.StatusOK,
			assertBody: `"enabled":true`,
		},
		{
			name:       "toggle",
			method:     http.MethodPost,
			target:     "/api/debug/toggle",
			body:       `{"enabled":false}`,
			handler:    DebugToggleHandler,
			statusCode: http.StatusOK,
			assertBody: `"enabled":false`,
		},
		{
			name:       "max-records",
			method:     http.MethodPost,
			target:     "/api/debug/max-records",
			body:       `{"maxRecords":5}`,
			handler:    DebugMaxRecordsHandler,
			statusCode: http.StatusOK,
			assertBody: `"maxRecords":5`,
		},
		{
			name:       "records",
			method:     http.MethodGet,
			target:     "/api/debug/records",
			handler:    DebugRecordsHandler,
			statusCode: http.StatusOK,
			assertBody: `"id":"record-1"`,
		},
		{
			name:       "clear",
			method:     http.MethodDelete,
			target:     "/api/debug/records",
			handler:    DebugClearRecordsHandler,
			statusCode: http.StatusOK,
			assertBody: `"message":"Records cleared"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			var bodyReader *strings.Reader
			if tc.body == "" {
				bodyReader = strings.NewReader("")
			} else {
				bodyReader = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(tc.method, tc.target, bodyReader)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			context.Request = req

			tc.handler(context)

			if recorder.Code != tc.statusCode {
				t.Fatalf("expected status %d, got %d", tc.statusCode, recorder.Code)
			}
			if tc.assertBody != "" && !strings.Contains(recorder.Body.String(), tc.assertBody) {
				t.Fatalf("expected response body to contain %q, got %q", tc.assertBody, recorder.Body.String())
			}
		})
	}

	if dm.IsEnabled() {
		t.Fatal("expected toggle handler to disable debug mode")
	}
	if got := dm.GetMaxRecords(); got != 5 {
		t.Fatalf("expected max records to be updated to 5, got %d", got)
	}
	if len(dm.GetRecords()) != 0 {
		t.Fatal("expected clear handler to remove records")
	}
}

func TestDebugMaxRecordsHandlerRejectsInvalidPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/debug/max-records", strings.NewReader(`{"maxRecords":0}`))
	context.Request.Header.Set("Content-Type", "application/json")

	DebugMaxRecordsHandler(context)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "maxRecords must be between 1 and 10000") {
		t.Fatalf("unexpected response body: %q", recorder.Body.String())
	}
}

func TestConfigPostHandlerRejectsDuplicateBackendNames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setTestConfig(t, &config.Config{
		TaskRouting:      make(map[string]string),
		ModelTypeRouting: make(map[string]string),
		Health:           make(map[string]config.BackendHealth),
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(`{
		"defaultBackend":"dup",
		"backends":[
			{"name":"dup","url":"http://one"},
			{"name":"dup","url":"http://two"}
		]
	}`))
	context.Request.Header.Set("Content-Type", "application/json")

	ConfigPostHandler(context)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "duplicate backend name: dup") {
		t.Fatalf("unexpected response body: %q", recorder.Body.String())
	}
}

func TestDebugMiddlewareAndResponseWriter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dm := debug.GetInstance()
	previousEnabled := dm.IsEnabled()
	previousMaxRecords := dm.GetMaxRecords()
	dm.ClearRecords()
	dm.SetEnabled(true)
	dm.SetMaxRecords(100)
	t.Cleanup(func() {
		dm.ClearRecords()
		dm.SetEnabled(previousEnabled)
		dm.SetMaxRecords(previousMaxRecords)
	})

	router := gin.New()
	router.Use(DebugMiddleware())
	router.POST("/predict", func(c *gin.Context) {
		c.JSON(http.StatusCreated, gin.H{
			"traceId": c.GetString("debugTraceID"),
		})
	})
	router.GET("/text", func(c *gin.Context) {
		c.String(http.StatusAccepted, "ok")
	})
	router.GET("/empty", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET("/api/debug/status", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/predict", strings.NewReader(`{"hello":"world"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", response.Code)
	}

	var payload map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode predict response: %v", err)
	}
	if payload["traceId"] == "" {
		t.Fatalf("expected middleware to inject traceId, got %q", response.Body.String())
	}

	records := dm.GetRecords()
	if len(records) != 1 {
		t.Fatalf("expected one recorded request, got %d", len(records))
	}
	record := records[0]
	if record.Type != "incoming" {
		t.Fatalf("expected incoming record, got %q", record.Type)
	}
	if record.Response.StatusCode != http.StatusCreated {
		t.Fatalf("expected recorded response status 201, got %d", record.Response.StatusCode)
	}
	if record.Request.Body.Kind != "json" {
		t.Fatalf("expected recorded request body kind json, got %q", record.Request.Body.Kind)
	}

	debugRequest := httptest.NewRequest(http.MethodGet, "/api/debug/status", nil)
	debugResponse := httptest.NewRecorder()
	router.ServeHTTP(debugResponse, debugRequest)
	if len(dm.GetRecords()) != 1 {
		t.Fatal("expected debug endpoints to be skipped by middleware")
	}

	textResponse := httptest.NewRecorder()
	router.ServeHTTP(textResponse, httptest.NewRequest(http.MethodGet, "/text", nil))
	if textResponse.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 for text route, got %d", textResponse.Code)
	}

	emptyResponse := httptest.NewRecorder()
	router.ServeHTTP(emptyResponse, httptest.NewRequest(http.MethodGet, "/empty", nil))
	if emptyResponse.Code != http.StatusNoContent {
		t.Fatalf("expected status 204 for empty route, got %d", emptyResponse.Code)
	}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	writer := &debugResponseWriter{
		ResponseWriter: context.Writer,
		status:         http.StatusOK,
	}

	writer.WriteHeader(http.StatusNoContent)
	if writer.status != http.StatusNoContent {
		t.Fatalf("expected writer status to track WriteHeader, got %d", writer.status)
	}

	recorder = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(recorder)
	writer = &debugResponseWriter{
		ResponseWriter: context.Writer,
		status:         http.StatusOK,
	}
	if _, err := writer.Write([]byte("body")); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if writer.body == nil || writer.body.String() != "body" {
		t.Fatalf("expected write body buffer to capture content, got %#v", writer.body)
	}

	recorder = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(recorder)
	writer = &debugResponseWriter{
		ResponseWriter: context.Writer,
		status:         http.StatusOK,
		body:           bytes.NewBuffer(nil),
	}
	if _, err := writer.WriteString("text"); err != nil {
		t.Fatalf("write string: %v", err)
	}
	if writer.body.String() != "text" {
		t.Fatalf("expected write string buffer to capture content, got %q", writer.body.String())
	}
}
