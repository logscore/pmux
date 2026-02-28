package port

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
)

const (
	// minPort is the lower bound of the random port range (first unprivileged port).
	minPort = 1024
	// maxPort is the upper bound (inclusive) of the random port range.
	maxPort = 65535
	// randomAttempts is how many random picks to try before falling back to a sequential scan.
	randomAttempts = 100
)

// wellKnownPorts contains ports commonly used by databases, dev servers, and
// infrastructure tools. These are excluded from random selection to avoid
// colliding with services the user is likely already running.
var wellKnownPorts = map[int]bool{
	// HTTP/HTTPS alternatives
	1080: true, // SOCKS proxy
	4443: true, // alt HTTPS
	8000: true, // Django / Python http.server
	8008: true, // alt HTTP
	8080: true, // alt HTTP (Tomcat, etc.)
	8081: true, // alt HTTP
	8082: true, // alt HTTP
	8085: true, // alt HTTP
	8088: true, // alt HTTP
	8443: true, // alt HTTPS
	8888: true, // Jupyter Notebook

	// Common dev server ports
	3000: true, // Rails / Node / Create React App
	3001: true, // Next.js (secondary)
	4200: true, // Angular CLI
	4321: true, // Astro
	5000: true, // Flask / ASP.NET
	5173: true, // Vite
	5174: true, // Vite (secondary)
	5500: true, // Live Server (VS Code)
	5555: true, // Prisma Studio
	8899: true, // Parcel

	// Databases
	1433:  true, // MS SQL Server
	1521:  true, // Oracle DB
	3306:  true, // MySQL
	5432:  true, // PostgreSQL
	6379:  true, // Redis
	6380:  true, // Redis (alt)
	9042:  true, // Cassandra CQL
	9200:  true, // Elasticsearch HTTP
	9300:  true, // Elasticsearch transport
	11211: true, // Memcached
	27017: true, // MongoDB
	27018: true, // MongoDB (shard)
	27019: true, // MongoDB (config)

	// Message brokers & queues
	2181:  true, // ZooKeeper
	5672:  true, // RabbitMQ AMQP
	9092:  true, // Kafka
	15672: true, // RabbitMQ management
	61616: true, // ActiveMQ OpenWire

	// Container & orchestration
	2375: true, // Docker (unencrypted)
	2376: true, // Docker (TLS)
	2379: true, // etcd client
	2380: true, // etcd peer

	// Monitoring & service discovery
	8500:  true, // Consul
	8761:  true, // Eureka
	9000:  true, // SonarQube / PHP-FPM / MinIO
	9090:  true, // Prometheus
	9093:  true, // Alertmanager
	9100:  true, // Node Exporter
	3100:  true, // Grafana Loki
	9411:  true, // Zipkin
	14268: true, // Jaeger

	// Other common services
	3389: true, // RDP
	5900: true, // VNC
	8161: true, // ActiveMQ web console
	9418: true, // Git daemon
}

// Find returns an available TCP port. If exactPort > 0, it attempts to use
// that specific port (returning an error if unavailable). If exactPort is 0,
// it selects a random available port from the range 1024-65535, excluding
// well-known application ports and ports already claimed in the routes file.
// If random selection fails after multiple attempts, it falls back to a
// sequential scan starting from 1024.
func Find(exactPort int, routesFile string) (int, error) {
	claimed := loadClaimedPorts(routesFile)

	// Reject negative ports.
	if exactPort < 0 {
		return 0, fmt.Errorf("invalid port %d: must be between 1 and 65535", exactPort)
	}

	// Exact pin mode: user explicitly requested this port.
	if exactPort > 0 {
		if exactPort > 65535 {
			return 0, fmt.Errorf("invalid port %d: must be between 1 and 65535", exactPort)
		}
		if claimed[exactPort] {
			return 0, fmt.Errorf("port %d is already claimed by another roxy service", exactPort)
		}
		if err := checkAvailable(exactPort); err != nil {
			return 0, fmt.Errorf("port %d is not available: %w", exactPort, err)
		}
		return exactPort, nil
	}

	// Random selection mode.
	portRange := maxPort - minPort + 1
	for range randomAttempts {
		p := minPort + rand.IntN(portRange)
		if wellKnownPorts[p] || claimed[p] {
			continue
		}
		if err := checkAvailable(p); err == nil {
			return p, nil
		}
	}

	// Fallback: sequential scan from minPort.
	for p := minPort; p <= maxPort; p++ {
		if wellKnownPorts[p] || claimed[p] {
			continue
		}
		if err := checkAvailable(p); err == nil {
			return p, nil
		}
	}

	return 0, fmt.Errorf("no available port found in range %d-%d", minPort, maxPort)
}

// checkAvailable tests whether a TCP port is free by briefly listening on it.
func checkAvailable(port int) error {
	ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	ln.Close()
	return nil
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
