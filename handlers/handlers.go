package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"immich_ml_proxy/config"
	"immich_ml_proxy/debug"
	"immich_ml_proxy/proxy"
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

// PingHandler handles GET /ping - checks health status of all backends and returns "pong" if each type has at least one healthy backend
func PingHandler(c *gin.Context) {
	backendURLs := cfg.GetAllBackendURLs()
	if len(backendURLs) == 0 {
		c.Status(http.StatusServiceUnavailable)
		return
	}

	var wg sync.WaitGroup
	statuses := make([]proxy.BackendStatus, len(backendURLs))
	statusesMu := sync.Mutex{}

	// Check health of all backends in parallel
	for i, backend := range cfg.Backends {
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

	// Check if each type in taskRouting has at least one healthy backend
	allTypes := cfg.GetAllTypes()
	allTypesHealthy := true

	for _, typeName := range allTypes {
		healthyBackends := cfg.GetHealthyBackendsByType(typeName)
		if len(healthyBackends) == 0 {
			allTypesHealthy = false
			break
		}
	}

	if allTypesHealthy {
		c.Data(http.StatusOK, "text/plain", []byte("pong"))
	} else {
		c.Status(http.StatusServiceUnavailable)
	}
}

// PredictHandler handles POST /predict - routes requests by type, merges same-type entries, and preserves order
func PredictHandler(c *gin.Context) {
	// Parse entries to determine task type
	entriesMap, err := proxy.ParseEntriesFromRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid entries: " + err.Error(),
		})
		return
	}

	// Parse entries with task, type, and order information
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

// Group entries by type
	groupedByType := proxy.GroupEntriesByType(entries)

	// For each type, build entries and forward to backend
	typeResults := make(map[string]interface{})
	typeErrors := make(map[string]error)
	var resultMutex sync.Mutex
	var wg sync.WaitGroup

	for typeName, typeEntries := range groupedByType {
		wg.Add(1)
		go func(t string, te []proxy.Entry) {
			defer wg.Done()

			// Build entries for this type
			entriesForType, err := proxy.BuildEntriesForType(te)
			if err != nil {
				resultMutex.Lock()
				typeErrors[t] = err
				resultMutex.Unlock()
				return
			}

			// Check if this is a clip task with modelType routing
			var selectedBackend *config.Backend
			for _, entry := range te {
				if entry.Task == "clip" {
					// For clip task, try to route by modelType (textual/visual)
					backend := cfg.GetBackendByModelType(entry.Type)
					if backend != nil {
						selectedBackend = backend
						break
					}
				}
			}

			// If no modelType-specific backend found, fall back to task type routing
			if selectedBackend == nil {
				// Get healthy backends for this type
				healthyBackends := cfg.GetHealthyBackendsByType(t)

				// Get all backends for this type
				allBackends := cfg.GetBackendsByType(t)

				// Fallback to default backend if no type-specific backends
				if len(allBackends) == 0 {
					// No type-specific backends, use default backend
					if cfg.DefaultBackend != "" {
						for _, b := range cfg.Backends {
							if b.Name == cfg.DefaultBackend {
								selectedBackend = &b
								break
							}
						}
					}
					if selectedBackend == nil {
						resultMutex.Lock()
						typeErrors[t] = fmt.Errorf("no backend available for type: %s", t)
						resultMutex.Unlock()
						return
					}
				} else {
					// Use round-robin to select backend
					var backendList []string
					if len(healthyBackends) > 0 {
						// Prefer healthy backends
						for _, b := range healthyBackends {
							backendList = append(backendList, b.URL)
						}
					} else {
						// No healthy backends, use all backends
						for _, b := range allBackends {
							backendList = append(backendList, b.URL)
						}
					}

					// Use round-robin to select backend
					selectedURL := proxy.GetNextBackend(t, backendList)
					if selectedURL == "" {
						resultMutex.Lock()
						typeErrors[t] = fmt.Errorf("no backend available for type: %s", t)
						resultMutex.Unlock()
						return
					}

					// Find backend by URL
					for _, b := range allBackends {
						if b.URL == selectedURL {
							selectedBackend = &b
							break
						}
					}
				}
			}

			// Create request with entries for this type
			entriesJSON, err := json.Marshal(entriesForType)
			if err != nil {
				resultMutex.Lock()
				typeErrors[t] = err
				resultMutex.Unlock()
				return
			}

			// Forward request to backend
			resp, bodyBytes, err := proxy.ForwardPredictRequestWithType(selectedBackend.URL, c.Request, string(entriesJSON))
			if err != nil {
				// Record error for debug
				if debug.GetInstance().IsEnabled() {
					recordID := debug.GenerateID()
					debug.GetInstance().RecordOutgoingRequest(recordID, "POST", selectedBackend.URL+"/predict", c.Request.Header, bodyBytes)
					debug.GetInstance().RecordError(recordID, err)
				}

				// Mark backend as unhealthy
				cfg.SetHealthStatus(selectedBackend.Name, config.HealthStatusUnhealthy, err.Error())

				resultMutex.Lock()
				typeErrors[t] = err
				resultMutex.Unlock()
				return
			}
			defer resp.Body.Close()

			// Update health status based on response
			if resp.StatusCode == http.StatusOK {
				cfg.SetHealthStatus(selectedBackend.Name, config.HealthStatusHealthy, "")
			} else {
				body, _ := io.ReadAll(resp.Body)
				cfg.SetHealthStatus(selectedBackend.Name, config.HealthStatusUnhealthy, fmt.Sprintf("status %d: %s", resp.StatusCode, string(body)))
				resp.Body = io.NopCloser(bytes.NewReader(body))
			}

			// Record outgoing request and response for debug
			if debug.GetInstance().IsEnabled() {
				recordID := debug.GenerateID()
				debug.GetInstance().RecordOutgoingRequest(recordID, "POST", selectedBackend.URL+"/predict", c.Request.Header, bodyBytes)
				body, _ := io.ReadAll(resp.Body)
				resp.Body = io.NopCloser(bytes.NewReader(body))
				debug.GetInstance().RecordOutgoingResponse(recordID, resp.StatusCode, resp.Header, body)
				// Restore body for subsequent reads
				resp.Body = io.NopCloser(bytes.NewReader(body))
			}

			// Read response
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				resultMutex.Lock()
				typeErrors[t] = err
				resultMutex.Unlock()
				return
			}

			if resp.StatusCode != http.StatusOK {
				resultMutex.Lock()
				typeErrors[t] = fmt.Errorf("backend returned status %d: %s", resp.StatusCode, string(body))
				resultMutex.Unlock()
				return
			}

			// Parse response
			var result map[string]interface{}
			if err := json.Unmarshal(body, &result); err != nil {
				resultMutex.Lock()
				typeErrors[t] = err
				resultMutex.Unlock()
				return
			}

			resultMutex.Lock()
			typeResults[t] = result
			resultMutex.Unlock()
		}(typeName, typeEntries)
	}

	wg.Wait()

	// Check for errors
	if len(typeErrors) > 0 {
		var errMsgs []string
		for t, err := range typeErrors {
			errMsgs = append(errMsgs, fmt.Sprintf("type %s: %v", t, err))
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to process some types",
			"errors": errMsgs,
		})
		return
	}

	// Assemble results in original order
	finalResult := make(map[string]interface{})
	for _, entry := range entries {
		typeResult, exists := typeResults[entry.Type]
		if exists {
			// typeResult is already in the format {"taskName": {...}}
			// Merge it directly into finalResult
			for key, value := range typeResult.(map[string]interface{}) {
				finalResult[key] = value
			}
		}
	}

	// Return assembled result
	c.JSON(http.StatusOK, finalResult)
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

	// Update config
	cfg.DefaultBackend = req.DefaultBackend
	cfg.Backends = req.Backends
	cfg.TaskRouting = req.TaskRouting
	cfg.ModelTypeRouting = req.ModelTypeRouting

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