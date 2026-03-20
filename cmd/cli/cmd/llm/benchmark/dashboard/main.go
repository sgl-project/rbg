/*
Copyright 2026 The RBG Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
