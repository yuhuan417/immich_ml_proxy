package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"immich_ml_proxy/config"
	"immich_ml_proxy/debug"
	"immich_ml_proxy/proxy"
	"immich_ml_proxy/tasks"
	"io"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

var cfg *config.Config

func Init(c *config.Config) {
	cfg = c
}

// RootHandler handles GET / - returns static service information
func RootHandler(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="utf-8">
	<title>Immich ML Proxy</title>
	<style>
		body { font-family: -apple-system, BlinkMacSystemFont, sans-serif; margin: 40px; }
		h1 { color: #333; }
		a { color: #f5576c; text-decoration: none; margin-right: 20px; }
		a:hover { text-decoration: underline; }
	</style>
</head>
<body>
	<h1>Immich ML Proxy</h1>
	<p><a href="/config">Config</a><a href="/debug">Debug</a></p>
</body>
</html>`))
}

// PingHandler handles GET /ping - checks health status of all backends and returns "pong" if each routed task/model type has a healthy backend
func PingHandler(c *gin.Context) {
	backendURLs := cfg.GetAllBackendURLs()
	if len(backendURLs) == 0 {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	backends := cfg.GetBackends()

	var wg sync.WaitGroup
	statuses := make([]proxy.BackendStatus, len(backendURLs))
	statusesMu := sync.Mutex{}

	// Check health of all backends in parallel
	for i, backend := range backends {
		wg.Add(1)
		go func(idx int, b config.Backend) {
			defer wg.Done()
			status := proxy.CheckBackendHealth(b.URL)
			statusesMu.Lock()
			statuses[idx] = status
			statusesMu.Unlock()

			// Update health status in config
			if status.Status == "healthy" {
				cfg.SetHealthStatus(b.Name, config.HealthStatusHealthy, "")
			} else {
				cfg.SetHealthStatus(b.Name, config.HealthStatusUnhealthy, status.Error)
			}
		}(i, backend)
	}

	wg.Wait()

	// Check if default backend is healthy (it handles all non-routed types)
	defaultBackend := cfg.GetDefaultBackend()
	if defaultBackend == nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}

	defaultBackendHealth := cfg.GetHealthStatus(defaultBackend.Name)
	if defaultBackendHealth.Status != config.HealthStatusHealthy {
		c.Status(http.StatusServiceUnavailable)
		return
	}

	// Check if each routed task has at least one healthy backend
	allTasksHealthy := true

	for _, taskName := range cfg.GetAllTasks() {
		healthyBackends := cfg.GetHealthyBackendsByTask(taskName)
		if len(healthyBackends) == 0 {
			allTasksHealthy = false
			break
		}
	}

	if !allTasksHealthy {
		c.Status(http.StatusServiceUnavailable)
		return
	}

	for _, modelType := range cfg.GetAllModelTypes() {
		backend := cfg.GetBackendByModelType(modelType)
		if backend == nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		backendHealth := cfg.GetHealthStatus(backend.Name)
		if backendHealth.Status != config.HealthStatusHealthy {
			c.Status(http.StatusServiceUnavailable)
			return
		}
	}

	c.Data(http.StatusOK, "text/plain", []byte("pong"))
}

// PredictHandler handles POST /predict - groups dependent tasks together and
// only splits tasks like CLIP whose model types can run independently.
func PredictHandler(c *gin.Context) {
	entriesMap, err := proxy.ParseEntriesFromRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid entries: " + err.Error(),
		})
		return
	}

	entries, err := proxy.ParseEntries(entriesMap)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Failed to parse entries: " + err.Error(),
		})
		return
	}

	if len(entries) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "No entries specified",
		})
		return
	}

	dispatchGroups := proxy.BuildDispatchGroups(entries)
	finalResult := make(map[string]interface{})
	groupErrors := make(map[string]error)
	var resultMutex sync.Mutex
	var wg sync.WaitGroup
	debugTraceID := c.GetString("debugTraceID")

	for _, group := range dispatchGroups {
		wg.Add(1)
		go func(g proxy.DispatchGroup) {
			defer wg.Done()

			entriesForGroup, err := proxy.BuildEntriesForDispatchGroup(g)
			if err != nil {
				resultMutex.Lock()
				groupErrors[g.Key] = err
				resultMutex.Unlock()
				return
			}

			selectedBackend, err := selectBackendForDispatchGroup(g)
			if err != nil {
				resultMutex.Lock()
				groupErrors[g.Key] = err
				resultMutex.Unlock()
				return
			}

			entriesJSON, err := json.Marshal(entriesForGroup)
			if err != nil {
				resultMutex.Lock()
				groupErrors[g.Key] = err
				resultMutex.Unlock()
				return
			}

			resp, bodyBytes, outgoingContentType, err := proxy.ForwardPredictRequestWithType(selectedBackend.URL, c.Request, string(entriesJSON))
			debugHeaders := cloneHeaderWithContentType(c.Request.Header, outgoingContentType)
			if err != nil {
				if debug.GetInstance().IsEnabled() {
					recordID := debug.GenerateID()
					debug.GetInstance().RecordOutgoingRequest(recordID, debugTraceID, "POST", selectedBackend.URL+"/predict", debugHeaders, bodyBytes)
					debug.GetInstance().RecordError(recordID, err)
				}

				cfg.SetHealthStatus(selectedBackend.Name, config.HealthStatusUnhealthy, err.Error())

				resultMutex.Lock()
				groupErrors[g.Key] = err
				resultMutex.Unlock()
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				cfg.SetHealthStatus(selectedBackend.Name, config.HealthStatusHealthy, "")
			} else {
				body, _ := io.ReadAll(resp.Body)
				cfg.SetHealthStatus(selectedBackend.Name, config.HealthStatusUnhealthy, fmt.Sprintf("status %d: %s", resp.StatusCode, string(body)))
				resp.Body = io.NopCloser(bytes.NewReader(body))
			}

			if debug.GetInstance().IsEnabled() {
				recordID := debug.GenerateID()
				debug.GetInstance().RecordOutgoingRequest(recordID, debugTraceID, "POST", selectedBackend.URL+"/predict", debugHeaders, bodyBytes)
				body, _ := io.ReadAll(resp.Body)
				resp.Body = io.NopCloser(bytes.NewReader(body))
				debug.GetInstance().RecordOutgoingResponse(recordID, resp.StatusCode, resp.Header, body)
				resp.Body = io.NopCloser(bytes.NewReader(body))
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				resultMutex.Lock()
				groupErrors[g.Key] = err
				resultMutex.Unlock()
				return
			}

			if resp.StatusCode != http.StatusOK {
				resultMutex.Lock()
				groupErrors[g.Key] = fmt.Errorf("backend returned status %d: %s", resp.StatusCode, string(body))
				resultMutex.Unlock()
				return
			}

			var result map[string]interface{}
			if err := json.Unmarshal(body, &result); err != nil {
				resultMutex.Lock()
				groupErrors[g.Key] = err
				resultMutex.Unlock()
				return
			}

			resultMutex.Lock()
			if err := mergePredictResult(finalResult, result); err != nil {
				groupErrors[g.Key] = err
				resultMutex.Unlock()
				return
			}
			resultMutex.Unlock()
		}(group)
	}

	wg.Wait()

	if len(groupErrors) > 0 {
		var errMsgs []string
		for groupKey, err := range groupErrors {
			errMsgs = append(errMsgs, fmt.Sprintf("%s: %v", groupKey, err))
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  "Failed to process some dispatch groups",
			"errors": errMsgs,
		})
		return
	}

	c.JSON(http.StatusOK, finalResult)
}

func cloneHeaderWithContentType(src http.Header, contentType string) http.Header {
	cloned := make(http.Header, len(src))
	for key, values := range src {
		copiedValues := make([]string, len(values))
		copy(copiedValues, values)
		cloned[key] = copiedValues
	}

	if contentType != "" {
		cloned.Set("Content-Type", contentType)
	}

	return cloned
}

func selectBackendForDispatchGroup(group proxy.DispatchGroup) (*config.Backend, error) {
	if group.Split {
		if backend := cfg.GetBackendByModelType(group.Type); backend != nil {
			return backend, nil
		}
	}

	healthyBackends := cfg.GetHealthyBackendsByTask(group.Task)
	allBackends := cfg.GetBackendsByTask(group.Task)

	if len(allBackends) > 0 {
		candidates := allBackends
		if len(healthyBackends) > 0 {
			candidates = healthyBackends
		}

		backendList := make([]string, 0, len(candidates))
		for _, backend := range candidates {
			backendList = append(backendList, backend.URL)
		}

		selectedURL := proxy.GetNextBackend(group.Key, backendList)
		if selectedURL != "" {
			for _, backend := range candidates {
				if backend.URL == selectedURL {
					return &backend, nil
				}
			}
		}
	}

	if backend := cfg.GetDefaultBackend(); backend != nil {
		return backend, nil
	}

	return nil, fmt.Errorf("no backend available for task: %s", group.Task)
}

func mergePredictResult(dst map[string]interface{}, src map[string]interface{}) error {
	for taskName, value := range src {
		srcTaskResult, ok := value.(map[string]interface{})
		if !ok {
			if _, exists := dst[taskName]; exists {
				return fmt.Errorf("duplicate non-object result for task: %s", taskName)
			}
			dst[taskName] = value
			continue
		}

		existing, exists := dst[taskName]
		if !exists {
			dst[taskName] = srcTaskResult
			continue
		}

		dstTaskResult, ok := existing.(map[string]interface{})
		if !ok {
			return fmt.Errorf("cannot merge object result into non-object task: %s", taskName)
		}

		for typeName, typeValue := range srcTaskResult {
			if _, exists := dstTaskResult[typeName]; exists {
				return fmt.Errorf("duplicate result for task %s type %s", taskName, typeName)
			}
			dstTaskResult[typeName] = typeValue
		}
	}

	return nil
}

func normalizeTaskRouting(taskRouting map[string]string) map[string]string {
	normalized := make(map[string]string)
	for taskName, backendName := range taskRouting {
		normalized[tasks.NormalizeTaskName(taskName)] = backendName
	}
	return normalized
}

func validateRoutingBackends(backends []config.Backend, taskRouting map[string]string, modelTypeRouting map[string]string) error {
	backendNames := make(map[string]struct{}, len(backends))
	for _, backend := range backends {
		backendNames[backend.Name] = struct{}{}
	}

	for taskName, backendName := range taskRouting {
		if _, exists := backendNames[backendName]; !exists {
			return fmt.Errorf("task routing for %s references unknown backend %s", taskName, backendName)
		}
	}

	for modelType, backendName := range modelTypeRouting {
		if _, exists := backendNames[backendName]; !exists {
			return fmt.Errorf("modelType routing for %s references unknown backend %s", modelType, backendName)
		}
	}

	return nil
}

func validateBackendDefinitions(backends []config.Backend) error {
	backendNames := make(map[string]struct{}, len(backends))
	for _, backend := range backends {
		if _, exists := backendNames[backend.Name]; exists {
			return fmt.Errorf("duplicate backend name: %s", backend.Name)
		}
		backendNames[backend.Name] = struct{}{}
	}

	return nil
}

// ConfigGetHandler handles GET /config - returns web configuration UI
func ConfigGetHandler(c *gin.Context) {
	c.File("static/config.html")
}

// ConfigAPIGetHandler handles GET /api/config - returns current configuration as JSON
func ConfigAPIGetHandler(c *gin.Context) {
	data, err := cfg.ToJSON()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}
	c.Data(http.StatusOK, "application/json", data)
}

// HealthAPIGetHandler handles GET /api/health - returns health status of all backends
func HealthAPIGetHandler(c *gin.Context) {
	healthStatus := cfg.GetAllHealthStatus()
	c.JSON(http.StatusOK, healthStatus)
}

// ConfigPostHandler handles POST /api/config - saves configuration
type ConfigRequest struct {
	DefaultBackend   string            `json:"defaultBackend"`
	Backends         []config.Backend  `json:"backends"`
	TaskRouting      map[string]string `json:"taskRouting"`
	ModelTypeRouting map[string]string `json:"modelTypeRouting"`
}

func ConfigPostHandler(c *gin.Context) {
	var req ConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	// Validate that at least one backend is configured
	if len(req.Backends) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "At least one backend must be configured",
		})
		return
	}
	if err := validateBackendDefinitions(req.Backends); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	// Validate that a default backend is configured
	if req.DefaultBackend == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "A default backend must be configured",
		})
		return
	}

	// Validate that the default backend exists in the backends list
	defaultBackendExists := false
	for _, backend := range req.Backends {
		if backend.Name == req.DefaultBackend {
			defaultBackendExists = true
			break
		}
	}
	if !defaultBackendExists {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Default backend must exist in the backends list",
		})
		return
	}

	normalizedTaskRouting := normalizeTaskRouting(req.TaskRouting)
	modelTypeRouting := req.ModelTypeRouting
	if modelTypeRouting == nil {
		modelTypeRouting = make(map[string]string)
	}

	if err := validateRoutingBackends(req.Backends, normalizedTaskRouting, modelTypeRouting); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	// Update config atomically so requests do not observe a partially-written config.
	cfg.Replace(req.DefaultBackend, req.Backends, normalizedTaskRouting, modelTypeRouting)

	// Save to file
	if err := cfg.Save(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Configuration saved successfully",
	})
}
