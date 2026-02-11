package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

//go:embed static/*
var staticFS embed.FS

func main() {
	var (
		dataDir string
		port    int
	)

	flag.StringVar(&dataDir, "data-dir", "/data", "Directory containing benchmark experiment data")
	flag.IntVar(&port, "port", 8080, "Port to listen on")
	flag.Parse()

	// Validate data directory
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		log.Fatalf("Data directory does not exist: %s", dataDir)
	}

	// Create server
	server := NewServer(dataDir)

	// Setup HTTP server
	addr := fmt.Sprintf(":%d", port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: server,
	}

	// Handle graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Starting benchmark results server on http://localhost%s", addr)
		log.Printf("Serving data from: %s", dataDir)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down server...")
	_ = httpServer.Close()
}
