package cmd

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/logscore/porter/internal/platform"
	"github.com/logscore/porter/pkg/config"
)

// Logs tails the log file for a detached process identified by domain.
func Logs(domain string) error {
	p := platform.Detect()
	paths := platform.GetPaths(p)
	store := config.NewStore(paths.RoutesFile)

	routes, err := store.LoadRoutes()
	if err != nil {
		return fmt.Errorf("failed to load routes: %w", err)
	}

	var logFile string
	for _, r := range routes {
		if r.Domain == domain {
			logFile = r.LogFile
			break
		}
	}

	if logFile == "" {
		return fmt.Errorf("no log file found for %q (is it running with --detach?)", domain)
	}

	f, err := os.Open(logFile)
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
