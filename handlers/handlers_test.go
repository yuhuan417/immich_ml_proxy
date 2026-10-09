package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"immich_ml_proxy/config"
	"immich_ml_proxy/debug"
	"immich_ml_proxy/proxy"
	"immich_ml_proxy/tasks"

	"github.com/gin-gonic/gin"
)

func buildPredictRequest(t *testing.T, entriesJSON string) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("entries", entriesJSON); err != nil {
		t.Fatalf("write entries field: %v", err)
	}
	if err := writer.WriteField("note", "keep-me"); err != nil {
		t.Fatalf("write note field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/predict", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

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
	routing := config.NormalizeTaskRouting(map[string]string{
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
	if err := validateRoutingPolicies(
		map[string]config.RoutingPolicy{tasks.OCRTask: config.RoutingPolicyFallback},
		map[string]config.RoutingPolicy{"visual": config.RoutingPolicyStrict},
	); err != nil {
		t.Fatalf("expected valid routing policies, got %v", err)
	}
	if err := validateRoutingPolicies(
		map[string]config.RoutingPolicy{tasks.OCRTask: config.RoutingPolicy("invalid")},
		map[string]config.RoutingPolicy{},
	); err == nil {
		t.Fatal("expected invalid task routing policy to fail validation")
	}
	if err := validateRoutingPolicies(
		map[string]config.RoutingPolicy{},
		map[string]config.RoutingPolicy{"visual": config.RoutingPolicy("invalid")},
	); err == nil {
		t.Fatal("expected invalid modelType routing policy to fail validation")
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

func TestSelectBackendForDispatchGroupPolicies(t *testing.T) {
	t.Run("modelType fallback policy uses default when routed backend is unhealthy", func(t *testing.T) {
		setTestConfig(t, &config.Config{
			DefaultBackend: "default",
			Backends: []config.Backend{
				{Name: "default", URL: "http://default"},
				{Name: "gpu", URL: "http://gpu"},
			},
			ModelTypeRouting: map[string]string{
				"textual": "gpu",
			},
			ModelTypeRoutingPolicy: map[string]config.RoutingPolicy{
				"textual": config.RoutingPolicyFallback,
			},
			Health: map[string]config.BackendHealth{
				"default": {Status: config.HealthStatusHealthy},
				"gpu":     {Status: config.HealthStatusUnhealthy},
			},
		})

		backend, err := selectBackendForDispatchGroup(proxy.DispatchGroup{
			Key:   "clip:textual",
			Task:  tasks.ClipTask,
			Type:  "textual",
			Split: true,
		})
		if err != nil || backend.Name != "default" {
			t.Fatalf("expected fallback to default backend, got backend=%+v err=%v", backend, err)
		}
	})

	t.Run("modelType strict policy keeps routed backend even when unhealthy", func(t *testing.T) {
		setTestConfig(t, &config.Config{
			DefaultBackend: "default",
			Backends: []config.Backend{
				{Name: "default", URL: "http://default"},
				{Name: "gpu", URL: "http://gpu"},
			},
			ModelTypeRouting: map[string]string{
				"textual": "gpu",
			},
			ModelTypeRoutingPolicy: map[string]config.RoutingPolicy{
				"textual": config.RoutingPolicyStrict,
			},
			Health: map[string]config.BackendHealth{
				"default": {Status: config.HealthStatusHealthy},
				"gpu":     {Status: config.HealthStatusUnhealthy},
			},
		})

		backend, err := selectBackendForDispatchGroup(proxy.DispatchGroup{
			Key:   "clip:textual",
			Task:  tasks.ClipTask,
			Type:  "textual",
			Split: true,
		})
		if err != nil || backend.Name != "gpu" {
			t.Fatalf("expected strict policy to keep routed backend, got backend=%+v err=%v", backend, err)
		}
	})

	t.Run("task fallback policy uses default when routed backend is unhealthy", func(t *testing.T) {
		setTestConfig(t, &config.Config{
			DefaultBackend: "default",
			Backends: []config.Backend{
				{Name: "default", URL: "http://default"},
				{Name: "ocr", URL: "http://ocr"},
			},
			TaskRouting: map[string]string{
				tasks.OCRTask: "ocr",
			},
			TaskRoutingPolicy: map[string]config.RoutingPolicy{
				tasks.OCRTask: config.RoutingPolicyFallback,
			},
			Health: map[string]config.BackendHealth{
				"default": {Status: config.HealthStatusHealthy},
				"ocr":     {Status: config.HealthStatusUnhealthy},
			},
		})

		backend, err := selectBackendForDispatchGroup(proxy.DispatchGroup{
			Key:  tasks.OCRTask,
			Task: tasks.OCRTask,
		})
		if err != nil || backend.Name != "default" {
			t.Fatalf("expected fallback to default backend, got backend=%+v err=%v", backend, err)
		}
	})
}

func TestPredictHandlerModelTypeFailureFallsBackToTaskRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gpuCalls atomic.Int32
	gpuServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gpuCalls.Add(1)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatalf("parse gpu request: %v", err)
		}
		if got := r.FormValue("entries"); got != `{"clip":{"textual":"hello"}}` {
			t.Fatalf("unexpected gpu entries payload: %q", got)
		}
		http.Error(w, "gpu down", http.StatusBadGateway)
	}))
	defer gpuServer.Close()

	var taskCalls atomic.Int32
	taskServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		taskCalls.Add(1)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatalf("parse task request: %v", err)
		}
		if got := r.FormValue("entries"); got != `{"clip":{"textual":"hello"}}` {
			t.Fatalf("unexpected task entries payload: %q", got)
		}
		_, _ = io.WriteString(w, `{"clip":{"textual":"task-result"}}`)
	}))
	defer taskServer.Close()

	var defaultCalls atomic.Int32
	defaultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultCalls.Add(1)
		_, _ = io.WriteString(w, `{"clip":{"textual":"default-result"}}`)
	}))
	defer defaultServer.Close()

	setTestConfig(t, &config.Config{
		DefaultBackend: "default",
		Backends: []config.Backend{
			{Name: "default", URL: defaultServer.URL},
			{Name: "clip-task", URL: taskServer.URL},
			{Name: "gpu", URL: gpuServer.URL},
		},
		TaskRouting: map[string]string{
			tasks.ClipTask: "clip-task",
		},
		ModelTypeRouting: map[string]string{
			"textual": "gpu",
		},
		TaskRoutingPolicy: map[string]config.RoutingPolicy{
			tasks.ClipTask: config.RoutingPolicyStrict,
		},
		ModelTypeRoutingPolicy: map[string]config.RoutingPolicy{
			"textual": config.RoutingPolicyFallback,
		},
		Health: make(map[string]config.BackendHealth),
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = buildPredictRequest(t, `{"clip":{"textual":"hello"}}`)

	PredictHandler(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var result map[string]map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := result[tasks.ClipTask]["textual"]; got != "task-result" {
		t.Fatalf("expected task fallback result, got %#v", result)
	}
	if got := gpuCalls.Load(); got != 1 {
		t.Fatalf("expected gpu backend to be tried once, got %d", got)
	}
	if got := taskCalls.Load(); got != 1 {
		t.Fatalf("expected task backend to be tried once, got %d", got)
	}
	if got := defaultCalls.Load(); got != 0 {
		t.Fatalf("did not expect default backend to be used, got %d calls", got)
	}
	// The 502 is one unreachable observation, which is below the demotion
	// threshold, so the backend keeps its verdict for now.
	if got := cfg.GetHealthStatus("gpu"); got.Status != config.HealthStatusUnknown || got.Failures != 1 {
		t.Fatalf("expected one gateway failure to stay below the threshold, got %+v", got)
	}
	if got := cfg.GetHealthStatus("clip-task").Status; got != config.HealthStatusHealthy {
		t.Fatalf("expected task backend to be marked healthy, got %q", got)
	}
}

func TestPredictHandlerTaskFailureFallsBackToDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var taskCalls atomic.Int32
	taskServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		taskCalls.Add(1)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatalf("parse task request: %v", err)
		}
		if got := r.FormValue("entries"); got != `{"ocr":{"text":"scan-me"}}` {
			t.Fatalf("unexpected task entries payload: %q", got)
		}
		http.Error(w, "ocr busy", http.StatusServiceUnavailable)
	}))
	defer taskServer.Close()

	var defaultCalls atomic.Int32
	defaultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultCalls.Add(1)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatalf("parse default request: %v", err)
		}
		if got := r.FormValue("entries"); got != `{"ocr":{"text":"scan-me"}}` {
			t.Fatalf("unexpected default entries payload: %q", got)
		}
		_, _ = io.WriteString(w, `{"ocr":{"text":"default-result"}}`)
	}))
	defer defaultServer.Close()

	setTestConfig(t, &config.Config{
		DefaultBackend: "default",
		Backends: []config.Backend{
			{Name: "default", URL: defaultServer.URL},
			{Name: "ocr", URL: taskServer.URL},
		},
		TaskRouting: map[string]string{
			tasks.OCRTask: "ocr",
		},
		TaskRoutingPolicy: map[string]config.RoutingPolicy{
			tasks.OCRTask: config.RoutingPolicyFallback,
		},
		Health: make(map[string]config.BackendHealth),
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = buildPredictRequest(t, `{"ocr":{"text":"scan-me"}}`)

	PredictHandler(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var result map[string]map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := result[tasks.OCRTask]["text"]; got != "default-result" {
		t.Fatalf("expected default fallback result, got %#v", result)
	}
	if got := taskCalls.Load(); got != 1 {
		t.Fatalf("expected task backend to be tried once, got %d", got)
	}
	if got := defaultCalls.Load(); got != 1 {
		t.Fatalf("expected default backend to be tried once, got %d", got)
	}
	// The 503 is one unreachable observation, which is below the demotion
	// threshold, so the backend keeps its verdict for now.
	if got := cfg.GetHealthStatus("ocr"); got.Status != config.HealthStatusUnknown || got.Failures != 1 {
		t.Fatalf("expected one gateway failure to stay below the threshold, got %+v", got)
	}
	if got := cfg.GetHealthStatus("default").Status; got != config.HealthStatusHealthy {
		t.Fatalf("expected default backend to be marked healthy, got %q", got)
	}
}

func TestPredictHandlerStrictRouteDoesNotFallbackAfterFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var strictCalls atomic.Int32
	strictServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		strictCalls.Add(1)
		http.Error(w, "strict backend failed", http.StatusBadGateway)
	}))
	defer strictServer.Close()

	var taskCalls atomic.Int32
	taskServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		taskCalls.Add(1)
		_, _ = io.WriteString(w, `{"clip":{"textual":"task-result"}}`)
	}))
	defer taskServer.Close()

	var defaultCalls atomic.Int32
	defaultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultCalls.Add(1)
		_, _ = io.WriteString(w, `{"clip":{"textual":"default-result"}}`)
	}))
	defer defaultServer.Close()

	setTestConfig(t, &config.Config{
		DefaultBackend: "default",
		Backends: []config.Backend{
			{Name: "default", URL: defaultServer.URL},
			{Name: "clip-task", URL: taskServer.URL},
			{Name: "gpu", URL: strictServer.URL},
		},
		TaskRouting: map[string]string{
			tasks.ClipTask: "clip-task",
		},
		ModelTypeRouting: map[string]string{
			"textual": "gpu",
		},
		TaskRoutingPolicy: map[string]config.RoutingPolicy{
			tasks.ClipTask: config.RoutingPolicyFallback,
		},
		ModelTypeRoutingPolicy: map[string]config.RoutingPolicy{
			"textual": config.RoutingPolicyStrict,
		},
		Health: make(map[string]config.BackendHealth),
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = buildPredictRequest(t, `{"clip":{"textual":"hello"}}`)

	PredictHandler(context)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "strict backend failed") {
		t.Fatalf("expected error response to mention strict backend failure, got %q", recorder.Body.String())
	}
	if got := strictCalls.Load(); got != 1 {
		t.Fatalf("expected strict backend to be tried once, got %d", got)
	}
	if got := taskCalls.Load(); got != 0 {
		t.Fatalf("did not expect task fallback after strict failure, got %d calls", got)
	}
	if got := defaultCalls.Load(); got != 0 {
		t.Fatalf("did not expect default fallback after strict failure, got %d calls", got)
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

// runPredict drives one /predict through the handler with the given entries.
func runPredict(t *testing.T, entriesJSON string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = buildPredictRequest(t, entriesJSON)
	PredictHandler(context)
	return recorder
}

// TestPredictHandlerHTTPFailureDoesNotDemoteBackend is the regression guard for
// the routing bug: a backend that answers with an error status is still up, so
// client-caused failures must never move it out of the way of its routes.
func TestPredictHandlerHTTPFailureDoesNotDemoteBackend(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var taskCalls atomic.Int32
			taskServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				taskCalls.Add(1)
				http.Error(w, "no thanks", status)
			}))
			defer taskServer.Close()

			setTestConfig(t, &config.Config{
				DefaultBackend: "default",
				Backends: []config.Backend{
					{Name: "default", URL: taskServer.URL},
					{Name: "ocr", URL: taskServer.URL},
				},
				TaskRouting: map[string]string{tasks.OCRTask: "ocr"},
				TaskRoutingPolicy: map[string]config.RoutingPolicy{
					tasks.OCRTask: config.RoutingPolicyFallback,
				},
				Health: make(map[string]config.BackendHealth),
			})

			// Far more failures than the demotion threshold.
			for i := 0; i < config.UnreachableThreshold+3; i++ {
				runPredict(t, `{"ocr":{"text":"scan-me"}}`)
			}

			// Answering at all proves reachability, whatever the status says.
			if got := cfg.GetHealthStatus("ocr"); got.Status != config.HealthStatusHealthy || got.Failures != 0 {
				t.Fatalf("status %d is the backend talking, it must count as reachable (got %+v)", status, got)
			}
		})
	}
}

// TestPredictHandlerGatewayFailuresEventuallyDemoteBackend covers the other half:
// an endpoint that reports it cannot serve does get moved aside, but only after
// enough consecutive observations.
func TestPredictHandlerGatewayFailuresEventuallyDemoteBackend(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var taskCalls atomic.Int32
	taskServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		taskCalls.Add(1)
		http.Error(w, "restarting", http.StatusServiceUnavailable)
	}))
	defer taskServer.Close()

	var defaultCalls atomic.Int32
	defaultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultCalls.Add(1)
		_, _ = io.WriteString(w, `{"ocr":{"text":"default-result"}}`)
	}))
	defer defaultServer.Close()

	setTestConfig(t, &config.Config{
		DefaultBackend: "default",
		Backends: []config.Backend{
			{Name: "default", URL: defaultServer.URL},
			{Name: "ocr", URL: taskServer.URL},
		},
		TaskRouting: map[string]string{tasks.OCRTask: "ocr"},
		TaskRoutingPolicy: map[string]config.RoutingPolicy{
			tasks.OCRTask: config.RoutingPolicyFallback,
		},
		Health: make(map[string]config.BackendHealth),
	})

	// Below the threshold the routed backend is still tried on every request,
	// with the default backend catching each one.
	belowThreshold := config.UnreachableThreshold - 1
	for i := 0; i < belowThreshold; i++ {
		if recorder := runPredict(t, `{"ocr":{"text":"scan-me"}}`); recorder.Code != http.StatusOK {
			t.Fatalf("request %d: expected the default backend to serve, got %d", i, recorder.Code)
		}
	}

	if got := cfg.GetHealthStatus("ocr"); got.Status == config.HealthStatusUnhealthy {
		t.Fatalf("expected %d failure(s) to stay below the demotion threshold, got %+v", belowThreshold, got)
	}
	if got := taskCalls.Load(); got != int32(belowThreshold) {
		t.Fatalf("expected the backend to still be tried, got %d call(s)", got)
	}

	// One more observation crosses the threshold and moves it aside.
	if recorder := runPredict(t, `{"ocr":{"text":"scan-me"}}`); recorder.Code != http.StatusOK {
		t.Fatalf("expected the default backend to serve, got %d", recorder.Code)
	}
	if got := cfg.GetHealthStatus("ocr"); got.Status != config.HealthStatusUnhealthy {
		t.Fatalf("expected %d gateway failures to demote the backend, got %+v", config.UnreachableThreshold, got)
	}

	// Once demoted, the fallback route stops spending an attempt on it.
	callsBefore := taskCalls.Load()
	if recorder := runPredict(t, `{"ocr":{"text":"scan-me"}}`); recorder.Code != http.StatusOK {
		t.Fatalf("expected the default backend to serve, got %d", recorder.Code)
	}
	if got := taskCalls.Load(); got != callsBefore {
		t.Fatalf("expected the demoted backend to be skipped, but it was called %d more time(s)", got-callsBefore)
	}
}

// TestPredictHandlerClientFaultStopsFallbackChain checks that a rejected payload
// is not replayed against the remaining backends.
func TestPredictHandlerClientFaultStopsFallbackChain(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var taskCalls atomic.Int32
	taskServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		taskCalls.Add(1)
		http.Error(w, "bad image data", http.StatusBadRequest)
	}))
	defer taskServer.Close()

	var defaultCalls atomic.Int32
	defaultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultCalls.Add(1)
		_, _ = io.WriteString(w, `{"ocr":{"text":"default-result"}}`)
	}))
	defer defaultServer.Close()

	setTestConfig(t, &config.Config{
		DefaultBackend: "default",
		Backends: []config.Backend{
			{Name: "default", URL: defaultServer.URL},
			{Name: "ocr", URL: taskServer.URL},
		},
		TaskRouting: map[string]string{tasks.OCRTask: "ocr"},
		TaskRoutingPolicy: map[string]config.RoutingPolicy{
			tasks.OCRTask: config.RoutingPolicyFallback,
		},
		Health: make(map[string]config.BackendHealth),
	})

	recorder := runPredict(t, `{"ocr":{"text":"scan-me"}}`)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected a failed prediction, got %d", recorder.Code)
	}
	if got := taskCalls.Load(); got != 1 {
		t.Fatalf("expected the routed backend to be tried once, got %d", got)
	}
	if got := defaultCalls.Load(); got != 0 {
		t.Fatalf("expected the rejected payload not to be replayed, but the default backend was called %d time(s)", got)
	}
}

// TestPingHandlerVerdict pins the /ping contract: the answer reflects this
// call's own probes, and a route only counts as servable if its backend answers
// "pong" right now.
func TestPingHandlerVerdict(t *testing.T) {
	gin.SetMode(gin.TestMode)

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			_, _ = io.WriteString(w, "pong")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer live.Close()

	// Answers, but not with the health contract's body.
	notPong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hi")
	}))
	defer notPong.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	backends := func(pairs ...config.Backend) []config.Backend { return pairs }

	cases := []struct {
		name     string
		config   *config.Config
		expected int
	}{
		{
			name: "everything reachable",
			config: &config.Config{
				DefaultBackend: "default",
				Backends:       backends(config.Backend{Name: "default", URL: live.URL}),
			},
			expected: http.StatusOK,
		},
		{
			name: "no backends configured",
			config: &config.Config{
				Backends: backends(),
			},
			expected: http.StatusServiceUnavailable,
		},
		{
			name: "default backend unreachable",
			config: &config.Config{
				DefaultBackend: "default",
				Backends:       backends(config.Backend{Name: "default", URL: deadURL}),
			},
			expected: http.StatusServiceUnavailable,
		},
		{
			name: "strict route unreachable",
			config: &config.Config{
				DefaultBackend:    "default",
				Backends:          backends(config.Backend{Name: "default", URL: live.URL}, config.Backend{Name: "ocr", URL: deadURL}),
				TaskRouting:       map[string]string{tasks.OCRTask: "ocr"},
				TaskRoutingPolicy: map[string]config.RoutingPolicy{tasks.OCRTask: config.RoutingPolicyStrict},
			},
			expected: http.StatusServiceUnavailable,
		},
		{
			name: "fallback route unreachable degrades to default",
			config: &config.Config{
				DefaultBackend:    "default",
				Backends:          backends(config.Backend{Name: "default", URL: live.URL}, config.Backend{Name: "ocr", URL: deadURL}),
				TaskRouting:       map[string]string{tasks.OCRTask: "ocr"},
				TaskRoutingPolicy: map[string]config.RoutingPolicy{tasks.OCRTask: config.RoutingPolicyFallback},
			},
			expected: http.StatusOK,
		},
		{
			name: "route answers without the health body",
			config: &config.Config{
				DefaultBackend:    "default",
				Backends:          backends(config.Backend{Name: "default", URL: live.URL}, config.Backend{Name: "ocr", URL: notPong.URL}),
				TaskRouting:       map[string]string{tasks.OCRTask: "ocr"},
				TaskRoutingPolicy: map[string]config.RoutingPolicy{tasks.OCRTask: config.RoutingPolicyStrict},
			},
			expected: http.StatusServiceUnavailable,
		},
		{
			name: "modelType route unreachable with fallback policy",
			config: &config.Config{
				DefaultBackend:         "default",
				Backends:               backends(config.Backend{Name: "default", URL: live.URL}, config.Backend{Name: "gpu", URL: deadURL}),
				ModelTypeRouting:       map[string]string{"textual": "gpu"},
				ModelTypeRoutingPolicy: map[string]config.RoutingPolicy{"textual": config.RoutingPolicyFallback},
			},
			expected: http.StatusOK,
		},
		{
			name: "modelType route unreachable with strict policy",
			config: &config.Config{
				DefaultBackend:         "default",
				Backends:               backends(config.Backend{Name: "default", URL: live.URL}, config.Backend{Name: "gpu", URL: deadURL}),
				ModelTypeRouting:       map[string]string{"textual": "gpu"},
				ModelTypeRoutingPolicy: map[string]config.RoutingPolicy{"textual": config.RoutingPolicyStrict},
			},
			expected: http.StatusServiceUnavailable,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.config.TaskRouting == nil {
				testCase.config.TaskRouting = map[string]string{}
			}
			if testCase.config.ModelTypeRouting == nil {
				testCase.config.ModelTypeRouting = map[string]string{}
			}
			if testCase.config.TaskRoutingPolicy == nil {
				testCase.config.TaskRoutingPolicy = map[string]config.RoutingPolicy{}
			}
			if testCase.config.ModelTypeRoutingPolicy == nil {
				testCase.config.ModelTypeRoutingPolicy = map[string]config.RoutingPolicy{}
			}
			testCase.config.Health = make(map[string]config.BackendHealth)
			setTestConfig(t, testCase.config)

			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodGet, "/ping", nil)

			PingHandler(context)
			// c.Status only records the code internally; gin's engine flushes it
			// after the handler chain, which calling the handler directly skips.
			context.Writer.WriteHeaderNow()

			if recorder.Code != testCase.expected {
				t.Fatalf("expected %d, got %d", testCase.expected, recorder.Code)
			}
			if testCase.expected == http.StatusOK && recorder.Body.String() != "pong" {
				t.Fatalf("expected pong body, got %q", recorder.Body.String())
			}
		})
	}
}
