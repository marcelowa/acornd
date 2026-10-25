package secretsproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/elazarl/goproxy"
)

// maxBody is how much of a request body the proxy will read looking for a placeholder. It is far
// above anything a command sends to an API and far below a file upload: a body over it is refused
// for a host a secret covers, because a body that was not read is a placeholder that was not
// found.
const maxBody = 8 << 20

// readHeaderTimeout bounds a client that connects and says nothing. It covers the CONNECT and the
// first request only: a hijacked tunnel is no longer the server's to time out.
const readHeaderTimeout = 30 * time.Second

// Config is what one workspace's proxy is given.
type Config struct {
	// Grants are the secrets this workspace may use.
	Grants []Grant
	// Token is the credential a client must present in Proxy-Authorization. Empty mints one, so
	// a caller cannot end up serving an open proxy on a port anything in the sandbox can reach.
	Token string
	// Addr is where to listen. Empty binds loopback on a port the kernel picks; a caller that
	// needs the same address across restarts passes one, because the sandbox's argv is where its
	// forward was baked and a new port would need a new sandbox.
	Addr string
	// Logf reports what was refused. It is called with a name and a host, never a value.
	Logf func(format string, args ...any)
	// Lookup resolves a host to its addresses. Nil uses the machine's resolver. It is a seam because
	// the gate it feeds is the thing worth testing: a name that answers differently to a check and a
	// dial is the attack this proxy is supposed to stop.
	Lookup func(ctx context.Context, host string) ([]net.IP, error)
	// Dial opens the connection the proxy forwards on. Nil dials normally.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Proxy is one workspace's secrets proxy: a loopback listener a command reaches through
// HTTPS_PROXY, with the workspace's CA behind it.
type Proxy struct {
	mu     sync.RWMutex
	grants []Grant
	out    *pinner
	token  []byte
	logf   func(format string, args ...any)
	ca     *CA
	proxy  *goproxy.ProxyHttpServer
	server *http.Server
	listen net.Listener
}

// New loads the install's CA and binds one workspace's listener on 127.0.0.1.
//
// Loopback, and bound here rather than by the caller: the address is the security property. pasta
// forwards exactly this port into the sandbox (see ADR 0031), so the proxy is not reachable from
// the machine's network and the sandbox reaches nothing else on the machine's loopback.
func New(caDir string, cfg Config) (*Proxy, error) {
	ca, err := LoadOrCreateCA(caDir)
	if err != nil {
		return nil, err
	}
	token := cfg.Token
	if token == "" {
		minted := make([]byte, 32)
		if _, err := rand.Read(minted); err != nil {
			return nil, fmt.Errorf("secrets proxy: minting a session token: %w", err)
		}
		token = hex.EncodeToString(minted)
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	addr := cfg.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	p := &Proxy{grants: cfg.Grants, token: []byte(token), logf: logf, ca: ca, out: newPinner(cfg)}
	p.proxy = p.build()
	listen, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("secrets proxy: binding %s: %w", addr, err)
	}
	p.listen = listen
	p.server = &http.Server{
		Handler:           p.proxy,
		ReadHeaderTimeout: readHeaderTimeout,
		// The server's own noise — a TLS handshake a pinned client refused is the normal case — goes
		// where the rest of this package's lines go, rather than to stderr behind the gateway's
		// logger.
		ErrorLog: log.New(logWriter{p.logf}, "", 0),
	}
	return p, nil
}

// build wires the handler chain. Each callback is a decision the policy already made elsewhere;
// this is where it reaches the library.
func (p *Proxy) build() *goproxy.ProxyHttpServer {
	server := goproxy.NewProxyHttpServer()
	server.Verbose = false
	server.Logger = proxyLogger{p.logf}
	// The default transport skips TLS verification and reads the environment's proxy settings.
	// Both are wrong here: a secret going to a host whose certificate was not checked is a secret
	// given to whoever answered, and the machine's own proxy is not this workspace's egress.
	server.Tr = &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		// Every forwarded request is dialled through the pin, so a name a secret may reach is
		// resolved once and the connection goes to the address that was decided on.
		DialContext: p.dial,
	}
	// Per instance: a cache shared between workspaces would hold certificates one workspace's CA
	// signed and the next could present.
	server.CertStore = newCertCache()
	// Not a proxy request: a client that asked for a relative path did not configure a proxy at
	// all, and answering it would look like an origin server that happens to be on a proxy port.
	server.NonproxyHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "this port is acornfall's secrets proxy, not a web server: send an absolute URL or a CONNECT", http.StatusBadRequest)
	})
	server.OnRequest().HandleConnectFunc(p.connect)
	server.OnRequest().DoFunc(p.request)
	return server
}

// dial opens the connection for a request the proxy forwards. A host a secret names is resolved here
// and its address pinned: the check and the connection are one decision, and an answer that would
// send the value to the machine's own services is refused rather than dialled.
func (p *Proxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return p.out.dial(ctx, network, addr)
	}
	if !allowsAnyHost(p.currentGrants(), host) {
		// Nothing of ours goes to this host, so there is no value to protect from a second answer.
		return p.out.dial(ctx, network, addr)
	}
	return p.out.dialPinned(ctx, network, addr, host)
}

// SetGrants replaces the secrets this proxy will substitute. It is the rotation path: the next
// request sees the new set, and a running command is not interrupted for it.
func (p *Proxy) SetGrants(grants []Grant) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.grants = grants
}

// currentGrants is the set a request is decided against. A copy of the slice header is enough: the
// grants themselves are never written after they are handed over.
func (p *Proxy) currentGrants() []Grant {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.grants
}

// Addr is the address a command puts in HTTPS_PROXY, without the credential.
func (p *Proxy) Addr() string { return p.listen.Addr().String() }

// Token is the credential the sandbox presents. It is minted per proxy, so a token in one
// workspace's environment is useless in another's.
func (p *Proxy) Token() string { return string(p.token) }

// Serve answers until the context ends. The lifetime belongs to the caller (G1): a proxy whose
// listener outlived its workspace would answer the next sandbox on a port it no longer owns.
func (p *Proxy) Serve(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() { errc <- p.server.Serve(p.listen) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("secrets proxy: serving: %w", err)
	case <-ctx.Done():
		// Close rather than Shutdown: Shutdown waits for hijacked CONNECT tunnels to finish, and
		// a tunnel is exactly what does not finish.
		if err := p.server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("secrets proxy: closing: %w", err)
		}
		<-errc
		return nil
	}
}

// connect decides one CONNECT: authenticated or not, intercepted or tunnelled.
func (p *Proxy) connect(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
	if !p.authorised(ctx.Req.Header) {
		ctx.Resp = refusal(ctx.Req, http.StatusProxyAuthRequired, "this proxy needs its session token in Proxy-Authorization")
		return &goproxy.ConnectAction{Action: goproxy.ConnectReject}, host
	}
	if !allowsAnyHost(p.currentGrants(), hostOnly(host)) {
		// Nothing for this host: a tunnel keeps the client's own TLS, so a certificate it pins is
		// the one it sees. Interception is the exception for a secret's destination, not the rule.
		return &goproxy.ConnectAction{Action: goproxy.ConnectAccept}, host
	}
	return &goproxy.ConnectAction{Action: goproxy.ConnectMitm, TLSConfig: goproxy.TLSConfigFromCA(&p.ca.certificate)}, host
}

// request decides one request that is already inside the proxy: plain HTTP, or the inside of an
// intercepted TLS connection.
func (p *Proxy) request(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	if req.Method == http.MethodConnect {
		// CONNECT never reaches this handler; saying so beats a rewrite path that cannot run.
		return req, nil
	}
	// The inner requests of an intercepted tunnel were authenticated by its CONNECT. A plain HTTP
	// proxy request was not, and carries the token itself.
	if req.URL.Scheme == "http" && !p.authorised(req.Header) {
		return req, refusal(req, http.StatusProxyAuthRequired, "this proxy needs its session token in Proxy-Authorization")
	}
	host := req.URL.Hostname()
	body, oversized, err := bufferBody(req)
	if err != nil {
		return req, refusal(req, http.StatusBadRequest, "the proxy could not read the request body")
	}
	if oversized && allowsAnyHost(p.currentGrants(), host) {
		return req, refusal(req, http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is larger than %d bytes, so a placeholder in it cannot be found", maxBody))
	}
	verdict := decide(p.currentGrants(), host, req.Header, req.URL.RawQuery, body)
	switch verdict.action {
	case refuse:
		p.logf("secrets: refused %s %s: %s", req.Method, host, verdict.reason)
		return req, refusal(req, http.StatusForbidden, verdict.reason)
	case forward:
		// Nothing to do: the request goes on as it arrived.
	case substitute:
		return req, p.substitute(req, &body, verdict.grants)
	}
	return req, nil
}

// substitute resolves the host, rewrites the request and answers when the name may not be used for a
// secret. A nil answer means the request is ready to forward.
//
// The resolution happens here, once, so the check and the connection are the same answer: the dial
// reuses this one, and a name that answers differently to two lookups has nothing to switch between.
// It is also where a refusal can be read by the client rather than surfacing as a broken connection.
func (p *Proxy) substitute(req *http.Request, body *[]byte, grants []Grant) *http.Response {
	host := req.URL.Hostname()
	if _, err := p.out.resolve(req.Context(), host); err != nil {
		p.logf("secrets: refused %s %s: %v", req.Method, host, err)
		return refusal(req, http.StatusForbidden, err.Error())
	}
	rewrite(req.Header, &req.URL.RawQuery, body, grants)
	replaceBody(req, *body)
	// Ours, not the destination's: forwarding it would hand a workspace's proxy token to an upstream
	// server.
	req.Header.Del("Proxy-Authorization")
	return nil
}

// authorised answers whether the request carries this proxy's token. Constant time: the listener
// is on loopback, which every process on the machine can dial, so the token is the boundary and a
// comparison that leaks it a byte at a time is not one.
func (p *Proxy) authorised(header http.Header) bool {
	raw := header.Get("Proxy-Authorization")
	if raw == "" {
		return false
	}
	scheme, value, found := strings.Cut(raw, " ")
	if !found {
		return false
	}
	value = strings.TrimSpace(value)
	switch strings.ToLower(scheme) {
	case "bearer":
		return subtle.ConstantTimeCompare([]byte(value), p.token) == 1
	case "basic":
		// A proxy URL is the portable place for it — http://user:token@127.0.0.1:port — and every
		// client that honours HTTPS_PROXY knows how to send that URL's password.
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return false
		}
		_, password, found := strings.Cut(string(decoded), ":")
		if !found {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(password), p.token) == 1
	}
	return false
}

// bufferBody reads at most maxBody and says whether the body was longer. The bytes are put back
// for a request that is forwarded untouched, so a body the proxy merely looked at is not lost.
func bufferBody(req *http.Request) ([]byte, bool, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, false, nil
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBody+1))
	if err != nil {
		return nil, false, err
	}
	oversized := len(body) > maxBody
	if oversized {
		// The rest was never read, so it is still there to be forwarded: putting back what was
		// read keeps a body the proxy only looked at byte-for-byte what the client sent.
		req.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(body), req.Body), Closer: req.Body}
		return body[:maxBody], true, nil
	}
	req.Body = readCloser{Reader: bytes.NewReader(body), Closer: http.NoBody}
	return body, false, nil
}

// replaceBody puts the rewritten bytes in the request, with the length that matches them.
func replaceBody(req *http.Request, body []byte) {
	req.Body = readCloser{Reader: bytes.NewReader(body), Closer: http.NoBody}
	req.ContentLength = int64(len(body))
	req.TransferEncoding = nil
	req.Header.Del("Content-Length")
	if len(body) == 0 {
		req.Body = http.NoBody
		req.ContentLength = 0
	}
}

// refusal builds the answer a refused request gets. The reason names the secret and the host; it
// never names the value, and the response carries no part of the request that could.
func refusal(req *http.Request, status int, reason string) *http.Response {
	resp := goproxy.NewResponse(req, "text/plain; charset=utf-8", status, reason+"\n")
	if status == http.StatusProxyAuthRequired {
		resp.Header.Set("Proxy-Authenticate", `Basic realm="acornfall secrets proxy"`)
	}
	return resp
}

// readCloser is a body the proxy rebuilt: the bytes read, and whatever was left of the original.
type readCloser struct {
	io.Reader
	io.Closer
}

// hostOnly drops the port from a CONNECT target.
func hostOnly(host string) string {
	if name, _, err := net.SplitHostPort(host); err == nil {
		return name
	}
	return host
}

// logWriter turns the standard library's own reports into this package's log lines.
type logWriter struct {
	logf func(format string, args ...any)
}

func (w logWriter) Write(p []byte) (int, error) {
	w.logf("secrets: %s", strings.TrimSpace(string(p)))
	return len(p), nil
}

// proxyLogger sends the library's own warnings into the proxy's log, so a certificate that could
// not be signed is one line where the rest of the gateway's logs are rather than on stderr.
type proxyLogger struct {
	logf func(format string, args ...any)
}

func (l proxyLogger) Printf(format string, args ...any) {
	l.logf("secrets: %s", strings.TrimSuffix(fmt.Sprintf(format, args...), "\n"))
}

// certCache keeps the certificate each host was given, per proxy. Signing is the expensive half of
// interception and a workspace talks to the same handful of hosts all day.
type certCache struct {
	mu    sync.RWMutex
	certs map[string]*tls.Certificate
}

func newCertCache() *certCache { return &certCache{certs: map[string]*tls.Certificate{}} }

func (c *certCache) Fetch(hostname string, gen func() (*tls.Certificate, error)) (*tls.Certificate, error) {
	c.mu.RLock()
	cached, ok := c.certs[hostname]
	c.mu.RUnlock()
	if ok {
		return cached, nil
	}
	// Signing happens outside the lock: it is not I/O but it is slow, and holding a lock across it
	// would serialise every first connection through one workspace.
	cert, err := gen()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.certs[hostname] = cert
	c.mu.Unlock()
	return cert, nil
}
