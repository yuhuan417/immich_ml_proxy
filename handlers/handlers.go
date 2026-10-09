package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"immich_ml_proxy/config"
	"immich_ml_proxy/debug"
	"immich_ml_proxy/proxy"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var cfg *config.Config

type dispatchAttempt struct {
	backend           config.Backend
	continueOnFailure bool
}

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

// PingHandler handles GET /ping - probes every backend and returns "pong" if
// each routed task and model type can be served right now.
//
// The verdict comes from this call's own probe results, never from the shared
// health field. That field is a routing hint that converges over several
// observations (see config.UnreachableThreshold), whereas a liveness probe has
// to answer for the moment it ran.
func PingHandler(c *gin.Context) {
	backends := cfg.GetBackends()
	if len(backends) == 0 {
		c.Status(http.StatusServiceUnavailable)
		return
	}

	probes := make([]proxy.BackendStatus, len(backends))
	var wg sync.WaitGroup

	for i, backend := range backends {
		wg.Add(1)
		go func(idx int, b config.Backend) {
			defer wg.Done()

			// Stamped before the request so a slow probe cannot overwrite a
			// fresh observation that completed while it was in flight.
			observedAt := time.Now()
			status := proxy.CheckBackendHealth(b.URL)
			probes[idx] = status

			if status.Status == "healthy" {
				cfg.RecordBackendReachable(b.Name, observedAt)
			} else {
				cfg.RecordBackendUnreachable(b.Name, status.Error, observedAt)
			}
		}(i, backend)
	}

	wg.Wait()

	reached := make(map[string]bool, len(backends))
	for i, backend := range backends {
		reached[backend.Name] = probes[i].Status == "healthy"
	}

	// The default backend handles everything that is not routed.
	defaultBackend := cfg.GetDefaultBackend()
	if defaultBackend == nil || !reached[defaultBackend.Name] {
		c.Status(http.StatusServiceUnavailable)
		return
	}

	// Every strict route needs a backend that answered just now. Fallback
	// routes may degrade to the default backend, so they only warn.
	for _, taskName := range cfg.GetAllTasks() {
		if routeReached(cfg.GetBackendsByTask(taskName), reached) {
			continue
		}
		if cfg.GetTaskRoutingPolicy(taskName) == config.RoutingPolicyFallback {
			continue
		}
		c.Status(http.StatusServiceUnavailable)
		return
	}

	for _, modelType := range cfg.GetAllModelTypes() {
		backend := cfg.GetBackendByModelType(modelType)
		if backend != nil && reached[backend.Name] {
			continue
		}
		if cfg.GetModelTypeRoutingPolicy(modelType) == config.RoutingPolicyFallback {
			continue
		}
		c.Status(http.StatusServiceUnavailable)
		return
	}

	c.Data(http.StatusOK, "text/plain", []byte("pong"))
}

// routeReached reports whether any backend of a route answered this probe.
func routeReached(backends []config.Backend, reached map[string]bool) bool {
	for _, backend := range backends {
		if reached[backend.Name] {
			return true
		}
	}
	return false
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

			entriesJSON, err := json.Marshal(entriesForGroup)
			if err != nil {
				resultMutex.Lock()
				groupErrors[g.Key] = err
				resultMutex.Unlock()
				return
			}

			result, err := executeDispatchGroup(c.Request, debugTraceID, g, string(entriesJSON))
			if err != nil {
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

func executeDispatchGroup(r *http.Request, debugTraceID string, group proxy.DispatchGroup, entriesJSON string) (map[string]interface{}, error) {
	attempts, err := buildDispatchAttempts(group)
	if err != nil {
		return nil, err
	}

	var attemptErrors []string
	for _, attempt := range attempts {
		result, err := executeDispatchAttempt(r, debugTraceID, attempt, entriesJSON)
		if err == nil {
			return result, nil
		}

		attemptErrors = append(attemptErrors, fmt.Sprintf("%s: %v", attempt.backend.Name, err))

		// A 4xx means the backend rejected this payload. The backend is fine,
		// so replaying the same request elsewhere cannot produce a different
		// answer — stop instead of burning the fallback chain on it.
		var statusErr *backendStatusError
		if errors.As(err, &statusErr) && statusErr.clientFault() {
			break
		}

		if !attempt.continueOnFailure {
			break
		}
	}

	if len(attemptErrors) == 0 {
		return nil, fmt.Errorf("no backend available for task: %s", group.Task)
	}
	if len(attemptErrors) == 1 {
		return nil, fmt.Errorf("%s", attemptErrors[0])
	}

	return nil, fmt.Errorf("all backends failed for %s: %s", group.Key, strings.Join(attemptErrors, "; "))
}

// backendStatusError carries the status a backend answered with, so callers can
// tell a request-level rejection from a server-level failure. Callers already
// prefix the backend name, so it is not repeated here.
type backendStatusError struct {
	code int
	body string
}

func (e *backendStatusError) Error() string {
	return fmt.Sprintf("backend returned status %d: %s", e.code, e.body)
}

// clientFault reports whether the backend rejected the request itself rather
// than failing to serve it.
func (e *backendStatusError) clientFault() bool {
	return e.code >= 400 && e.code < http.StatusInternalServerError
}

// isGatewayFailure reports whether a status means "this endpoint could not serve
// the request", as opposed to "it served the request and did not like it".
//
// Immich ML only ever answers 400, 422, 500 or 200, so these codes come from
// the endpoint or from a proxy in front of it reporting a dead upstream.
func isGatewayFailure(code int) bool {
	switch code {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func executeDispatchAttempt(r *http.Request, debugTraceID string, attempt dispatchAttempt, entriesJSON string) (map[string]interface{}, error) {
	// Stamped before the request so a slow attempt cannot overwrite a fresh
	// observation that completed while it was in flight.
	observedAt := time.Now()
	resp, bodyBytes, outgoingContentType, err := proxy.ForwardPredictRequestWithType(attempt.backend.URL, r, entriesJSON)
	debugHeaders := cloneHeaderWithContentType(r.Header, outgoingContentType)
	if err != nil {
		if debug.GetInstance().IsEnabled() {
			recordID := debug.GenerateID()
			debug.GetInstance().RecordOutgoingRequest(recordID, debugTraceID, http.MethodPost, attempt.backend.URL+"/predict", debugHeaders, bodyBytes)
			debug.GetInstance().RecordError(recordID, err)
		}

		// A transport failure is the only thing here that says the backend
		// itself is unreachable. See the health note in executeDispatchGroup.
		cfg.RecordBackendUnreachable(attempt.backend.Name, err.Error(), observedAt)
		return nil, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if debug.GetInstance().IsEnabled() {
		recordID := debug.GenerateID()
		debug.GetInstance().RecordOutgoingRequest(recordID, debugTraceID, http.MethodPost, attempt.backend.URL+"/predict", debugHeaders, bodyBytes)
		if readErr != nil {
			debug.GetInstance().RecordError(recordID, readErr)
		} else {
			debug.GetInstance().RecordOutgoingResponse(recordID, resp.StatusCode, resp.Header, body)
		}
	}

	if readErr != nil {
		// The connection broke mid-response, which is also a transport failure.
		cfg.RecordBackendUnreachable(attempt.backend.Name, readErr.Error(), observedAt)
		return nil, readErr
	}

	// The backend answered, so it is up. What it answered with decides whether
	// that counts as evidence that it cannot serve:
	//
	//   4xx          the request was at fault; the backend is fine
	//   500          ambiguous — Immich ML returns it both for a broken model
	//                and for an unhandled error on bad client data
	//   502/503/504  the endpoint itself says it cannot serve, which is what a
	//                proxy in front of a dead upstream reports
	if isGatewayFailure(resp.StatusCode) {
		cfg.RecordBackendUnreachable(attempt.backend.Name, resp.Status, observedAt)
	} else {
		cfg.RecordBackendReachable(attempt.backend.Name, observedAt)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &backendStatusError{
			code: resp.StatusCode,
			body: string(body),
		}
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("backend %s answered with an unreadable body: %w", attempt.backend.Name, err)
	}

	return result, nil
}

func buildDispatchAttempts(group proxy.DispatchGroup) ([]dispatchAttempt, error) {
	var attempts []dispatchAttempt
	seenBackends := make(map[string]struct{})

	appendAttempt := func(backend *config.Backend, continueOnFailure bool) {
		if backend == nil {
			return
		}
		if _, exists := seenBackends[backend.Name]; exists {
			return
		}

		attempts = append(attempts, dispatchAttempt{
			backend:           *backend,
			continueOnFailure: continueOnFailure,
		})
		seenBackends[backend.Name] = struct{}{}
	}

	if group.Split {
		if backend := cfg.GetBackendByModelType(group.Type); backend != nil {
			policy := cfg.GetModelTypeRoutingPolicy(group.Type)
			if policy == config.RoutingPolicyStrict || cfg.GetHealthStatus(backend.Name).Status != config.HealthStatusUnhealthy {
				appendAttempt(backend, policy == config.RoutingPolicyFallback)
			}
		}
	}

	if backend := selectTaskBackendForDispatchGroup(group); backend != nil {
		appendAttempt(backend, cfg.GetTaskRoutingPolicy(group.Task) == config.RoutingPolicyFallback)
	}

	if backend := cfg.GetDefaultBackend(); backend != nil {
		appendAttempt(backend, false)
	}

	if len(attempts) == 0 {
		return nil, fmt.Errorf("no backend available for task: %s", group.Task)
	}

	return attempts, nil
}

func selectTaskBackendForDispatchGroup(group proxy.DispatchGroup) *config.Backend {
	healthyBackends := cfg.GetHealthyBackendsByTask(group.Task)
	allBackends := cfg.GetBackendsByTask(group.Task)
	taskPolicy := cfg.GetTaskRoutingPolicy(group.Task)

	if len(allBackends) > 0 {
		candidates := allBackends
		if len(healthyBackends) > 0 {
			candidates = healthyBackends
		} else if taskPolicy == config.RoutingPolicyFallback {
			allExplicitlyUnhealthy := true
			for _, backend := range allBackends {
				backendHealth := cfg.GetHealthStatus(backend.Name)
				if backendHealth.Status != config.HealthStatusUnhealthy {
					allExplicitlyUnhealthy = false
					break
				}
			}
			if allExplicitlyUnhealthy {
				candidates = []config.Backend{}
			}
		}

		backendList := make([]string, 0, len(candidates))
		for _, backend := range candidates {
			backendList = append(backendList, backend.URL)
		}

		selectedURL := proxy.GetNextBackend(group.Key, backendList)
		if selectedURL != "" {
			for _, backend := range candidates {
				if backend.URL == selectedURL {
					return &backend
				}
			}
		}
	}

	return nil
}

func selectBackendForDispatchGroup(group proxy.DispatchGroup) (*config.Backend, error) {
	attempts, err := buildDispatchAttempts(group)
	if err != nil {
		return nil, err
	}

	return &attempts[0].backend, nil
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

func validateRoutingPolicies(taskRoutingPolicy map[string]config.RoutingPolicy, modelTypeRoutingPolicy map[string]config.RoutingPolicy) error {
	for taskName, policy := range taskRoutingPolicy {
		if policy != config.RoutingPolicyStrict && policy != config.RoutingPolicyFallback {
			return fmt.Errorf("task routing policy for %s is invalid: %s", taskName, policy)
		}
	}

	for modelType, policy := range modelTypeRoutingPolicy {
		if policy != config.RoutingPolicyStrict && policy != config.RoutingPolicyFallback {
			return fmt.Errorf("modelType routing policy for %s is invalid: %s", modelType, policy)
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
	DefaultBackend         string                          `json:"defaultBackend"`
	Backends               []config.Backend                `json:"backends"`
	TaskRouting            map[string]string               `json:"taskRouting"`
	ModelTypeRouting       map[string]string               `json:"modelTypeRouting"`
	TaskRoutingPolicy      map[string]config.RoutingPolicy `json:"taskRoutingPolicy"`
	ModelTypeRoutingPolicy map[string]config.RoutingPolicy `json:"modelTypeRoutingPolicy"`
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

	normalizedTaskRouting := config.NormalizeTaskRouting(req.TaskRouting)
	modelTypeRouting := req.ModelTypeRouting
	if modelTypeRouting == nil {
		modelTypeRouting = make(map[string]string)
	}
	taskRoutingPolicy := config.NormalizeTaskRoutingPolicy(req.TaskRoutingPolicy)
	modelTypeRoutingPolicy := req.ModelTypeRoutingPolicy
	if modelTypeRoutingPolicy == nil {
		modelTypeRoutingPolicy = make(map[string]config.RoutingPolicy)
	}

	if err := validateRoutingBackends(req.Backends, normalizedTaskRouting, modelTypeRouting); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}
	if err := validateRoutingPolicies(taskRoutingPolicy, modelTypeRoutingPolicy); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	// Update config atomically so requests do not observe a partially-written config.
	cfg.Replace(req.DefaultBackend, req.Backends, normalizedTaskRouting, modelTypeRouting, taskRoutingPolicy, modelTypeRoutingPolicy)

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
