package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/pgzip"
)

// File extensions that benefit from precompression
var compressibleExtensions = map[string]bool{
	".json":  true,
	".js":    true,
	".css":   true,
	".html":  true,
	".htm":   true,
	".xml":   true,
	".svg":   true,
	".txt":   true,
	".csv":   true,
	".md":    true,
	".yaml":  true,
	".yml":   true,
	".toml":  true,
	".ini":   true,
	".cfg":   true,
	".conf":  true,
	".log":   true,
	".map":   true,
	".wasm":  true,
	".ico":   true,
	".ttf":   true,
	".otf":   true,
	".eot":   true,
	".woff":  true,
	".woff2": true,
}

// Minimum file size for compression (bytes)
const minCompressSize = 256

type Compressor struct {
	workers int
}

func NewCompressor(workers int) *Compressor {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	return &Compressor{workers: workers}
}

func isCompressible(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return compressibleExtensions[ext]
}

// CompressAll performs gzip + brotli precompression on all eligible files in dir
func (c *Compressor) CompressAll(ctx context.Context, dir string) error {
	start := time.Now()
	var totalFiles int64
	var compressedFiles int64
	var totalOrigSize int64
	var mu sync.Mutex
	_ = mu

	pool := NewWorkerPool(c.workers)
	var walkErr error

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if info.IsDir() {
			return nil
		}

		// Skip already compressed files
		if strings.HasSuffix(path, ".gz") || strings.HasSuffix(path, ".br") {
			return nil
		}

		atomic.AddInt64(&totalFiles, 1)

		if !isCompressible(path) {
			return nil
		}

		if info.Size() < minCompressSize {
			return nil
		}

		pool.Submit(func() {
			if err := c.compressFile(path); err != nil {
				log.Printf("WARNING: Failed to compress %s: %v", path, err)
				return
			}
			atomic.AddInt64(&compressedFiles, 1)
			atomic.AddInt64(&totalOrigSize, info.Size())
		})

		return nil
	})

	pool.Wait()

	if err != nil {
		return err
	}
	if walkErr != nil {
		return walkErr
	}

	elapsed := time.Since(start)
	log.Printf("Compression complete: %d/%d files compressed, %.1f MB original, took %v",
		compressedFiles, totalFiles, float64(totalOrigSize)/1024/1024, elapsed)

	return nil
}

// CompressFiles compresses specific files (used for incremental updates)
func (c *Compressor) CompressFiles(ctx context.Context, files []string) error {
	pool := NewWorkerPool(c.workers)

	for _, f := range files {
		file := f
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		pool.Submit(func() {
			info, err := os.Stat(file)
			if err != nil {
				// File might have been deleted
				// Clean up stale .gz/.br
				os.Remove(file + ".gz")
				os.Remove(file + ".br")
				return
			}

			if !isCompressible(file) || info.Size() < minCompressSize {
				// Remove stale compressed versions if file no longer qualifies
				os.Remove(file + ".gz")
				os.Remove(file + ".br")
				return
			}

			if err := c.compressFile(file); err != nil {
				log.Printf("WARNING: Failed to compress %s: %v", file, err)
			}
		})
	}

	pool.Wait()
	return nil
}

func (c *Compressor) compressFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// Gzip compression
	if err := compressGzip(path+".gz", data); err != nil {
		return fmt.Errorf("gzip %s: %w", path, err)
	}

	// Brotli compression
	if err := compressBrotli(path+".br", data); err != nil {
		return fmt.Errorf("brotli %s: %w", path, err)
	}

	return nil
}

func compressGzip(outputPath string, data []byte) error {
	var buf bytes.Buffer
	writer, err := pgzip.NewWriterLevel(&buf, pgzip.BestCompression)
	if err != nil {
		return err
	}

	if _, err := writer.Write(data); err != nil {
		writer.Close()
		return err
	}

	if err := writer.Close(); err != nil {
		return err
	}

	return os.WriteFile(outputPath, buf.Bytes(), 0644)
}

func compressBrotli(outputPath string, data []byte) error {
	var buf bytes.Buffer
	writer := brotli.NewWriterLevel(&buf, brotli.BestCompression)

	if _, err := io.Copy(writer, bytes.NewReader(data)); err != nil {
		writer.Close()
		return err
	}

	if err := writer.Close(); err != nil {
		return err
	}

	return os.WriteFile(outputPath, buf.Bytes(), 0644)
}

// RemoveCompressed removes .gz and .br for a given file path in serve dir
func (c *Compressor) RemoveCompressed(servePath string) {
	os.Remove(servePath + ".gz")
	os.Remove(servePath + ".br")
	os.Remove(servePath)
}
