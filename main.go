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

	// API routes
	r.GET("/", handlers.RootHandler)
	r.GET("/ping", handlers.PingHandler)
	r.POST("/predict", handlers.PredictHandler)

	// Configuration routes
	r.GET("/config", handlers.ConfigGetHandler)
	r.GET("/api/config", handlers.ConfigAPIGetHandler)
	r.POST("/api/config", handlers.ConfigPostHandler)

	// Start server
	log.Println("Starting Immich ML Proxy on :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}