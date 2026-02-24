package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/logscore/pmux/internal/platform"
	"github.com/logscore/pmux/pkg/config"
)

func List() error {
	p := platform.Detect()
	paths := platform.GetPaths(p)
	store := config.NewStore(paths.RoutesFile)

	routes, err := store.LoadRoutes()
	if err != nil {
		return fmt.Errorf("failed to load routes: %w", err)
	}

	if len(routes) == 0 {
		fmt.Println("No active tunnels.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DOMAIN\tPORT\tTYPE\tPID\tCOMMAND")
	for _, r := range routes {
		typ := r.Type
		if typ == "" {
			typ = "http"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%d\t%s\n", r.Domain, r.Port, typ, r.PID, r.Command)
	}
	return w.Flush()
}
