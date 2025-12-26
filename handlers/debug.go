package handlers

import (
	"bytes"
	"immich_ml_proxy/debug"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// DebugPageHandler handles GET /debug - returns debug UI page
func DebugPageHandler(c *gin.Context) {
	c.File("static/debug.html")
}

// DebugStatusHandler handles GET /api/debug/status - returns debug status
func DebugStatusHandler(c *gin.Context) {
	dm := debug.GetInstance()
	c.JSON(http.StatusOK, dm.GetStatus())
}

// DebugToggleHandler handles POST /api/debug/toggle - toggles debug mode
func DebugToggleHandler(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	dm := debug.GetInstance()
	dm.SetEnabled(req.Enabled)
	c.JSON(http.StatusOK, gin.H{"enabled": req.Enabled})
}

// DebugMaxRecordsHandler handles POST /api/debug/max-records - sets max records
func DebugMaxRecordsHandler(c *gin.Context) {
	var req struct {
		MaxRecords int `json:"maxRecords"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.MaxRecords < 1 || req.MaxRecords > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "maxRecords must be between 1 and 10000"})
		return
	}

	dm := debug.GetInstance()
	dm.SetMaxRecords(req.MaxRecords)
	c.JSON(http.StatusOK, gin.H{"maxRecords": req.MaxRecords})
}

// DebugRecordsHandler handles GET /api/debug/records - returns all debug records
func DebugRecordsHandler(c *gin.Context) {
	dm := debug.GetInstance()
	records := dm.GetRecords()
	c.JSON(http.StatusOK, records)
}

// DebugClearRecordsHandler handles DELETE /api/debug/records - clears all records
func DebugClearRecordsHandler(c *gin.Context) {
	dm := debug.GetInstance()
	dm.ClearRecords()
	c.JSON(http.StatusOK, gin.H{"message": "Records cleared"})
}

// DebugMiddleware is a middleware that records incoming requests and responses
func DebugMiddleware() gin.HandlerFunc {
	dm := debug.GetInstance()

	return func(c *gin.Context) {
		if !dm.IsEnabled() {
			c.Next()
			return
		}

		// Skip debug endpoints to avoid infinite loops
		if c.Request.URL.Path == "/debug" || c.Request.URL.Path == "/api/debug/status" ||
			c.Request.URL.Path == "/api/debug/toggle" || c.Request.URL.Path == "/api/debug/max-records" ||
			c.Request.URL.Path == "/api/debug/records" || c.Request.URL.Path == "/api/config" {
			c.Next()
			return
		}

		// Read request body
		var body []byte
		if c.Request.Body != nil {
			body, _ = io.ReadAll(c.Request.Body)
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}

		// Record incoming request
		recordID := debug.GenerateID()
		dm.RecordIncomingRequest(recordID, c.Request, body)

		// Restore request body
		if len(body) > 0 {
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}

		// Capture response
		writer := &debugResponseWriter{
			ResponseWriter: c.Writer,
			recordID:       recordID,
			dm:             dm,
		}
		c.Writer = writer

		c.Next()

		// Record response if not already recorded
		if !writer.recorded {
			dm.RecordIncomingResponse(recordID, writer.status, writer.Header(), writer.body.Bytes())
		}
	}
}

type debugResponseWriter struct {
	gin.ResponseWriter
	recordID string
	dm       *debug.DebugManager
	status   int
	body     *bytes.Buffer
	recorded bool
}

func (w *debugResponseWriter) WriteHeader(statusCode int) {
	w.status = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *debugResponseWriter) Write(data []byte) (int, error) {
	if w.body == nil {
		w.body = &bytes.Buffer{}
	}
	w.body.Write(data)
	return w.ResponseWriter.Write(data)
}

func (w *debugResponseWriter) WriteString(s string) (int, error) {
	if w.body == nil {
		w.body = &bytes.Buffer{}
	}
	w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}