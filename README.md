# Immich ML Proxy

A proxy service for Immich ML with support for multi-backend routing and task distribution.

## Features

- **Multi-backend Support**: Configure multiple Immich ML backend servers
- **Task Routing**: Automatically route to different backends based on task type
- **Health Checks**: Check health status of all backends
- **Web UI**: Simple web configuration interface

## API Endpoints

### GET /
Forwards to the default backend and returns service information.

### GET /ping
Checks the health status of all configured backends.

**Response Example**:
```json
{
  "backends": [
    {
      "url": "http://localhost:3003",
      "status": "healthy"
    }
  ],
  "allHealthy": true
}
```

### POST /predict
Routes inference requests to the appropriate backend based on task type.

**Request Parameters**:
- `entries`: JSON string containing task configuration
- `image`: Image file (optional)
- `text`: Text content (optional)

### GET /config
Returns the web configuration interface.

### GET /api/config
Returns current configuration in JSON format.

### POST /api/config
Saves configuration.

**Request Body**:
```json
{
  "defaultBackend": "backend1",
  "backends": [
    {
      "name": "backend1",
      "url": "http://localhost:3003"
    }
  ],
  "taskRouting": {
    "facial_recognition": "backend1",
    "search": "backend1"
  }
}
```

## Configuration

Configuration is saved in `config.json`:

```json
{
  "defaultBackend": "backend1",
  "backends": [
    {
      "name": "backend1",
      "url": "http://localhost:3003"
    },
    {
      "name": "backend2",
      "url": "http://localhost:3004"
    }
  ],
  "taskRouting": {
    "facial_recognition": "backend1",
    "search": "backend2"
  }
}
```

## Running

```bash
# Install dependencies
go mod download

# Run the service
go run main.go
```

The service listens on port `:8080` by default.

## Usage Example

1. Start the service and visit `http://localhost:8080/config` to configure
2. Add backend servers
3. Configure task routing
4. Save configuration
5. Use `http://localhost:8080/predict` for inference requests

## Project Structure

```
immich_ml_proxy/
├── main.go           # Main entry point
├── config/
│   └── config.go     # Configuration management
├── proxy/
│   └── proxy.go      # Proxy logic
├── handlers/
│   └── handlers.go   # HTTP handlers
└── static/
    └── config.html   # Web UI
```