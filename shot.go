package main

// Screenshot path and command. Defaults to .tinybk/shots under the current working directory
// so a restricted harness does not need to write next to the binary.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/chromedp"
)

type shotResult struct {
	based
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// resolveShotDir turns --shot-dir / shot into an absolute directory and creates it.
// An empty value means .tinybk/shots under the current working directory.
func resolveShotDir(dir string) (string, error) {
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("read current working directory: %w", err)
		}
		dir = filepath.Join(cwd, ".tinybk", "shots")
	} else if !filepath.IsAbs(dir) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("read current working directory: %w", err)
		}
		dir = filepath.Join(cwd, dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve screenshot directory %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("create screenshot directory %s: %w", abs, err)
	}
	return abs, nil
}

func captureShot(ctx context.Context, dir string) (string, int, error) {
	buf, err := chromedp.Run(ctx, chromedp.CaptureScreenshot())
	if err != nil {
		return "", 0, fmt.Errorf("screenshot: %w", err)
	}
	targetDir, err := resolveShotDir(dir)
	if err != nil {
		return "", 0, err
	}
	path := filepath.Join(targetDir, time.Now().Format("20060102-150405.000")+".png")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return "", 0, fmt.Errorf("write screenshot %s: %w", path, err)
	}
	return path, len(buf), nil
}

func cmdShot(s *Session, dir string) (any, error) {
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	path, n, err := captureShot(ctx, dir)
	if err != nil {
		return nil, err
	}
	return &shotResult{Path: path, Bytes: n}, nil
}
