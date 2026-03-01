package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

const (
	repoURL      = "https://github.com/Team-Haruki/haruki-sekai-master.git"
	repoDir      = "/data/repo"
	serveDir     = "/data/serve"
	syncMarker   = "/data/serve/.git_synced"
	pollInterval = 1 * time.Minute
	maxWorkers   = 0 // 0 = use runtime.NumCPU()
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("=== Haruki Builder Starting (CPUs: %d) ===", runtime.NumCPU())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
	}()

	// Phase 1: Full clone
	if err := fullClone(ctx); err != nil {
		log.Fatalf("Initial clone failed: %v", err)
	}

	// Run preprocessor on initial clone
	log.Println("Running preprocessor on initial clone...")
	if err := RunPreprocessor(repoDir); err != nil {
		log.Printf("WARNING: Preprocessor failed on initial clone: %v", err)
	}

	// Phase 2: Copy repo to serve directory and full compress
	if err := syncToServeDir(); err != nil {
		log.Fatalf("Initial sync failed: %v", err)
	}

	// Create sync marker for the entrypoint script
	if err := os.WriteFile(syncMarker, []byte(time.Now().UTC().String()), 0644); err != nil {
		log.Printf("WARNING: Failed to write sync marker: %v", err)
	}

	log.Println("Starting full pre-compression of all files...")
	compressor := NewCompressor(maxWorkers)
	if err := compressor.CompressAll(ctx, serveDir); err != nil {
		log.Fatalf("Initial compression failed: %v", err)
	}

	// Phase 3: Start watcher loop
	watcher := NewWatcher(repoURL, repoDir, serveDir, compressor, pollInterval)
	watcher.Run(ctx)

	log.Println("=== Haruki Builder Stopped ===")
}

func fullClone(ctx context.Context) error {
	if _, err := os.Stat(repoDir); err == nil {
		log.Println("Removing existing repo directory...")
		if err := os.RemoveAll(repoDir); err != nil {
			return err
		}
	}

	log.Printf("Cloning %s into %s ...", repoURL, repoDir)
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", repoURL, repoDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	log.Println("Clone completed.")
	return nil
}

func syncToServeDir() error {
	log.Printf("Syncing repo to serve directory %s ...", serveDir)

	if err := os.MkdirAll(serveDir, 0755); err != nil {
		return err
	}

	return filepath.Walk(repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, _ := filepath.Rel(repoDir, path)

		if isGitPath(relPath) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		destPath := filepath.Join(serveDir, relPath)

		if info.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}

		return copyFile(path, destPath)
	})
}

func isGitPath(relPath string) bool {
	if relPath == ".git" {
		return true
	}
	if len(relPath) >= 5 && relPath[:5] == ".git/" {
		return true
	}
	return false
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

type WorkerPool struct {
	sem chan struct{}
	wg  sync.WaitGroup
}

func NewWorkerPool(size int) *WorkerPool {
	return &WorkerPool{
		sem: make(chan struct{}, size),
	}
}

func (p *WorkerPool) Submit(fn func()) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.sem <- struct{}{}
		defer func() { <-p.sem }()
		fn()
	}()
}

func (p *WorkerPool) Wait() {
	p.wg.Wait()
}
