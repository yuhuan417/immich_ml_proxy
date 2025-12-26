package handlers

import (
	"encoding/json"
	"fmt"
	"immich_ml_proxy/config"
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
	c.JSON(http.StatusOK, gin.H{
		"message": "Immich ML",
	})
}

// PingHandler handles GET /ping - checks health status of all backends and returns "pong" if all are healthy
func PingHandler(c *gin.Context) {
	backendURLs := cfg.GetAllBackendURLs()
	if len(backendURLs) == 0 {
		c.Status(http.StatusServiceUnavailable)
		return
	}

	var wg sync.WaitGroup
	statuses := make([]proxy.BackendStatus, len(backendURLs))
	statusesMu := sync.Mutex{}

	for i, url := range backendURLs {
		wg.Add(1)
		go func(idx int, backendURL string) {
			defer wg.Done()
			status := proxy.CheckBackendHealth(backendURL)
			statusesMu.Lock()
			statuses[idx] = status
			statusesMu.Unlock()
		}(i, url)
	}

	wg.Wait()

	allHealthy := true
	for _, status := range statuses {
		if status.Status != "healthy" {
			allHealthy = false
			break
		}
	}

	if allHealthy {
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

	for typeKey, typeEntries := range groupedByType {
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

			// Get backend URL for this type
			backendURL := proxy.GetBackendURLForType(te, func(task string) string {
				return cfg.GetBackendURL(task)
			})
			if backendURL == "" {
				resultMutex.Lock()
				typeErrors[t] = fmt.Errorf("no backend configured for type: %s", t)
				resultMutex.Unlock()
				return
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
			resp, err := proxy.ForwardPredictRequestWithType(backendURL, c.Request, string(entriesJSON))
			if err != nil {
				resultMutex.Lock()
				typeErrors[t] = err
				resultMutex.Unlock()
				return
			}
			defer resp.Body.Close()

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
		}(typeKey, typeEntries)
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
		if finalResult[entry.Task] == nil {
			finalResult[entry.Task] = make(map[string]interface{})
		}
		taskResult := finalResult[entry.Task].(map[string]interface{})

		// Extract result for this type
		typeResult := typeResults[entry.Type].(map[string]interface{})
		if taskResult[entry.Type] == nil {
			taskResult[entry.Type] = typeResult[entry.Type]
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

// ConfigPostHandler handles POST /api/config - saves configuration
type ConfigRequest struct {
	DefaultBackend string            `json:"defaultBackend"`
	Backends       []config.Backend  `json:"backends"`
	TaskRouting    map[string]string `json:"taskRouting"`
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