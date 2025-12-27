# Immich ML Proxy

A proxy service for Immich ML with support for multi-backend routing, type-based task distribution, health monitoring, and comprehensive debugging capabilities.

## Features

- **Multi-backend Support**: Configure multiple Immich ML backend servers
- **Type-based Routing**: Automatically route requests to different backends based on type (e.g., clip, facial_recognition, ocr)
- **Round-robin Load Balancing**: Distribute requests across multiple healthy backends for the same type
- **Health Monitoring**: Continuous health checking with automatic failover
- **Concurrent Processing**: Process multiple types in parallel for improved performance
- **Web Configuration UI**: Simple web interface for managing backends and routing with real-time health status
- **Debug Mode**: Comprehensive request/response logging and debugging tools
- **Request Recording**: Capture and inspect incoming and outgoing HTTP requests/responses

## API Endpoints

### GET /
Returns a simple web page with links to the configuration and debug interfaces.

### GET /ping
Checks the health status of all configured backends and verifies that each type has at least one healthy backend.

**Behavior**:
- Checks health of all backends in parallel by calling their `/ping` endpoint
- Updates health status for each backend based on response
- Verifies that the default backend is healthy (handles all non-routed types)
- Verifies that each type in `taskRouting` has at least one healthy backend

**Response**:
- Returns `"pong"` with HTTP 200 if:
  - Default backend is healthy
  - Every type in `taskRouting` has at least one healthy backend
- Returns HTTP 503 (Service Unavailable) if:
  - No backends are configured
  - Default backend is unhealthy
  - Any type in `taskRouting` lacks healthy backends

### POST /predict
Routes inference requests to appropriate backends based on type. Groups entries by type and processes them concurrently with health-aware round-robin load balancing.

**Request Parameters**:
- `entries`: JSON string containing task configuration with nested structure
  - Format: `{"taskName": {"type": config, ...}}`
- `image`: Image file (optional, multipart form data)
- `text`: Text content (optional, multipart form data)

**Behavior**:
- Parses entries and groups them by type (not task)
- For each type:
  - Gets healthy backends for that type from `taskRouting`
  - If no type-specific routing configured, uses default backend
  - Uses round-robin to select backend from healthy backends
  - If no healthy backends available, falls back to all backends
  - Forwards request to selected backend
  - Updates backend health status based on response (200 = healthy, other = unhealthy)
- Processes all types concurrently for better performance
- Merges results from all types and returns them in the original order

**Health Status Updates**:
- Backend marked as healthy: Returns HTTP 200
- Backend marked as unhealthy: Returns non-200 status or connection error

**Response**: JSON object with results from all types

### GET /config
Returns the web configuration interface.

### GET /api/config
Returns current configuration in JSON format.

### GET /api/health
Returns health status of all backends in real-time.

**Response**:
```json
{
  "backend1": {
    "status": "healthy",
    "lastCheck": 1735278000,
    "error": ""
  },
  "backend2": {
    "status": "unhealthy",
    "lastCheck": 1735278010,
    "error": "connection refused"
  }
}
```

**Status Values**:
- `healthy`: Backend is responding correctly
- `unhealthy`: Backend is not responding or returning errors
- `unknown`: Health status not yet checked

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
    "clip": "backend2",
    "facial_recognition": "backend1"
  }
}
```

**Configuration Fields**:
- `defaultBackend`: Name of the backend that handles all types not in `taskRouting`
- `backends`: List of backend servers with name and URL
- `taskRouting`: Maps type names to backend names (e.g., `clip` → `backend2`)

**Type Routing**:
- Types defined in `taskRouting` are routed to their specific backends
- All other types are routed to the `defaultBackend`
- Health checks verify that both default backend and routed types have healthy backends

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
# Request for facial_recognition type (routed to backend1)
curl -X POST http://localhost:3004/predict \
  -F "entries={\"facial_recognition\": {\"image\": {}}}" \
  -F "image=@photo.jpg"

# Request for clip type (routed to backend2)
curl -X POST http://localhost:3004/predict \
  -F "entries={\"clip\": {\"image\": {}}}" \
  -F "image=@photo.jpg"

# Request for unknown type (routed to defaultBackend)
curl -X POST http://localhost:3004/predict \
  -F "entries={\"ocr\": {\"image\": {}}}" \
  -F "image=@document.jpg"
```

### Health Monitoring

1. Check overall health:
   ```bash
   curl http://localhost:3004/ping
   ```
   - Returns "pong" if all types have healthy backends
   - Returns 503 if any backend is unhealthy

2. View individual backend health status:
   ```bash
   curl http://localhost:3004/api/health
   ```
   - Returns health status for all backends with timestamps and error details

3. Monitor health in real-time:
   - Visit `http://localhost:3004/config` to see live health status for each backend
   - Health status refreshes every 5 seconds automatically

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

- **Configuration**: Thread-safe singleton configuration manager with file persistence and health status tracking
- **Proxy**: Handles request parsing, type-based grouping, round-robin load balancing, and concurrent forwarding to backends
- **Health Monitoring**: Continuous health checking with automatic status updates and failover logic
- **Handlers**: HTTP endpoint handlers for configuration, prediction, health monitoring, and debugging
- **Debug**: Comprehensive request/response recording with configurable retention
- **Middleware**: Debug middleware that captures all HTTP traffic when enabled

**Routing Logic**:
1. Parse request entries and group by type
2. For each type, look up routing in `taskRouting`
3. If no routing found, use `defaultBackend`
4. Select backend using round-robin from healthy backends
5. If no healthy backends, fall back to all backends
6. Forward request and update health status based on response

**Health Check Logic**:
1. Check all backends in parallel via `/ping` endpoint
2. Verify `defaultBackend` is healthy (required for non-routed types)
3. Verify each type in `taskRouting` has at least one healthy backend
4. Return healthy only if all conditions are met