package dns

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// Server is a DNS server that resolves *.test to 127.0.0.1
// and forwards everything else to the system's upstream resolver.
type Server struct {
	udp      *dns.Server
	tcp      *dns.Server
	upstream string
}

// Start listens on 127.0.0.1 for DNS queries on the given port (default 1299).
func Start(port int) (*Server, error) {
	if port == 0 {
		port = 1299
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	upstream := findUpstream()

	s := &Server{upstream: upstream}

	mux := dns.NewServeMux()
	mux.HandleFunc("test.", s.handleTest)
	mux.HandleFunc(".", s.handleForward)

	s.udp = &dns.Server{Addr: addr, Net: "udp", Handler: mux}
	s.tcp = &dns.Server{Addr: addr, Net: "tcp", Handler: mux}

	errCh := make(chan error, 2)
	go func() { errCh <- s.udp.ListenAndServe() }()
	go func() { errCh <- s.tcp.ListenAndServe() }()

	// Give servers a moment to fail if port is busy, then check for errors.
	time.Sleep(50 * time.Millisecond)

	// Drain all immediately available errors
	var errs []error
	for {
		select {
		case err := <-errCh:
			errs = append(errs, err)
		default:
			goto done
		}
	}
done:
	if len(errs) > 0 {
		// Stop whichever server may still be running
		s.Stop()
		return nil, fmt.Errorf("dns server startup failed: %v", errs)
	}

	log.Printf("dns listening on %s (upstream: %s)", addr, upstream)
	return s, nil
}

// Stop shuts down the DNS server.
func (s *Server) Stop() {
	if s.udp != nil {
		s.udp.Shutdown()
	}
	if s.tcp != nil {
		s.tcp.Shutdown()
	}
}

// handleTest responds to *.test queries with 127.0.0.1.
func (s *Server) handleTest(w dns.ResponseWriter, r *dns.Msg) {
	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true

	for _, q := range r.Question {
		if q.Qtype == dns.TypeA || q.Qtype == dns.TypeANY {
			msg.Answer = append(msg.Answer, &dns.A{
				Hdr: dns.RR_Header{
					Name:   q.Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    60,
				},
				A: net.ParseIP("127.0.0.1"),
			})
		}
	}

	w.WriteMsg(msg)
}

// handleForward proxies non-.test queries to the upstream resolver.
func (s *Server) handleForward(w dns.ResponseWriter, r *dns.Msg) {
	if s.upstream == "" {
		dns.HandleFailed(w, r)
		return
	}

	c := new(dns.Client)
	resp, _, err := c.Exchange(r, s.upstream)
	if err != nil {
		dns.HandleFailed(w, r)
		return
	}

	w.WriteMsg(resp)
}

// findUpstream discovers the system's DNS resolver.
func findUpstream() string {
	switch runtime.GOOS {
	case "darwin":
		return findUpstreamDarwin()
	default:
		return findUpstreamLinux()
	}
}

func findUpstreamDarwin() string {
	// scutil --dns lists all resolvers; find the default one
	out, err := exec.Command("scutil", "--dns").Output()
	if err != nil {
		return "8.8.8.8:53"
	}

	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nameserver[0]") || strings.HasPrefix(line, "nameserver :") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				ip := parts[len(parts)-1]
				if ip != "127.0.0.1" && net.ParseIP(ip) != nil {
					return ip + ":53"
				}
			}
		}
	}

	return "8.8.8.8:53"
}

func findUpstreamLinux() string {
	// Read /etc/resolv.conf for the first non-localhost nameserver
	c, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil {
		return "8.8.8.8:53"
	}

	for _, s := range c.Servers {
		if s != "127.0.0.1" && s != "::1" {
			return s + ":" + c.Port
		}
	}

	return "8.8.8.8:53"
}
