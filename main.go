package main

import (
	"flag"
	"immich_ml_proxy/config"
	"immich_ml_proxy/handlers"
	"log"

	"github.com/gin-gonic/gin"
)

func main() {
	// Parse command line flags
	debugMode := flag.Bool("debug", false, "Enable debug mode")
	flag.Parse()

	// Set Gin mode: Release by default, Debug only if --debug flag is provided
	if *debugMode {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

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
	log.Println("Starting Immich ML Proxy on :3004")
	if err := r.Run(":3004"); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}