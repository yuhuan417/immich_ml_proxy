package config

import (
	"encoding/json"
	"os"
	"sync"
)

type Backend struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Config struct {
		DefaultBackend string            `json:"defaultBackend"`
		Backends       []Backend         `json:"backends"`
		TaskRouting    map[string]string `json:"taskRouting"` // task -> backend name mapping
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