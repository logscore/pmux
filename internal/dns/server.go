package dns

import (
	"log"
	"net"
	"os/exec"
	"runtime"
	"strings"

	"github.com/miekg/dns"
)

// Server is a DNS server that resolves *.test to 127.0.0.1
// and forwards everything else to the system's upstream resolver.
type Server struct {
	udp      *dns.Server
	tcp      *dns.Server
	upstream string
}

// Start listens on 127.0.0.1:53 for DNS queries.
func Start() (*Server, error) {
	upstream := findUpstream()

	s := &Server{upstream: upstream}

	mux := dns.NewServeMux()
	mux.HandleFunc("test.", s.handleTest)
	mux.HandleFunc(".", s.handleForward)

	s.udp = &dns.Server{Addr: "127.0.0.1:53", Net: "udp", Handler: mux}
	s.tcp = &dns.Server{Addr: "127.0.0.1:53", Net: "tcp", Handler: mux}

	errCh := make(chan error, 2)
	go func() { errCh <- s.udp.ListenAndServe() }()
	go func() { errCh <- s.tcp.ListenAndServe() }()

	// Check for immediate startup errors
	// (give servers a moment to fail if port is busy)
	select {
	case err := <-errCh:
		return nil, err
	default:
	}

	log.Printf("dns listening on 127.0.0.1:53 (upstream: %s)", upstream)
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

	for _, line := range strings.Split(string(out), "\n") {
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
