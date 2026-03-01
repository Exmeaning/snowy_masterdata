package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Files that trigger the preprocessor when changed
var preprocessorTriggerFiles = map[string]bool{
	"master/costume3ds.json":         true,
	"master/cardCostume3ds.json":     true,
	"master/costume3dShopItems.json": true,
}

type Watcher struct {
	repoURL      string
	repoDir      string
	serveDir     string
	compressor   *Compressor
	pollInterval time.Duration
	lastCommit   string
}

func NewWatcher(repoURL, repoDir, serveDir string, compressor *Compressor, interval time.Duration) *Watcher {
	return &Watcher{
		repoURL:      repoURL,
		repoDir:      repoDir,
		serveDir:     serveDir,
		compressor:   compressor,
		pollInterval: interval,
	}
}

func (w *Watcher) Run(ctx context.Context) {
	// Get initial commit hash
	hash, err := w.getCurrentCommit(ctx)
	if err != nil {
		log.Printf("WARNING: Failed to get initial commit: %v", err)
	} else {
		w.lastCommit = hash
		log.Printf("Initial commit: %s", hash)
	}

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.checkAndUpdate(ctx)
		}
	}
}

func (w *Watcher) getCurrentCommit(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", w.repoDir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (w *Watcher) getRemoteCommit(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", w.repoDir, "ls-remote", "origin", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	parts := strings.Fields(string(out))
	if len(parts) < 1 {
		return "", fmt.Errorf("unexpected ls-remote output: %s", string(out))
	}
	return parts[0], nil
}

func (w *Watcher) checkAndUpdate(ctx context.Context) {
	remoteHash, err := w.getRemoteCommit(ctx)
	if err != nil {
		log.Printf("WARNING: Failed to check remote: %v", err)
		return
	}

	if remoteHash == w.lastCommit {
		return
	}

	log.Printf("New commit detected: %s -> %s", w.lastCommit, remoteHash)

	// Fetch and get changed files
	changedFiles, deletedFiles, err := w.fetchAndDiff(ctx, remoteHash)
	if err != nil {
		log.Printf("ERROR: Failed to fetch and diff: %v", err)
		return
	}

	log.Printf("Changed files: %d, Deleted files: %d", len(changedFiles), len(deletedFiles))

	// Check if preprocessor needs to run
	needPreprocess := false
	for _, f := range changedFiles {
		if preprocessorTriggerFiles[f] {
			needPreprocess = true
			break
		}
	}

	// Handle deleted files
	for _, f := range deletedFiles {
		if isGitPath(f) {
			continue
		}
		servePath := filepath.Join(w.serveDir, f)
		w.compressor.RemoveCompressed(servePath)
		log.Printf("Removed: %s", f)
	}

	// Handle changed/added files: copy from repo to serve dir
	var filesToCompress []string
	for _, f := range changedFiles {
		if isGitPath(f) {
			continue
		}
		srcPath := filepath.Join(w.repoDir, f)
		dstPath := filepath.Join(w.serveDir, f)

		// Ensure destination directory exists
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			log.Printf("WARNING: Failed to create dir for %s: %v", dstPath, err)
			continue
		}

		if err := copyFile(srcPath, dstPath); err != nil {
			log.Printf("WARNING: Failed to copy %s: %v", f, err)
			continue
		}

		filesToCompress = append(filesToCompress, dstPath)
	}

	// Run preprocessor if needed
	if needPreprocess {
		log.Println("Preprocessor trigger files changed, running preprocessor...")
		if err := RunPreprocessor(w.repoDir); err != nil {
			log.Printf("WARNING: Preprocessor failed: %v", err)
		} else {
			// Copy the generated file to serve dir and mark for compression
			generatedFile := "master/snowy_costumes.json"
			srcPath := filepath.Join(w.repoDir, generatedFile)
			dstPath := filepath.Join(w.serveDir, generatedFile)
			if err := copyFile(srcPath, dstPath); err != nil {
				log.Printf("WARNING: Failed to copy generated file: %v", err)
			} else {
				filesToCompress = append(filesToCompress, dstPath)
				log.Println("Preprocessor output copied to serve directory")
			}
		}
	}

	// Compress changed files
	if len(filesToCompress) > 0 {
		log.Printf("Compressing %d changed files...", len(filesToCompress))
		if err := w.compressor.CompressFiles(ctx, filesToCompress); err != nil {
			log.Printf("WARNING: Compression of changed files failed: %v", err)
		}
	}

	w.lastCommit = remoteHash
	log.Printf("Update complete. Current commit: %s", remoteHash)
}

func (w *Watcher) fetchAndDiff(ctx context.Context, remoteHash string) (changed, deleted []string, err error) {
	// Since we use --depth=1, we need a different strategy:
	// 1. Fetch the new commit
	// 2. Use diff-tree to find changes

	oldHash := w.lastCommit

	// Fetch latest
	cmd := exec.CommandContext(ctx, "git", "-C", w.repoDir, "fetch", "origin", "--depth=2")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// If fetch fails with shallow, try full unshallow
		cmd2 := exec.CommandContext(ctx, "git", "-C", w.repoDir, "fetch", "origin", "--deepen=1")
		cmd2.Stdout = os.Stdout
		cmd2.Stderr = os.Stderr
		if err2 := cmd2.Run(); err2 != nil {
			return nil, nil, fmt.Errorf("fetch failed: %v / %v", err, err2)
		}
	}

	// Reset to the new commit
	resetCmd := exec.CommandContext(ctx, "git", "-C", w.repoDir, "reset", "--hard", "origin/HEAD")
	resetCmd.Stdout = os.Stdout
	resetCmd.Stderr = os.Stderr
	if err := resetCmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("reset failed: %w", err)
	}

	// Try to get diff between old and new
	diffCmd := exec.CommandContext(ctx, "git", "-C", w.repoDir, "diff", "--name-status", oldHash, "HEAD")
	out, err := diffCmd.Output()
	if err != nil {
		// If diff fails (shallow history), fall back to listing all files as changed
		log.Printf("WARNING: git diff failed, falling back to full file list: %v", err)
		return w.listAllFiles(ctx)
	}

	return parseDiffOutput(string(out))
}

func parseDiffOutput(output string) (changed, deleted []string, err error) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		status := parts[0]
		filename := parts[len(parts)-1]

		switch {
		case status == "D":
			deleted = append(deleted, filename)
		case strings.HasPrefix(status, "R"):
			// Rename: old name is deleted, new name is changed
			if len(parts) >= 3 {
				deleted = append(deleted, parts[1])
				changed = append(changed, parts[2])
			}
		default:
			// A, M, C, T, etc. — treat as changed
			changed = append(changed, filename)
		}
	}

	return changed, deleted, scanner.Err()
}

func (w *Watcher) listAllFiles(ctx context.Context) (changed, deleted []string, err error) {
	err = filepath.Walk(w.repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, _ := filepath.Rel(w.repoDir, path)
		if isGitPath(relPath) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if !info.IsDir() {
			changed = append(changed, relPath)
		}
		return nil
	})
	return
}
