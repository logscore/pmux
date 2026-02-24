package port

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
)

// Find returns the first available TCP port starting from startFrom.
// It checks both OS-level availability and the routes file to avoid
// collisions with ports already claimed by other pmux processes.
// If startFrom is 0, it defaults to 3000.
func Find(startFrom int, routesFile string) (int, error) {
	if startFrom == 0 {
		startFrom = 3000
	}

	claimed := loadClaimedPorts(routesFile)

	for port := startFrom; port < startFrom+1000; port++ {
		if claimed[port] {
			continue
		}

		ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", port))
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, errors.New("no available port in range " + fmt.Sprintf("%d-%d", startFrom, startFrom+999))
}

// loadClaimedPorts reads the routes file and returns a set of ports in use.
func loadClaimedPorts(routesFile string) map[int]bool {
	claimed := make(map[int]bool)

	data, err := os.ReadFile(routesFile)
	if err != nil {
		return claimed
	}

	var routes []struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(data, &routes); err != nil {
		return claimed
	}

	for _, r := range routes {
		claimed[r.Port] = true
	}
	return claimed
}
