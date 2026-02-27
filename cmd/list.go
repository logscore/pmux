package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/logscore/porter/internal/platform"
	"github.com/logscore/porter/pkg/config"
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
		fmt.Println("DOMAIN\tPORT\tTYPE\tPID\tCOMMAND")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tDOMAIN\tTYPE\tPORT\tPID\tCOMMAND")
	for _, r := range routes {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\n", r.ID, r.Domain, r.Type, r.Port, r.PID, r.Command)
	}
	return w.Flush()
}
