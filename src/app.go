package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"crevo-builder/internal/api"
	"crevo-builder/internal/config"
	"crevo-builder/internal/logging"
)

const version = "1.0.5"

func main() {
	// Tune Go runtime memory & GC for low-memory environments (e.g. Render 512MB free tier)
	debug.SetGCPercent(50)
	debug.SetMemoryLimit(160 * 1024 * 1024) // 160 MiB limit for Go heap

	// Load configuration with CLI flags
	cfg, err := config.LoadWithCLI(version)
	if err != nil {
		fmt.Printf("\n❌ ERROR: %s\n\n", err)
		fmt.Println("Run with --help to see usage information")
		os.Exit(1)
	}

	// Initialize logger with debug flag
	logger := logging.Init(cfg.Debug)
	if cfg.Debug {
		logger.Info("Debug mode enabled")
	}

	// Create storage directories (hardcoded to ./storage)
	if err := createDirectories(); err != nil {
		fmt.Printf("\n❌ ERROR: %s\n\n", err)
		fmt.Println("Failed to create storage directories")
		os.Exit(1)
	}

	// Ensure shared template node_modules cache is linked under ./storage/workspaces/.shared_cache/node_modules
	// CRITICAL: Directory basename MUST literally be "node_modules" for Node.js ESM resolution across symlinks
	if _, err := os.Stat("/opt/template-cache/node_modules"); err == nil {
		// Remove any legacy broken .shared_node_modules symlink from earlier builds
		_ = os.Remove("./storage/workspaces/.shared_node_modules")
		cacheParent := "./storage/workspaces/.shared_cache"
		_ = os.MkdirAll(cacheParent, 0755)
		targetShared := filepath.Join(cacheParent, "node_modules")
		if _, err := os.Stat(targetShared); err != nil {
			_ = os.Remove(targetShared)
			_ = os.Symlink("/opt/template-cache/node_modules", targetShared)
		}
	}

	// Background memory scavenger: regularly return unused virtual memory pages to OS
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		for range ticker.C {
			debug.FreeOSMemory()
		}
	}()

	// Background workspace cleaner: remove stale workspaces (>12 hours old) to prevent disk/page-cache bloat
	go func() {
		cleanTicker := time.NewTicker(15 * time.Minute)
		for range cleanTicker.C {
			cleanOldWorkspaces("./storage/workspaces", 12*time.Hour)
		}
	}()

	// Create and start server
	server := api.NewServer(api.ServerConfig{
		Host:          cfg.Host,
		Port:          fmt.Sprintf("%d", cfg.Port),
		ServerKey:     cfg.Key,
		WorkspacePath: "./storage/workspaces",
		Version:       version,
		Debug:         cfg.Debug,
	}, logger)

	// Start HTTP server in goroutine
	go func() {
		addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
		if err := server.RunAddr(addr); err != nil {
			fmt.Printf("\n❌ ERROR: %s\n\n", err)
			fmt.Println("Server failed to start")
			os.Exit(1)
		}
	}()

	// Print startup banner
	fmt.Println()
	fmt.Println("🌐 CREVO BUILD SERVER")
	fmt.Printf("📦 Version:   %s\n", version)
	fmt.Printf("🔌 Port:      %d\n", cfg.Port)
	fmt.Println()

	// Wait for interrupt signal
	logging.Info("Server is running. Press Ctrl+C to stop.")
	waitForShutdown()

	logging.Info("Shutting down gracefully...")
}

// createDirectories ensures all required directories exist
func createDirectories() error {
	dirs := []string{
		"./storage/workspaces",
		"./storage/logs",
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	return nil
}

func waitForShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan
}

// cleanOldWorkspaces deletes workspaces that have not been modified within maxAge
// (excluding shared node_modules caches) to prevent inode and page-cache bloat on low-memory containers.
func cleanOldWorkspaces(workspacesDir string, maxAge time.Duration) {
	entries, err := os.ReadDir(workspacesDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".shared_cache" || entry.Name() == ".shared_node_modules" {
			continue
		}
		entryPath := filepath.Join(workspacesDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(entryPath)
		}
	}
	debug.FreeOSMemory()
}
