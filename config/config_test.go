package config

import (
	"encoding/json"
	"testing"

	"immich_ml_proxy/tasks"
)

func newTestConfig() *Config {
	return &Config{
		TaskRouting:            make(map[string]string),
		ModelTypeRouting:       make(map[string]string),
		TaskRoutingPolicy:      make(map[string]RoutingPolicy),
		ModelTypeRoutingPolicy: make(map[string]RoutingPolicy),
		Health:                 make(map[string]BackendHealth),
	}
}

func TestConfigBackendRoutingLifecycle(t *testing.T) {
	cfg := newTestConfig()

	cfg.AddBackend("default", "http://default")
	cfg.AddBackend("ocr", "http://ocr")
	cfg.AddBackend("default", "http://default-v2")
	cfg.SetDefaultBackend("default")
	cfg.SetTaskRouting(tasks.LegacyFacialRecognitionTask, "ocr")

	if got := cfg.GetBackendURL(tasks.LegacyFacialRecognitionTask); got != "http://ocr" {
		t.Fatalf("expected routed backend url, got %q", got)
	}
	if got := cfg.GetBackendURL(tasks.ClipTask); got != "http://default-v2" {
		t.Fatalf("expected default backend url, got %q", got)
	}

	urls := cfg.GetAllBackendURLs()
	if len(urls) != 2 {
		t.Fatalf("expected 2 backend urls, got %d", len(urls))
	}

	backends := cfg.GetBackends()
	backends[0].Name = "changed"
	if cfg.Backends[0].Name == "changed" {
		t.Fatal("expected GetBackends to return a copy")
	}

	cfg.RemoveBackend("ocr")
	if cfg.GetBackendByName("ocr") != nil {
		t.Fatal("expected removed backend to disappear")
	}
	if _, ok := cfg.TaskRouting[tasks.FacialRecognitionTask]; ok {
		t.Fatalf("expected task routing to be removed with backend, got %#v", cfg.TaskRouting)
	}
}

func TestConfigHealthAndQueryAccessors(t *testing.T) {
	cfg := &Config{
		DefaultBackend: "default",
		Backends: []Backend{
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
		TaskRoutingPolicy: map[string]RoutingPolicy{
			tasks.OCRTask: RoutingPolicyFallback,
		},
		ModelTypeRoutingPolicy: map[string]RoutingPolicy{
			"visual": RoutingPolicyFallback,
		},
		Health: make(map[string]BackendHealth),
	}

	cfg.SetHealthStatus("ocr", HealthStatusHealthy, "")
	cfg.SetHealthStatus("default", HealthStatusUnhealthy, "down")

	if got := cfg.GetHealthStatus("ocr"); got.Status != HealthStatusHealthy || got.LastCheck == 0 {
		t.Fatalf("expected healthy backend health status, got %+v", got)
	}
	if got := cfg.GetHealthStatus("missing"); got.Status != HealthStatusUnknown {
		t.Fatalf("expected unknown status for missing backend, got %+v", got)
	}
	if got := cfg.GetAllHealthStatus(); len(got) != 2 {
		t.Fatalf("expected 2 health entries, got %d", len(got))
	}

	if got := cfg.GetBackendsByTask(tasks.OCRTask); len(got) != 1 || got[0].Name != "ocr" {
		t.Fatalf("expected routed task backend, got %#v", got)
	}
	if got := cfg.GetBackendsByType(tasks.OCRTask); len(got) != 1 || got[0].Name != "ocr" {
		t.Fatalf("expected compatibility type backend lookup, got %#v", got)
	}
	if got := cfg.GetHealthyBackendsByTask(tasks.OCRTask); len(got) != 1 || got[0].Name != "ocr" {
		t.Fatalf("expected healthy routed backend, got %#v", got)
	}
	if got := cfg.GetHealthyBackendsByType(tasks.OCRTask); len(got) != 1 || got[0].Name != "ocr" {
		t.Fatalf("expected healthy compatibility type lookup, got %#v", got)
	}
	if got := cfg.GetDefaultBackend(); got == nil || got.Name != "default" {
		t.Fatalf("expected default backend, got %+v", got)
	}
	if got := cfg.GetBackendByModelType("visual"); got == nil || got.Name != "visual" {
		t.Fatalf("expected modelType backend, got %+v", got)
	}
	if got := cfg.GetTaskRoutingPolicy(tasks.OCRTask); got != RoutingPolicyFallback {
		t.Fatalf("expected task routing policy fallback, got %q", got)
	}
	if got := cfg.GetTaskRoutingPolicy(tasks.ClipTask); got != RoutingPolicyStrict {
		t.Fatalf("expected unknown task policy to default strict, got %q", got)
	}
	if got := cfg.GetModelTypeRoutingPolicy("visual"); got != RoutingPolicyFallback {
		t.Fatalf("expected modelType routing policy fallback, got %q", got)
	}
	if got := cfg.GetModelTypeRoutingPolicy("textual"); got != RoutingPolicyStrict {
		t.Fatalf("expected unknown modelType policy to default strict, got %q", got)
	}
	if got := cfg.GetAllTasks(); len(got) != 1 || got[0] != tasks.OCRTask {
		t.Fatalf("expected one routed task, got %#v", got)
	}
	if got := cfg.GetAllTypes(); len(got) != 1 || got[0] != tasks.OCRTask {
		t.Fatalf("expected compatibility types list, got %#v", got)
	}
	if got := cfg.GetAllModelTypes(); len(got) != 1 || got[0] != "visual" {
		t.Fatalf("expected one modelType, got %#v", got)
	}
}

func TestConfigReplaceAndToJSON(t *testing.T) {
	cfg := newTestConfig()
	backends := []Backend{
		{Name: "default", URL: "http://default"},
		{Name: "ocr", URL: "http://ocr"},
	}
	taskRouting := map[string]string{
		tasks.LegacyFacialRecognitionTask: "ocr",
	}
	modelTypeRouting := map[string]string{
		"visual": "ocr",
	}
	taskRoutingPolicy := map[string]RoutingPolicy{
		tasks.LegacyFacialRecognitionTask: RoutingPolicyFallback,
	}
	modelTypeRoutingPolicy := map[string]RoutingPolicy{
		"visual": RoutingPolicyFallback,
	}

	cfg.Replace("default", backends, taskRouting, modelTypeRouting, taskRoutingPolicy, modelTypeRoutingPolicy)

	backends[0].Name = "changed"
	taskRouting[tasks.ClipTask] = "default"
	modelTypeRouting["textual"] = "default"
	taskRoutingPolicy[tasks.ClipTask] = RoutingPolicyFallback
	modelTypeRoutingPolicy["textual"] = RoutingPolicyFallback

	if cfg.Backends[0].Name != "default" {
		t.Fatalf("expected Replace to copy backends, got %#v", cfg.Backends)
	}
	if _, exists := cfg.TaskRouting[tasks.ClipTask]; exists {
		t.Fatalf("did not expect later task routing mutations to leak into config, got %#v", cfg.TaskRouting)
	}
	if _, exists := cfg.ModelTypeRouting["textual"]; exists {
		t.Fatalf("did not expect later modelType mutations to leak into config, got %#v", cfg.ModelTypeRouting)
	}
	if _, exists := cfg.TaskRoutingPolicy[tasks.ClipTask]; exists {
		t.Fatalf("did not expect later task policy mutations to leak into config, got %#v", cfg.TaskRoutingPolicy)
	}
	if _, exists := cfg.ModelTypeRoutingPolicy["textual"]; exists {
		t.Fatalf("did not expect later modelType policy mutations to leak into config, got %#v", cfg.ModelTypeRoutingPolicy)
	}

	data, err := cfg.ToJSON()
	if err != nil {
		t.Fatalf("config to json: %v", err)
	}

	var decoded struct {
		DefaultBackend         string                   `json:"defaultBackend"`
		TaskRouting            map[string]string        `json:"taskRouting"`
		ModelTypeRouting       map[string]string        `json:"modelTypeRouting"`
		TaskRoutingPolicy      map[string]RoutingPolicy `json:"taskRoutingPolicy"`
		ModelTypeRoutingPolicy map[string]RoutingPolicy `json:"modelTypeRoutingPolicy"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode config json: %v", err)
	}

	if decoded.DefaultBackend != "default" {
		t.Fatalf("expected default backend in json, got %q", decoded.DefaultBackend)
	}
	if got := decoded.TaskRouting[tasks.FacialRecognitionTask]; got != "ocr" {
		t.Fatalf("expected normalized task routing in json, got %#v", decoded.TaskRouting)
	}
	if got := decoded.ModelTypeRouting["visual"]; got != "ocr" {
		t.Fatalf("expected modelType routing in json, got %#v", decoded.ModelTypeRouting)
	}
	if got := decoded.TaskRoutingPolicy[tasks.FacialRecognitionTask]; got != RoutingPolicyFallback {
		t.Fatalf("expected normalized task policy in json, got %#v", decoded.TaskRoutingPolicy)
	}
	if got := decoded.ModelTypeRoutingPolicy["visual"]; got != RoutingPolicyFallback {
		t.Fatalf("expected modelType policy in json, got %#v", decoded.ModelTypeRoutingPolicy)
	}

	if got := normalizeTaskRouting(nil); len(got) != 0 {
		t.Fatalf("expected nil routing to normalize to empty map, got %#v", got)
	}
}
