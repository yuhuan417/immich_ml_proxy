# Immich ML Proxy

A proxy service for Immich ML with support for multi-backend routing, task distribution, and comprehensive debugging capabilities.

## Features

- **Multi-backend Support**: Configure multiple Immich ML backend servers
- **Task-based Routing**: Automatically route requests to different backends based on task type (e.g., facial_recognition, search)
- **Concurrent Processing**: Process multiple tasks in parallel for improved performance
- **Health Checks**: Check health status of all configured backends
- **Web Configuration UI**: Simple web interface for managing backends and routing
- **Debug Mode**: Comprehensive request/response logging and debugging tools
- **Request Recording**: Capture and inspect incoming and outgoing HTTP requests/responses

## API Endpoints

### GET /
Returns a simple web page with links to the configuration and debug interfaces.

### GET /ping
Checks the health status of all configured backends.

**Response**:
- Returns `"pong"` with HTTP 200 if all backends are healthy
- Returns HTTP 503 (Service Unavailable) if any backend is unhealthy or no backends are configured

### POST /predict
Routes inference requests to appropriate backends based on task type. Groups entries by task and processes them concurrently.

**Request Parameters**:
- `entries`: JSON string containing task configuration with nested structure
  - Format: `{"taskName": {"type": config, ...}}`
- `image`: Image file (optional, multipart form data)
- `text`: Text content (optional, multipart form data)

**Behavior**:
- Parses entries and groups them by task
- For each task, forwards the request to the configured backend
- Processes all tasks concurrently for better performance
- Merges results from all tasks and returns them in the original order

**Response**: JSON object with results from all tasks

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

### GET /debug
Returns the debug monitoring interface.

### GET /api/debug/status
Returns current debug status.

**Response**:
```json
{
  "enabled": true,
  "maxRecords": 100,
  "recordCount": 42
}
```

### POST /api/debug/toggle
Enables or disables debug mode.

**Request Body**:
```json
{
  "enabled": true
}
```

### POST /api/debug/max-records
Sets the maximum number of debug records to keep (1-10000).

**Request Body**:
```json
{
  "maxRecords": 500
}
```

### GET /api/debug/records
Returns all debug records (incoming and outgoing HTTP requests/responses).

### DELETE /api/debug/records
Clears all debug records.

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

# Run the service (production mode)
go run main.go

# Run the service with debug mode enabled
go run main.go --debug
```

The service listens on port `:3004` by default.

## Usage Example

### Basic Setup

1. Start the service:
   ```bash
   go run main.go
   ```

2. Visit `http://localhost:3004/config` to configure backends

3. Add backend servers and configure task routing

4. Save configuration

### Making Predictions

Send a POST request to `http://localhost:3004/predict` with multipart form data:

```bash
curl -X POST http://localhost:3004/predict \
  -F "entries={\"facial_recognition\": {\"image\": {}}}" \
  -F "image=@photo.jpg"
```

### Debugging

1. Enable debug mode:
   - Visit `http://localhost:3004/debug` and click "Enable Debug"
   - Or use the API: `POST /api/debug/toggle` with `{"enabled": true}`

2. Make some requests

3. View recorded requests/responses at `http://localhost:3004/debug`

4. Clear records when needed: `DELETE /api/debug/records`

## Project Structure

```
immich_ml_proxy/
├── main.go              # Main entry point
├── config/
│   └── config.go        # Configuration management (singleton pattern)
├── proxy/
│   └── proxy.go         # Proxy logic and request forwarding
├── handlers/
│   ├── handlers.go      # Main HTTP handlers
│   └── debug.go         # Debug-related handlers
├── debug/
│   └── debug.go         # Debug manager for request/response recording
└── static/
    ├── config.html      # Web configuration interface
    └── debug.html       # Debug monitoring interface
```

## Architecture

- **Configuration**: Thread-safe singleton configuration manager with file persistence
- **Proxy**: Handles request parsing, task grouping, and concurrent forwarding to backends
- **Handlers**: HTTP endpoint handlers for configuration, prediction, and debugging
- **Debug**: Comprehensive request/response recording with configurable retention
- **Middleware**: Debug middleware that captures all HTTP traffic when enabled