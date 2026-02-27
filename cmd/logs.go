package cmd

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/logscore/roxy/internal/platform"
	"github.com/logscore/roxy/pkg/config"
)

// Logs tails the log file for a detached process identified by ID or domain.
func Logs(target string) error {
	p := platform.Detect()
	paths := platform.GetPaths(p)
	store := config.NewStore(paths.RoutesFile)

	route, err := store.ResolveRoute(target)
	if err != nil {
		return err
	}

	if route.LogFile == "" {
		return fmt.Errorf("no log file for %s (is it running with --detach?)", route.Domain)
	}

	f, err := os.Open(route.LogFile)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer f.Close()

	// Print existing content
	if _, err := io.Copy(os.Stdout, f); err != nil {
		return err
	}

	// Tail: poll for new data
	for {
		n, err := io.Copy(os.Stdout, f)
		if err != nil {
			return err
		}
		if n == 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
}
