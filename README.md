# Immich ML Proxy

A proxy service for Immich ML with support for multi-backend routing, task-aware dispatch, health monitoring, and comprehensive debugging capabilities.

## Features

- **Multi-backend Support**: Configure multiple Immich ML backend servers
- **Task-aware Routing**: Keep dependent task sub-types together for tasks like `facial-recognition` and `ocr`
- **CLIP Split Routing**: Route `clip.textual` and `clip.visual` independently when they should run on different backends
- **Per-route Policy**: Configure each task/modelType route as `strict` or `fallback`
- **Round-robin Load Balancing**: Distribute requests across healthy backends for each routed task
- **Health Monitoring**: Continuous health checking with automatic failover
- **Concurrent Processing**: Process independent dispatch groups in parallel for improved performance
- **Web Configuration UI**: Simple web interface for managing backends and routing with real-time health status
- **Debug Mode**: Comprehensive request/response logging and debugging tools
- **Request Recording**: Capture and inspect incoming and outgoing HTTP requests/responses

## API Endpoints

### GET /
Returns a simple web page with links to the configuration and debug interfaces.

### GET /ping
Checks the health status of all configured backends and verifies that each routed task/model type has a healthy backend.

**Behavior**:
- Checks health of all backends in parallel by calling their `/ping` endpoint
- Updates health status for each backend based on response
- Verifies that the default backend is healthy (handles all non-routed types)
- Verifies strict task routes in `taskRouting` have a healthy backend
- Verifies strict CLIP `modelTypeRouting` targets are healthy
- Allows fallback routes to degrade to `defaultBackend` when their routed backend is unhealthy

**Response**:
- Returns `"pong"` with HTTP 200 if:
  - Default backend is healthy
- Every strict task route in `taskRouting` has at least one healthy backend
- Every strict CLIP `modelTypeRouting` target is healthy
- Returns HTTP 503 (Service Unavailable) if:
  - No backends are configured
  - Default backend is unhealthy
- Any strict task route in `taskRouting` lacks healthy backends
- Any strict CLIP `modelTypeRouting` target is unhealthy

### POST /predict
Routes inference requests to appropriate backends based on task semantics. Dependent tasks stay grouped, while CLIP can be split by model type and merged back into one response.

**Request Parameters**:
- `entries`: JSON string containing task configuration with nested structure
  - Format: `{"taskName": {"type": config, ...}}`
- `image`: Image file (optional, multipart form data)
- `text`: Text content (optional, multipart form data)

**Behavior**:
- Normalizes legacy `facial_recognition` requests/config entries to `facial-recognition`
- Keeps `facial-recognition` and `ocr` grouped so their dependent sub-types are forwarded together
- Splits `clip` into independent `textual` and `visual` requests
- Supports CLIP requests that contain either one model type or both model types
- For each dispatch group:
  - Uses `modelTypeRouting` first for split CLIP requests
  - Falls back to task routing from `taskRouting`
  - Falls back again to `defaultBackend` if no task-specific route exists
  - Applies per-route policy:
    - `strict`: keep routed backend even when unhealthy
    - `fallback`: if routed backend is unhealthy, degrade to next fallback level
  - Updates backend health status based on response (200 = healthy, other = unhealthy)
- Processes dispatch groups concurrently and merges split CLIP responses back into one JSON response

**Health Status Updates**:
- Backend marked as healthy: Returns HTTP 200
- Backend marked as unhealthy: Returns non-200 status or connection error

**Response**: JSON object with results merged back under their original task keys

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
    },
    {
      "name": "backend3",
      "url": "http://localhost:3005"
    }
  ],
  "taskRouting": {
    "facial-recognition": "backend1",
    "search": "backend2"
  },
  "modelTypeRouting": {
    "textual": "backend2",
    "visual": "backend3"
  },
  "taskRoutingPolicy": {
    "search": "strict"
  },
  "modelTypeRoutingPolicy": {
    "textual": "fallback",
    "visual": "strict"
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
    },
    {
      "name": "backend3",
      "url": "http://localhost:3005"
    }
  ],
  "taskRouting": {
    "clip": "backend2",
    "facial-recognition": "backend1"
  },
  "modelTypeRouting": {
    "textual": "backend2",
    "visual": "backend3"
  },
  "taskRoutingPolicy": {
    "clip": "strict",
    "facial-recognition": "strict"
  },
  "modelTypeRoutingPolicy": {
    "textual": "fallback",
    "visual": "strict"
  }
}
```

**Configuration Fields**:
- `defaultBackend`: Name of the backend that handles tasks without explicit routing
- `backends`: List of backend servers with name and URL
- `taskRouting`: Maps task names to backend names (e.g., `facial-recognition` → `backend1`)
- `modelTypeRouting`: Maps CLIP model types to backend names (e.g., `textual` → `backend2`)
- `taskRoutingPolicy`: Optional per-task policy: `strict` or `fallback`
- `modelTypeRoutingPolicy`: Optional per-modelType policy: `strict` or `fallback`

**Dispatch Rules**:
- `facial-recognition` and `ocr` stay grouped and are routed using `taskRouting`
- `clip` can arrive with `textual`, `visual`, or both, and each present model type is routed independently via `modelTypeRouting`, with fallback to `taskRouting["clip"]` and then `defaultBackend`
- `strict` route policy keeps routed backend even if unhealthy
- `fallback` route policy degrades to the next fallback level when routed backend is unhealthy
- All other tasks are routed to the `defaultBackend`
- Health checks verify the default backend, routed tasks, and configured CLIP model-type routes

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
# Request for grouped facial-recognition (routed to backend1)
curl -X POST http://localhost:3004/predict \
  -F "entries={\"facial-recognition\": {\"detection\": {}, \"recognition\": {}}}" \
  -F "image=@photo.jpg"

# Request for split CLIP with both model types present
curl -X POST http://localhost:3004/predict \
  -F "entries={\"clip\": {\"textual\": {}, \"visual\": {}}}" \
  -F "image=@photo.jpg"

# Request for CLIP textual only
curl -X POST http://localhost:3004/predict \
  -F "entries={\"clip\": {\"textual\": {}}}" \
  -F "text=cat on a sofa"

# Request for grouped OCR (types stay together on one backend)
curl -X POST http://localhost:3004/predict \
  -F "entries={\"ocr\": {\"detection\": {}, \"recognition\": {}}}" \
  -F "image=@document.jpg"
```

### Health Monitoring

1. Check overall health:
   ```bash
   curl http://localhost:3004/ping
   ```
   - Returns "pong" if all routed tasks/model types have healthy backends
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
- **Proxy**: Handles request parsing, task-aware grouping/splitting, round-robin load balancing, and concurrent forwarding to backends
- **Health Monitoring**: Continuous health checking with automatic status updates and failover logic
- **Handlers**: HTTP endpoint handlers for configuration, prediction, health monitoring, and debugging
- **Debug**: Comprehensive request/response recording with configurable retention
- **Middleware**: Debug middleware that captures all HTTP traffic when enabled

**Routing Logic**:
1. Parse request entries and normalize task names such as `facial_recognition` → `facial-recognition`
2. Keep `facial-recognition` and `ocr` grouped by task so dependent sub-types stay together
3. Split `clip` into `textual` / `visual` dispatch groups for whichever model types are present
4. Route split CLIP groups with `modelTypeRouting`, otherwise use `taskRouting`
5. Apply route policy (`strict` or `fallback`) at each routing level
6. Fall back to `defaultBackend` when no explicit route is selected
7. Forward each dispatch group and merge split-task responses back together

**Health Check Logic**:
1. Check all backends in parallel via `/ping` endpoint
2. Verify `defaultBackend` is healthy (required for non-routed types)
3. Verify each strict task route in `taskRouting` has at least one healthy backend
4. Verify each strict CLIP `modelTypeRouting` backend is healthy
5. Return healthy only if all conditions are met
