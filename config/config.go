package config

import (
	"encoding/json"
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

type BackendHealth struct {
	Status    HealthStatus `json:"status"`
	LastCheck int64        `json:"lastCheck"` // Unix timestamp
	Error     string       `json:"error,omitempty"`
}

type Config struct {
		DefaultBackend string            `json:"defaultBackend"`
		Backends       []Backend         `json:"backends"`
		TaskRouting    map[string]string `json:"taskRouting"` // task -> backend name mapping
		Health         map[string]BackendHealth `json:"-"` // backend name -> health status
		mu             sync.RWMutex
	}
var (
	instance *Config
	once     sync.Once
	configFile = "config.json"
)

func Load() *Config {
	once.Do(func() {
		instance = &Config{
			DefaultBackend: "",
			Backends:       []Backend{},
			TaskRouting:    make(map[string]string),
			Health:         make(map[string]BackendHealth),
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
	c.TaskRouting = cfg.TaskRouting
}

func (c *Config) Save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(configFile, data, 0644)
}

func (c *Config) GetBackendURL(task string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

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
	c.TaskRouting[task] = backendName
}

func (c *Config) SetDefaultBackend(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.DefaultBackend = name
}

func (c *Config) ToJSON() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return json.MarshalIndent(c, "", "  ")
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

// GetBackendsByType returns backends that handle the specified type
func (c *Config) GetBackendsByType(typeName string) []Backend {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Check if this type has a specific routing in taskRouting
	backendName, hasRouting := c.TaskRouting[typeName]

	if hasRouting {
		// Return the specific backend for this type
		for _, backend := range c.Backends {
			if backend.Name == backendName {
				return []Backend{backend}
			}
		}
	}

	// No specific routing, return empty (type not supported)
	return []Backend{}
}

// GetHealthyBackendsByType returns healthy backends that handle the specified type
func (c *Config) GetHealthyBackendsByType(typeName string) []Backend {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Check if this type has a specific routing in taskRouting
	backendName, hasRouting := c.TaskRouting[typeName]

	if hasRouting {
		// Check if the specific backend is healthy
		for _, backend := range c.Backends {
			if backend.Name == backendName {
				if health, ok := c.Health[backend.Name]; ok && health.Status == HealthStatusHealthy {
					return []Backend{backend}
				}
				// Backend exists but is not healthy
				return []Backend{}
			}
		}
	}

	// No specific routing or backend not found
	return []Backend{}
}

// GetAllTypes returns all unique types from taskRouting
// Note: This doesn't include types handled by defaultBackend, as those are unknown
func (c *Config) GetAllTypes() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	typeMap := make(map[string]bool)
	for task := range c.TaskRouting {
		typeMap[task] = true
	}

	var result []string
	for t := range typeMap {
		result = append(result, t)
	}
	return result
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