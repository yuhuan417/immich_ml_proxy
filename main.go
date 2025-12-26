package main

import (
	"immich_ml_proxy/config"
	"immich_ml_proxy/handlers"
	"log"

	"github.com/gin-gonic/gin"
)

func main() {
	// Load configuration
	cfg := config.Load()
	handlers.Init(cfg)

	// Create Gin router
	r := gin.Default()

	// Use debug middleware
	r.Use(handlers.DebugMiddleware())

	// API routes
	r.GET("/", handlers.RootHandler)
	r.GET("/ping", handlers.PingHandler)
	r.POST("/predict", handlers.PredictHandler)

	// Configuration routes
	r.GET("/config", handlers.ConfigGetHandler)
	r.GET("/api/config", handlers.ConfigAPIGetHandler)
	r.POST("/api/config", handlers.ConfigPostHandler)

	// Debug routes
	r.GET("/debug", handlers.DebugPageHandler)
	r.GET("/api/debug/status", handlers.DebugStatusHandler)
	r.POST("/api/debug/toggle", handlers.DebugToggleHandler)
	r.POST("/api/debug/max-records", handlers.DebugMaxRecordsHandler)
	r.GET("/api/debug/records", handlers.DebugRecordsHandler)
	r.DELETE("/api/debug/records", handlers.DebugClearRecordsHandler)

	// Start server
	log.Println("Starting Immich ML Proxy on :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}