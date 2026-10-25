package secretsproxy

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// resolveTTL is how long one answer is used. Long enough that a page of API calls is one lookup,
// short enough that a host moving to a new address is followed the same minute.
const resolveTTL = 5 * time.Minute

// pinner resolves a host and dials the address it was given.
//
// One lookup for one request is the whole point. A proxy that resolved a name to check it and then
// let the connection resolve it again has a window: an answer that changes between the two sends the
// value somewhere the check never saw. Here the address that was checked is the address dialled.
type pinner struct {
	lookup func(ctx context.Context, host string) ([]net.IP, error)
	dial   func(ctx context.Context, network, addr string) (net.Conn, error)

	mu   sync.Mutex
	seen map[string]pinnedAnswer
}

type pinnedAnswer struct {
	ips    []net.IP
	looked time.Time
}

func newPinner(cfg Config) *pinner {
	lookup := cfg.Lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	dial := cfg.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	return &pinner{lookup: lookup, dial: dial, seen: map[string]pinnedAnswer{}}
}

// dialPinned resolves a host if the answer it has is stale, refuses an address the value must not
// travel to, and connects to the address it settled on.
func (p *pinner) dialPinned(ctx context.Context, network, addr, host string) (net.Conn, error) {
	ips, err := p.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		conn, err := p.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, fmt.Errorf("%s resolved to %v and none of them answered: %w", host, ips, last)
}

// resolve is the answer for one host, refreshed when it is older than the TTL. A name that answers
// with an address inside the machine is refused: a secret's host is a name on the internet, and an
// answer pointing at loopback, a private range or a link-local address is how a granted name becomes
// the machine's own services.
func (p *pinner) resolve(ctx context.Context, host string) ([]net.IP, error) {
	p.mu.Lock()
	answered, ok := p.seen[host]
	p.mu.Unlock()
	if ok && time.Since(answered.looked) < resolveTTL {
		return answered.ips, nil
	}
	ips, err := p.lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s resolved to no address", host)
	}
	for _, ip := range ips {
		if insideTheMachine(ip) {
			return nil, fmt.Errorf("%s resolved to %s, which is inside this machine: a secret is not sent there", host, ip)
		}
	}
	p.mu.Lock()
	p.seen[host] = pinnedAnswer{ips: ips, looked: time.Now()}
	p.mu.Unlock()
	return ips, nil
}

// insideTheMachine is every address a secret must not be substituted for, whatever the name says:
// loopback, the private ranges, link-local (a cloud metadata endpoint lives at one), multicast and
// the unspecified address.
func insideTheMachine(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}
