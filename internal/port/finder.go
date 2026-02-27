package port

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
)

// Find returns the first available TCP port starting from startFrom.
// It checks both OS-level availability and the routes file to avoid
// collisions with ports already claimed by other porter processes.
// If startFrom is 0, it defaults to 3000.
func Find(startFrom int, routesFile string) (int, error) {
	if startFrom == 0 {
		startFrom = 3000
	}
	if startFrom < 1 || startFrom > 65535 {
		return 0, fmt.Errorf("invalid start port %d: must be between 1 and 65535", startFrom)
	}

	claimed := loadClaimedPorts(routesFile)

	endPort := startFrom + 1000
	endPort = min(endPort, 65535)

	for port := startFrom; port < endPort; port++ {
		if claimed[port] {
			continue
		}

		ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", port))
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port in range %d-%d", startFrom, endPort-1)
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
