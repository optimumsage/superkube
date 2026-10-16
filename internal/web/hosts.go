package web

import (
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// loopbackHosts are always acceptable Host values.
var loopbackHosts = []string{"127.0.0.1", "localhost", "[::1]", "::1"}

// isWildcard reports whether bind listens on every interface.
func isWildcard(bind string) bool {
	switch bind {
	case "0.0.0.0", "::", "[::]":
		return true
	}
	return false
}

// computeAllowedHosts builds the Host-header allowlist (DNS-rebind defense).
//
//   - loopback bind: loopback names only.
//   - wildcard bind (0.0.0.0 / ::): loopback plus every usable interface
//     address and the machine hostname. A literal "0.0.0.0" Host would never
//     be sent by a real client, so adding the bind string (the old behavior)
//     made every remote request 403.
//   - specific address: loopback plus that address.
//
// The token is still required on every non-loopback bind; this list only stops
// a browser being steered to the server under an attacker-controlled name.
func computeAllowedHosts(bind string, addrs []net.Addr, hostname string) []string {
	hosts := append([]string(nil), loopbackHosts...)
	switch {
	case isLoopback(bind):
	case isWildcard(bind):
		for _, ip := range interfaceIPs(addrs) {
			hosts = append(hosts, ip.String())
		}
		if hostname != "" {
			hosts = append(hosts, hostname)
		}
	default:
		hosts = append(hosts, strings.Trim(bind, "[]"))
	}
	return hosts
}

// interfaceIPs extracts routable unicast IPs from interface addresses,
// skipping loopback and link-local (browsers can't use zoned addresses).
func interfaceIPs(addrs []net.Addr) []net.IP {
	var out []net.IP
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			continue
		}
		out = append(out, ip)
	}
	return out
}

// allowedHosts reports the set of acceptable Host header values. We use this
// for DNS-rebind protection: even though the listener is bound to 127.0.0.1
// by default, a malicious page can resolve a name like sk.attacker.com to
// 127.0.0.1 and pivot through the browser. Restricting Host stops that.
func (s *Server) allowedHosts() []string {
	s.hostsMu.RLock()
	defer s.hostsMu.RUnlock()
	return append([]string(nil), s.hosts...)
}

// hostAllowed reports whether the (already normalized) host is allowlisted.
func (s *Server) hostAllowed(host string) bool {
	for _, ok := range s.allowedHosts() {
		if host == strings.ToLower(strings.Trim(ok, "[]")) {
			return true
		}
	}
	return false
}

// hostsRefreshInterval rate-limits interface re-scans triggered by Host misses
// so an attacker spraying bogus Host headers cannot make us scan constantly.
const hostsRefreshInterval = 10 * time.Second

// refreshHosts re-reads interface addresses for wildcard binds (DHCP renewal,
// VPN or Wi-Fi coming up after startup). It reports whether a rescan ran.
func (s *Server) refreshHosts() bool {
	if !isWildcard(s.cfg.Bind) {
		return false
	}
	s.hostsMu.Lock()
	if time.Since(s.hostsRefreshed) < hostsRefreshInterval {
		s.hostsMu.Unlock()
		return false
	}
	s.hostsRefreshed = time.Now()
	s.hostsMu.Unlock()

	addrFn := s.ifaceAddrs
	if addrFn == nil {
		addrFn = net.InterfaceAddrs
	}
	addrs, err := addrFn()
	if err != nil {
		return false
	}
	hostname, _ := os.Hostname()
	hosts := computeAllowedHosts(s.cfg.Bind, addrs, hostname)
	s.hostsMu.Lock()
	s.hosts = hosts
	s.hostsMu.Unlock()
	return true
}

// publicHost is the host to print in the startup URL. Wildcard binds print a
// real interface address (preferring IPv4) instead of 0.0.0.0.
func (s *Server) publicHost() string {
	bind := strings.Trim(s.cfg.Bind, "[]")
	switch {
	case isLoopback(s.cfg.Bind):
		return "127.0.0.1"
	case isWildcard(s.cfg.Bind):
		addrs, _ := net.InterfaceAddrs()
		var v6 string
		for _, ip := range interfaceIPs(addrs) {
			if ip.To4() != nil {
				return ip.String()
			}
			if v6 == "" {
				v6 = ip.String()
			}
		}
		if v6 != "" {
			return v6
		}
		if h, err := os.Hostname(); err == nil && h != "" {
			return h
		}
		return "127.0.0.1"
	}
	return bind
}

// originPatterns turns the host allowlist into coder/websocket OriginPatterns
// (path.Match globs matched against the Origin's host[:port]). IPv6 literals
// contain '[' and ':' so the brackets must be escaped.
func (s *Server) originPatterns() []string {
	port := ""
	if s.addr != "" {
		port = s.addr[strings.LastIndex(s.addr, ":")+1:]
	}
	var out []string
	for _, h := range s.allowedHosts() {
		bare := strings.Trim(h, "[]")
		lit := bare
		if strings.Contains(bare, ":") {
			lit = `\[` + bare + `\]`
		}
		out = append(out, lit)
		if port != "" {
			out = append(out, lit+":"+port)
		}
	}
	return out
}

// startupOut receives the "listening at" line. A var so tests can capture it.
var startupOut io.Writer = os.Stderr

// stderrIsTTY reports whether stderr is a terminal. A var so tests can force
// either answer.
var stderrIsTTY = func() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
