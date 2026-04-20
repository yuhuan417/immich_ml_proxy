package config

import (
	"encoding/json"
	"immich_ml_proxy/tasks"
	"os"
	"sync"
	"time"
)

type Backend struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
	HealthStatusUnknown   HealthStatus = "unknown"
)

type RoutingPolicy string

const (
	RoutingPolicyStrict   RoutingPolicy = "strict"
	RoutingPolicyFallback RoutingPolicy = "fallback"
)

type BackendHealth struct {
	Status    HealthStatus `json:"status"`
	LastCheck int64        `json:"lastCheck"` // Unix timestamp
	Error     string       `json:"error,omitempty"`
}

type Config struct {
	DefaultBackend         string                   `json:"defaultBackend"`
	Backends               []Backend                `json:"backends"`
	TaskRouting            map[string]string        `json:"taskRouting"`            // task -> backend name mapping
	ModelTypeRouting       map[string]string        `json:"modelTypeRouting"`       // modelType -> backend name mapping (for clip: textual, visual)
	TaskRoutingPolicy      map[string]RoutingPolicy `json:"taskRoutingPolicy"`      // task -> routing policy ("strict" | "fallback")
	ModelTypeRoutingPolicy map[string]RoutingPolicy `json:"modelTypeRoutingPolicy"` // modelType -> routing policy ("strict" | "fallback")
	Health                 map[string]BackendHealth `json:"-"`                      // backend name -> health status
	mu                     sync.RWMutex
}

var (
	instance   *Config
	once       sync.Once
	configFile = "config.json"
)

func Load() *Config {
	once.Do(func() {
		instance = &Config{
			DefaultBackend:         "",
			Backends:               []Backend{},
			TaskRouting:            make(map[string]string),
			ModelTypeRouting:       make(map[string]string),
			TaskRoutingPolicy:      make(map[string]RoutingPolicy),
			ModelTypeRoutingPolicy: make(map[string]RoutingPolicy),
			Health:                 make(map[string]BackendHealth),
		}
		instance.loadFromFile()
	})
	return instance
}

func (c *Config) loadFromFile() {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(configFile)
	if err != nil {
		// File doesn't exist yet, use default configuration
		return
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return
	}

	c.DefaultBackend = cfg.DefaultBackend
	c.Backends = cfg.Backends
	c.TaskRouting = normalizeTaskRouting(cfg.TaskRouting)
	c.ModelTypeRouting = cfg.ModelTypeRouting
	c.TaskRoutingPolicy = normalizeTaskRoutingPolicy(cfg.TaskRoutingPolicy)
	c.ModelTypeRoutingPolicy = normalizeModelTypeRoutingPolicy(cfg.ModelTypeRoutingPolicy)

	if c.TaskRouting == nil {
		c.TaskRouting = make(map[string]string)
	}
	if c.ModelTypeRouting == nil {
		c.ModelTypeRouting = make(map[string]string)
	}
	if c.TaskRoutingPolicy == nil {
		c.TaskRoutingPolicy = make(map[string]RoutingPolicy)
	}
	if c.ModelTypeRoutingPolicy == nil {
		c.ModelTypeRoutingPolicy = make(map[string]RoutingPolicy)
	}
}

func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(configFile, data, 0644)
}

func (c *Config) GetBackendURL(task string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	task = tasks.NormalizeTaskName(task)

	if backendName, ok := c.TaskRouting[task]; ok {
		for _, backend := range c.Backends {
			if backend.Name == backendName {
				return backend.URL
			}
		}
	}

	// Return default backend if no task-specific routing configured
	if c.DefaultBackend != "" {
		for _, backend := range c.Backends {
			if backend.Name == c.DefaultBackend {
				return backend.URL
			}
		}
	}

	return ""
}

func (c *Config) GetAllBackendURLs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	urls := make([]string, 0, len(c.Backends))
	for _, backend := range c.Backends {
		urls = append(urls, backend.URL)
	}
	return urls
}

func (c *Config) GetBackends() []Backend {
	c.mu.RLock()
	defer c.mu.RUnlock()

	backends := make([]Backend, len(c.Backends))
	copy(backends, c.Backends)
	return backends
}

func (c *Config) AddBackend(name, url string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, b := range c.Backends {
		if b.Name == name {
			c.Backends[i].URL = url
			return
		}
	}
	c.Backends = append(c.Backends, Backend{Name: name, URL: url})
}

func (c *Config) RemoveBackend(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, b := range c.Backends {
		if b.Name == name {
			c.Backends = append(c.Backends[:i], c.Backends[i+1:]...)
			// Remove task routing for this backend
			for task, backendName := range c.TaskRouting {
				if backendName == name {
					delete(c.TaskRouting, task)
					delete(c.TaskRoutingPolicy, task)
				}
			}
			for modelType, backendName := range c.ModelTypeRouting {
				if backendName == name {
					delete(c.ModelTypeRouting, modelType)
					delete(c.ModelTypeRoutingPolicy, modelType)
				}
			}
			// Reset default backend if needed
			if c.DefaultBackend == name {
				c.DefaultBackend = ""
			}
			return
		}
	}
}

func (c *Config) SetTaskRouting(task, backendName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.TaskRouting[tasks.NormalizeTaskName(task)] = backendName
}

func (c *Config) SetDefaultBackend(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.DefaultBackend = name
}

func (c *Config) Replace(defaultBackend string, backends []Backend, taskRouting map[string]string, modelTypeRouting map[string]string, taskRoutingPolicy map[string]RoutingPolicy, modelTypeRoutingPolicy map[string]RoutingPolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()

	backendsCopy := make([]Backend, len(backends))
	copy(backendsCopy, backends)

	taskRoutingCopy := normalizeTaskRouting(taskRouting)
	modelTypeRoutingCopy := make(map[string]string, len(modelTypeRouting))
	for modelType, backendName := range modelTypeRouting {
		modelTypeRoutingCopy[modelType] = backendName
	}
	taskRoutingPolicyCopy := normalizeTaskRoutingPolicy(taskRoutingPolicy)
	modelTypeRoutingPolicyCopy := normalizeModelTypeRoutingPolicy(modelTypeRoutingPolicy)

	c.DefaultBackend = defaultBackend
	c.Backends = backendsCopy
	c.TaskRouting = taskRoutingCopy
	c.ModelTypeRouting = modelTypeRoutingCopy
	c.TaskRoutingPolicy = taskRoutingPolicyCopy
	c.ModelTypeRoutingPolicy = modelTypeRoutingPolicyCopy
}

func (c *Config) ToJSON() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Create a copy to avoid modifying the original
	result := struct {
		DefaultBackend         string                   `json:"defaultBackend"`
		Backends               []Backend                `json:"backends"`
		TaskRouting            map[string]string        `json:"taskRouting"`
		ModelTypeRouting       map[string]string        `json:"modelTypeRouting"`
		TaskRoutingPolicy      map[string]RoutingPolicy `json:"taskRoutingPolicy"`
		ModelTypeRoutingPolicy map[string]RoutingPolicy `json:"modelTypeRoutingPolicy"`
	}{
		DefaultBackend:         c.DefaultBackend,
		Backends:               c.Backends,
		TaskRouting:            normalizeTaskRouting(c.TaskRouting),
		ModelTypeRouting:       c.ModelTypeRouting,
		TaskRoutingPolicy:      normalizeTaskRoutingPolicy(c.TaskRoutingPolicy),
		ModelTypeRoutingPolicy: normalizeModelTypeRoutingPolicy(c.ModelTypeRoutingPolicy),
	}

	// Ensure maps are not nil
	if result.TaskRouting == nil {
		result.TaskRouting = make(map[string]string)
	}
	if result.ModelTypeRouting == nil {
		result.ModelTypeRouting = make(map[string]string)
	}
	if result.TaskRoutingPolicy == nil {
		result.TaskRoutingPolicy = make(map[string]RoutingPolicy)
	}
	if result.ModelTypeRoutingPolicy == nil {
		result.ModelTypeRoutingPolicy = make(map[string]RoutingPolicy)
	}

	return json.MarshalIndent(result, "", "  ")
}

// SetHealthStatus sets the health status for a backend
func (c *Config) SetHealthStatus(backendName string, status HealthStatus, error string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Health[backendName] = BackendHealth{
		Status:    status,
		LastCheck: time.Now().Unix(),
		Error:     error,
	}
}

// GetHealthStatus gets the health status for a backend
func (c *Config) GetHealthStatus(backendName string) BackendHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if health, ok := c.Health[backendName]; ok {
		return health
	}
	return BackendHealth{
		Status:    HealthStatusUnknown,
		LastCheck: 0,
	}
}

// GetAllHealthStatus returns health status for all backends
func (c *Config) GetAllHealthStatus() map[string]BackendHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]BackendHealth)
	for k, v := range c.Health {
		result[k] = v
	}
	return result
}

// GetBackendsByTask returns backends that handle the specified task.
func (c *Config) GetBackendsByTask(taskName string) []Backend {
	c.mu.RLock()
	defer c.mu.RUnlock()

	taskName = tasks.NormalizeTaskName(taskName)
	backendName, hasRouting := c.TaskRouting[taskName]

	if hasRouting {
		for _, backend := range c.Backends {
			if backend.Name == backendName {
				return []Backend{backend}
			}
		}
	}

	return []Backend{}
}

// GetBackendsByType is kept as a compatibility wrapper for older callers.
func (c *Config) GetBackendsByType(typeName string) []Backend {
	return c.GetBackendsByTask(typeName)
}

// GetHealthyBackendsByTask returns healthy backends that handle the specified task.
func (c *Config) GetHealthyBackendsByTask(taskName string) []Backend {
	c.mu.RLock()
	defer c.mu.RUnlock()

	taskName = tasks.NormalizeTaskName(taskName)
	backendName, hasRouting := c.TaskRouting[taskName]

	if hasRouting {
		for _, backend := range c.Backends {
			if backend.Name == backendName {
				if health, ok := c.Health[backend.Name]; ok && health.Status == HealthStatusHealthy {
					return []Backend{backend}
				}
				return []Backend{}
			}
		}
	}

	return []Backend{}
}

// GetHealthyBackendsByType is kept as a compatibility wrapper for older callers.
func (c *Config) GetHealthyBackendsByType(typeName string) []Backend {
	return c.GetHealthyBackendsByTask(typeName)
}

// GetAllTasks returns all tasks with explicit task-level routing.
func (c *Config) GetAllTasks() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	taskMap := make(map[string]bool)
	for task := range c.TaskRouting {
		taskMap[tasks.NormalizeTaskName(task)] = true
	}

	var result []string
	for t := range taskMap {
		result = append(result, t)
	}
	return result
}

// GetAllTypes is kept as a compatibility wrapper for older callers.
func (c *Config) GetAllTypes() []string {
	return c.GetAllTasks()
}

// GetDefaultBackend returns the default backend
func (c *Config) GetDefaultBackend() *Backend {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.DefaultBackend == "" {
		return nil
	}

	for _, backend := range c.Backends {
		if backend.Name == c.DefaultBackend {
			return &backend
		}
	}
	return nil
}

// GetBackendByName returns a configured backend by name.
func (c *Config) GetBackendByName(name string) *Backend {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, backend := range c.Backends {
		if backend.Name == name {
			return &backend
		}
	}
	return nil
}

// GetBackendByModelType returns the backend for a specific modelType (e.g., "textual", "visual")
// Returns nil if no specific routing is configured for this modelType
func (c *Config) GetBackendByModelType(modelType string) *Backend {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if backendName, ok := c.ModelTypeRouting[modelType]; ok {
		for _, backend := range c.Backends {
			if backend.Name == backendName {
				return &backend
			}
		}
	}

	return nil
}

// GetTaskRoutingPolicy returns routing policy for a task.
// Unknown or invalid values default to strict.
func (c *Config) GetTaskRoutingPolicy(task string) RoutingPolicy {
	c.mu.RLock()
	defer c.mu.RUnlock()

	task = tasks.NormalizeTaskName(task)
	if policy, ok := c.TaskRoutingPolicy[task]; ok {
		return normalizeRoutingPolicy(policy)
	}
	return RoutingPolicyStrict
}

// GetModelTypeRoutingPolicy returns routing policy for a modelType.
// Unknown or invalid values default to strict.
func (c *Config) GetModelTypeRoutingPolicy(modelType string) RoutingPolicy {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if policy, ok := c.ModelTypeRoutingPolicy[modelType]; ok {
		return normalizeRoutingPolicy(policy)
	}
	return RoutingPolicyStrict
}

// GetAllModelTypes returns all model types with explicit routing.
func (c *Config) GetAllModelTypes() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var result []string
	for modelType := range c.ModelTypeRouting {
		result = append(result, modelType)
	}
	return result
}

func normalizeTaskRouting(taskRouting map[string]string) map[string]string {
	if taskRouting == nil {
		return make(map[string]string)
	}

	normalized := make(map[string]string, len(taskRouting))
	for task, backendName := range taskRouting {
		normalized[tasks.NormalizeTaskName(task)] = backendName
	}
	return normalized
}

func normalizeTaskRoutingPolicy(taskRoutingPolicy map[string]RoutingPolicy) map[string]RoutingPolicy {
	if taskRoutingPolicy == nil {
		return make(map[string]RoutingPolicy)
	}

	normalized := make(map[string]RoutingPolicy, len(taskRoutingPolicy))
	for task, policy := range taskRoutingPolicy {
		normalized[tasks.NormalizeTaskName(task)] = normalizeRoutingPolicy(policy)
	}
	return normalized
}

func normalizeModelTypeRoutingPolicy(modelTypeRoutingPolicy map[string]RoutingPolicy) map[string]RoutingPolicy {
	if modelTypeRoutingPolicy == nil {
		return make(map[string]RoutingPolicy)
	}

	normalized := make(map[string]RoutingPolicy, len(modelTypeRoutingPolicy))
	for modelType, policy := range modelTypeRoutingPolicy {
		normalized[modelType] = normalizeRoutingPolicy(policy)
	}
	return normalized
}

func normalizeRoutingPolicy(policy RoutingPolicy) RoutingPolicy {
	switch policy {
	case RoutingPolicyFallback:
		return RoutingPolicyFallback
	default:
		return RoutingPolicyStrict
	}
}
