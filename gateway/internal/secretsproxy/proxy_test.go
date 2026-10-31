package secretsproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeHost is the destination every TLS test asks for. A hostname rather than 127.0.0.1 because
// that is what a grant names, and because the test origin's certificate is issued for it.
const fakeHost = "example.com"

// testPublicIP is a documentation address (TEST-NET-2): routable as far as the rules are concerned,
// and never anything's real answer.
const testPublicIP = "198.51.100.10"

// thePlaceholder and theValue are one secret a test grants. The value is spelled so that a test
// can grep a response, a log, and a request for it and know the answer is not coincidence.
const (
	thePlaceholder = "$MSB_TEST_TOKEN"
	theValue       = "real-value-3f9a"
)

// origin is the upstream a test talks through the proxy to. It reports what it actually received,
// which is the only claim worth asserting: what left the proxy.
type origin struct {
	server *httptest.Server
	hits   atomic.Int32
}

func newOrigin(t *testing.T) *origin {
	t.Helper()
	o := &origin{}
	o.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "auth=%s query=%s body=%s length=%d proxy-auth=%s",
			r.Header.Get("Authorization"), r.URL.RawQuery, body, r.ContentLength,
			r.Header.Get("Proxy-Authorization"))
	}))
	t.Cleanup(o.server.Close)
	return o
}

func (o *origin) addr() string { return o.server.Listener.Addr().String() }

// cert is what a client that trusts the origin directly would verify against.
func (o *origin) cert(t *testing.T) *x509.Certificate {
	t.Helper()
	if o.server.Certificate() == nil {
		t.Fatal("the test origin has no certificate")
	}
	return o.server.Certificate()
}

// harness is one proxy and the clients a test builds from it.
type harness struct {
	proxy  *Proxy
	addr   string
	token  string
	caCert *x509.Certificate
	logs   *recorder
	origin *origin
}

// newHarness starts a proxy for one test. Traffic for any host is dialled to the origin, so the
// name in a grant and the certificate a client verifies can both be real without a DNS server.
func newHarness(t *testing.T, o *origin, grants ...Grant) *harness {
	t.Helper()
	rec := &recorder{}
	caDir := t.TempDir()
	p, err := New(caDir, Config{
		Grants: grants,
		Logf:   rec.printf,
		// The test's origin listens on the machine's loopback, which a secret must never be sent to,
		// so the answer is a public address and the dial goes to the origin anyway. Both are seams a
		// real proxy fills with the machine's resolver and a normal dial.
		Lookup: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP(testPublicIP)}, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			rec.dial(addr)
			return (&net.Dialer{}).DialContext(ctx, network, o.addr())
		},
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	if o != nil {
		p.proxy.ConnectDial = func(network, _ string) (net.Conn, error) { return net.Dial(network, o.addr()) }
		// The proxy verifies the origin like any other client — its production default is the
		// system roots — so a test origin, which is signed by itself, has to be named.
		roots := x509.NewCertPool()
		roots.AddCert(o.cert(t))
		p.proxy.Tr.TLSClientConfig.RootCAs = roots
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		// The proxy's own shutdown is the test's cleanup, not a failure to report.
		_ = p.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	block, _ := pem.Decode(caPEM(t, caDir))
	if block == nil {
		t.Fatal("the proxy's CA has no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse the proxy CA: %v", err)
	}
	return &harness{proxy: p, addr: p.Addr(), token: p.Token(), caCert: cert, logs: rec, origin: o}
}

// client is a sandboxed command: it knows only the proxy address, and the roots it trusts are the
// ones a test gives it.
func (h *harness) client(t *testing.T, token string, roots ...*x509.Certificate) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	for _, cert := range roots {
		pool.AddCert(cert)
	}
	userinfo := ""
	if token != "" {
		userinfo = url.UserPassword("acornd", token).String() + "@"
	}
	proxyURL, err := url.Parse("http://" + userinfo + h.addr)
	if err != nil {
		t.Fatalf("parse the proxy URL: %v", err)
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}
}

// do sends one request and returns its status and body, so each test reads as the assertion it
// makes.
func do(t *testing.T, client *http.Client, method, target string, header map[string]string, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	for name, value := range header {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the response: %v", err)
	}
	return resp.StatusCode, string(read)
}

// connectStatus sends a CONNECT with the given Proxy-Authorization and returns the status line.
// A raw TCP client, because Go's transport manages the proxy credential itself: the Bearer form a
// tool like curl sends has to be tested on the wire to be tested at all.
func connectStatus(t *testing.T, addr, authorization string) string {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial the proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set the deadline: %v", err)
	}
	request := "CONNECT " + fakeHost + ":443 HTTP/1.1\r\nHost: " + fakeHost + ":443\r\n"
	if authorization != "" {
		request += "Proxy-Authorization: " + authorization + "\r\n"
	}
	request += "\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("write the CONNECT: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read the status line: %v", err)
	}
	return strings.TrimSpace(line)
}

// recorder collects the proxy's own log, so a test can assert what it said — and that it did not
// say the value.
type recorder struct {
	mu      sync.Mutex
	lines   []string
	dialled []string
}

// dial records where the proxy was told to connect, which is the address it resolved.
func (r *recorder) dial(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dialled = append(r.dialled, addr)
}

func (r *recorder) dials() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.dialled...)
}

func (r *recorder) printf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *recorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// A placeholder in a header becomes the value at the upstream, and the workspace's proxy token
// does not travel with it: the substitution is the proxy's, not the command's.
func TestAPlaceholderBecomesTheValueForAnAllowedHost(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin, Grant{
		Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder,
		Hosts: []string{fakeHost}, Headers: true,
	})
	client := h.client(t, h.token, h.caCert, origin.cert(t))

	status, body := do(t, client, http.MethodGet, "https://"+fakeHost+"/v1/things",
		map[string]string{"Authorization": "Bearer " + thePlaceholder}, "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	if want := "auth=Bearer " + theValue; !strings.Contains(body, want) {
		t.Errorf("the upstream saw %q, not %q", body, want)
	}
	if !strings.Contains(body, "proxy-auth=") || !strings.HasSuffix(strings.SplitN(body, "proxy-auth=", 2)[1], "") {
		t.Fatalf("the origin's report is missing: %q", body)
	}
	if strings.Contains(body, "proxy-auth=Basic") || strings.Contains(body, "proxy-auth=Bearer") {
		t.Errorf("the workspace's proxy token reached the upstream: %q", body)
	}
}

// A secret sent to a host it does not name is refused before the request leaves. The host itself
// is intercepted — another secret covers it — which is the case that can be refused at all: a
// tunnel to a host no secret names never reaches this policy.
func TestASecretSentToAHostItDoesNotNameIsRefused(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin,
		Grant{Name: "HERE", Value: "here-value", Placeholder: "$MSB_HERE", Hosts: []string{fakeHost}, Headers: true},
		Grant{Name: "ELSEWHERE", Value: theValue, Placeholder: thePlaceholder, Hosts: []string{"api.other.test"}, Headers: true},
	)
	client := h.client(t, h.token, h.caCert, origin.cert(t))

	status, body := do(t, client, http.MethodGet, "https://"+fakeHost+"/v1/things",
		map[string]string{"Authorization": "Bearer " + thePlaceholder}, "")
	if status != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", status, body)
	}
	if !strings.Contains(body, "ELSEWHERE") || !strings.Contains(body, fakeHost) {
		t.Errorf("the refusal should name the secret and the host: %q", body)
	}
	if origin.hits.Load() != 0 {
		t.Errorf("the upstream was contacted %d times for a refused request", origin.hits.Load())
	}
}

// The honest limit of intercepting only a secret's destinations: a placeholder sent somewhere no
// secret covers travels as itself. It is worth a test that says so, because the property that
// holds is the one that matters — the value does not travel.
func TestAPlaceholderOnAHostWithNoSecretTravelsAsText(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin, Grant{
		Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder,
		Hosts: []string{"api.other.test"}, Headers: true,
	})
	client := h.client(t, h.token, origin.cert(t))

	status, body := do(t, client, http.MethodGet, "https://"+fakeHost+"/v1/things",
		map[string]string{"Authorization": "Bearer " + thePlaceholder}, "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	if !strings.Contains(body, "auth=Bearer "+thePlaceholder) {
		t.Errorf("the tunnel should carry the placeholder unchanged: %q", body)
	}
	if strings.Contains(body, theValue) {
		t.Fatalf("the value travelled to a host no secret names: %q", body)
	}
}

// Table of the ways a placeholder can be in the wrong place. Each is a refusal, and the reason
// says which place it was in.
func TestAPlaceholderInADisabledLocationIsRefused(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin, Grant{
		Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder,
		Hosts: []string{fakeHost}, Headers: true,
	})
	client := h.client(t, h.token, h.caCert, origin.cert(t))

	cases := []struct {
		name   string
		target string
		body   string
		reason string
	}{
		{"query", "https://" + fakeHost + "/things?api_key=" + thePlaceholder, "", "query"},
		{"body", "https://" + fakeHost + "/things", thePlaceholder, "body"},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			status, body := do(t, client, http.MethodPost, one.target, nil, one.body)
			if status != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", status, body)
			}
			if !strings.Contains(body, one.reason) {
				t.Errorf("the refusal should say %q: %q", one.reason, body)
			}
		})
	}
	if origin.hits.Load() != 0 {
		t.Errorf("the upstream was contacted %d times for a refused request", origin.hits.Load())
	}
}

// A request with no placeholder is none of the proxy's business: it is forwarded as it arrived.
func TestARequestWithoutAPlaceholderIsForwardedUntouched(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin, Grant{
		Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder,
		Hosts: []string{fakeHost}, Headers: true,
	})
	client := h.client(t, h.token, h.caCert, origin.cert(t))

	status, body := do(t, client, http.MethodGet, "https://"+fakeHost+"/v1/things",
		map[string]string{"Authorization": "Bearer something-else"}, "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	if !strings.Contains(body, "auth=Bearer something-else") {
		t.Errorf("the header should be unchanged: %q", body)
	}
}

// A host no secret covers is tunnelled, not intercepted: a client that pins that host's
// certificate keeps working, because it sees the origin's certificate and not ours.
func TestAHostWithNoGrantIsTunnelledNotIntercepted(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin, Grant{
		Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder,
		Hosts: []string{"api.other.test"}, Headers: true,
	})
	// Only the origin's certificate: interception would present a certificate this client cannot
	// verify, so a passing request is proof the tunnel was passed through.
	client := h.client(t, h.token, origin.cert(t))

	status, body := do(t, client, http.MethodGet, "https://"+fakeHost+"/v1/things", nil, "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	if !strings.Contains(body, "auth=") {
		t.Errorf("the tunnel did not reach the origin: %q", body)
	}
}

// A body is rewritten with the length that matches it: a substitution that kept the old length
// would send a truncated or padded request.
func TestTheBodyIsRewrittenWithItsLength(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin, Grant{
		Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder,
		Hosts: []string{fakeHost}, Body: true,
	})
	client := h.client(t, h.token, h.caCert, origin.cert(t))

	sent := `{"token":"` + thePlaceholder + `"}`
	want := `{"token":"` + theValue + `"}`
	status, body := do(t, client, http.MethodPost, "https://"+fakeHost+"/v1/things", nil, sent)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	if !strings.Contains(body, "body="+want) {
		t.Errorf("the upstream received %q, not the rewritten body", body)
	}
	if !strings.Contains(body, fmt.Sprintf("length=%d", len(want))) {
		t.Errorf("the length did not follow the body: %q", body)
	}
}

// A query is a location like any other, and the raw form is what travels.
func TestTheQueryIsRewritten(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin, Grant{
		Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder,
		Hosts: []string{fakeHost}, Query: true,
	})
	client := h.client(t, h.token, h.caCert, origin.cert(t))

	status, body := do(t, client, http.MethodGet, "https://"+fakeHost+"/things?api_key="+thePlaceholder, nil, "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	if !strings.Contains(body, "query=api_key="+theValue) {
		t.Errorf("the upstream saw %q", body)
	}
}

// The token is the boundary a loopback listener needs: without it, anything on the machine can ask
// for a substitution. Every form a client can send is checked, because a proxy that accepted one
// wrong form would be open to it.
func TestTheProxyRefusesAClientWithoutItsToken(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin, Grant{
		Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder,
		Hosts: []string{fakeHost}, Headers: true,
	})
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("acornd:"+h.token))

	cases := []struct {
		name   string
		auth   string
		status string
	}{
		{"no token", "", "407"},
		{"wrong token", "Bearer not-the-token", "407"},
		{"wrong scheme", "Token " + h.token, "407"},
		{"bearer", "Bearer " + h.token, "200"},
		{"basic", basic, "200"},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()
			if got := connectStatus(t, h.addr, one.auth); !strings.Contains(got, one.status) {
				t.Errorf("CONNECT answered %q, want %s", got, one.status)
			}
		})
	}
}

// A refusal is one line in the log and one sentence to the client, and neither carries the value.
func TestARefusalNamesTheSecretAndNeverTheValue(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin,
		Grant{Name: "HERE", Value: "here-value", Placeholder: "$MSB_HERE", Hosts: []string{fakeHost}, Headers: true},
		Grant{Name: "ELSEWHERE", Value: theValue, Placeholder: thePlaceholder, Hosts: []string{"api.other.test"}, Headers: true},
	)
	client := h.client(t, h.token, h.caCert, origin.cert(t))

	_, body := do(t, client, http.MethodGet, "https://"+fakeHost+"/v1/things",
		map[string]string{"Authorization": "Bearer " + thePlaceholder}, "")
	if strings.Contains(body, theValue) {
		t.Fatalf("the refusal carried the value: %q", body)
	}
	logged := h.logs.all()
	if !strings.Contains(logged, "ELSEWHERE") || !strings.Contains(logged, fakeHost) {
		t.Errorf("the log should name the secret and the host: %q", logged)
	}
	if strings.Contains(logged, theValue) {
		t.Fatalf("the log carried the value: %q", logged)
	}
}

// The CA is the install's, made once: a second proxy on the same folder trusts the same
// certificate, because a second CA would invalidate every workspace that trusted the first.
func TestTheCAIsMadeOnceAndKept(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := LoadOrCreateCA(dir); err != nil {
		t.Fatalf("make the CA: %v", err)
	}
	made := readCAFile(t, dir)
	if _, err := LoadOrCreateCA(dir); err != nil {
		t.Fatalf("read the CA: %v", err)
	}
	if !bytes.Equal(made, readCAFile(t, dir)) {
		t.Fatal("a second read produced a different CA")
	}
	info, err := os.Stat(filepath.Join(dir, caFile))
	if err != nil {
		t.Fatalf("stat the CA file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the CA file is %04o, and its key must be 0600", mode)
	}
}

// Two gateways starting at once race for the CA. The loser reads the winner's file rather than
// replacing it, so every workspace ends up trusting one certificate.
func TestTwoCallersStartingAtOnceGetOneCA(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const callers = 8
	pems := make([]string, callers)
	var wg sync.WaitGroup
	for index := range callers {
		wg.Go(func() {
			ca, err := LoadOrCreateCA(dir)
			if err != nil {
				t.Errorf("caller %d: %v", index, err)
				return
			}
			pems[index] = string(readCAFile(t, dir))
			_ = ca
		})
	}
	wg.Wait()
	for index, got := range pems {
		if got != pems[0] {
			t.Fatalf("caller %d got a different CA than caller 0", index)
		}
	}
}

// A CA file anything else can read is refused rather than used: the key signs certificates for
// every workspace, and a readable key is one anybody on the machine can impersonate.
func TestACAFileOtherUsersCanReadIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := LoadOrCreateCA(dir); err != nil {
		t.Fatalf("make the CA: %v", err)
	}
	path := filepath.Join(dir, caFile)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := LoadOrCreateCA(dir); err == nil {
		t.Fatal("a CA readable by other users should be refused")
	} else if !strings.Contains(err.Error(), "0600") {
		t.Errorf("the refusal should say what to fix: %v", err)
	}
}

// A file that is not a CA is a refusal, not a reason to make a new one: what is on disk is what
// the workspaces will trust, and replacing it silently would break every one of them.
func TestAnUnreadableCAIsRefusedRatherThanReplaced(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, caFile)
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadOrCreateCA(dir); err == nil {
		t.Fatal("an unreadable CA should be refused")
	}
	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(kept) != "not a certificate" {
		t.Errorf("the file was replaced instead of refused: %q", kept)
	}
}

// The host grammar: a wildcard covers subdomains and not the apex, an exact name covers itself,
// and a bare "*" — which the vault refuses before it can get here — matches nothing.
func TestAllowedHostMatchesTheVaultsGrammar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		patterns []string
		host     string
		want     bool
	}{
		{"exact", []string{"api.example.com"}, "api.example.com", true},
		{"case and trailing dot", []string{"api.example.com"}, "API.Example.com.", true},
		{"exact is not a suffix", []string{"api.example.com"}, "evil-api.example.com", false},
		{"wildcard covers a subdomain", []string{"*.example.com"}, "api.example.com", true},
		{"wildcard does not cover the apex", []string{"*.example.com"}, "example.com", false},
		{"wildcard is not a substring", []string{"*.example.com"}, "example.com.evil.test", false},
		{"bare star matches nothing", []string{"*"}, "api.example.com", false},
		{"empty list matches nothing", nil, "api.example.com", false},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()
			if got := allowedHost(one.patterns, one.host); got != one.want {
				t.Errorf("allowedHost(%v, %q) = %v, want %v", one.patterns, one.host, got, one.want)
			}
		})
	}
}

// A client that did not configure a proxy reaches this port by mistake. Answering it as though it
// were an origin would make a proxy port look like a web server; the refusal says what it is.
func TestARequestThatIsNotAProxyRequestIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil, Grant{Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder, Hosts: []string{fakeHost}, Headers: true})

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + h.addr + "/ping")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(string(body), "secrets proxy") {
		t.Errorf("the answer should say what this port is: %q", body)
	}
}

// A body the proxy cannot read is a body it cannot promise anything about, so the request is
// refused rather than forwarded on a guess.
func TestABodyTheProxyCannotReadIsRefused(t *testing.T) {
	t.Parallel()
	p, err := New(t.TempDir(), Config{Grants: []Grant{{Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder, Hosts: []string{fakeHost}, Body: true}}})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://"+fakeHost+"/v1/things", failingBody{})
	_, resp := p.request(req, nil)
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a body that cannot be read should be refused: %+v", resp)
	}
}

// A body larger than the proxy will read is refused for a host a secret covers: what was not read
// is a placeholder that was not found, and forwarding it would be a guess.
func TestAnOversizedBodyIsRefusedForASecretHost(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	h := newHarness(t, nil, Grant{
		Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder,
		Hosts: []string{"127.0.0.1"}, Body: true,
	})
	client := h.client(t, h.token)

	status, body := do(t, client, http.MethodPost, upstream.URL, nil, strings.Repeat("a", maxBody+1))
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413: %s", status, body)
	}
	if hits.Load() != 0 {
		t.Errorf("the upstream was contacted %d times for a refused request", hits.Load())
	}
}

// A listener that is already gone is reported, not swallowed: a proxy nobody can reach must not
// look like one that is serving.
func TestTheProxyReportsAFailureAfterItsListenerIsGone(t *testing.T) {
	t.Parallel()
	p, err := New(t.TempDir(), Config{})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	if err := p.listen.Close(); err != nil {
		t.Fatalf("close the listener: %v", err)
	}
	if err := p.Serve(context.Background()); err == nil {
		t.Fatal("serving a closed listener should be reported")
	}
}

// A caller that names no token gets 256 bits of it: an open proxy on a loopback port is a
// substitution service for anything on the machine.
func TestTheTokenIsMintedWhenTheCallerGivesNone(t *testing.T) {
	t.Parallel()
	caDir := t.TempDir()
	p, err := New(caDir, Config{})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	token := p.Token()
	if len(token) != 64 {
		t.Fatalf("the token should be 32 bytes of hex, got %d characters", len(token))
	}
	if _, err := hex.DecodeString(token); err != nil {
		t.Fatalf("the token is not hex: %v", err)
	}
	if _, err := os.Stat(CAPath(caDir)); err != nil {
		t.Fatalf("a proxy without a CA cannot be trusted by the sandbox: %v", err)
	}
}

// A CONNECT target carries a port and a grant carries a name: dropping the port is the whole
// translation, and a target that never had one is left alone.
func TestHostOnlyDropsThePort(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"example.com:443": "example.com",
		"example.com":     "example.com",
		"[::1]:443":       "::1",
		"10.0.0.1:8080":   "10.0.0.1",
	}
	for target, want := range cases {
		if got := hostOnly(target); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", target, got, want)
		}
	}
}

// Both credentials a client can send are accepted, and nothing else is. The comparisons are the
// boundary on a port every process on the machine can dial, so each way of getting it wrong is a
// case.
func TestAuthorisedAcceptsOnlyTheTwoFormsAClientCanSend(t *testing.T) {
	t.Parallel()
	p, err := New(t.TempDir(), Config{Token: "the-token"})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	basic := func(value string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(value))
	}
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"bearer", "Bearer the-token", true},
		{"bearer lower case", "bearer the-token", true},
		{"basic", basic("acornd:the-token"), true},
		{"empty user", basic(":the-token"), true},
		{"no header", "", false},
		{"no space", "Bearer", false},
		{"wrong bearer", "Bearer other", false},
		{"wrong password", basic("acornd:other"), false},
		{"basic without a colon", basic("the-token"), false},
		{"basic that is not base64", "Basic !!!not-base64!!!", false},
		{"unknown scheme", "Token the-token", false},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()
			header := http.Header{}
			if one.header != "" {
				header.Set("Proxy-Authorization", one.header)
			}
			if got := p.authorised(header); got != one.want {
				t.Errorf("authorised(%q) = %v, want %v", one.header, got, one.want)
			}
		})
	}
}

// The two adapters that turn someone else's log line into this package's: one line, no formatting
// surprises, and nothing that could carry a request body.
func TestTheLogAdaptersWriteOneLine(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	if n, err := (logWriter{rec.printf}).Write([]byte("http: TLS handshake error from 127.0.0.1\n")); n == 0 || err != nil {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
	(proxyLogger{rec.printf}).Printf("signing for %s", fakeHost)
	logged := rec.all()
	if !strings.Contains(logged, "TLS handshake error from 127.0.0.1") {
		t.Errorf("the server's line did not reach the log: %q", logged)
	}
	if !strings.Contains(logged, "signing for "+fakeHost) {
		t.Errorf("the library's line did not reach the log: %q", logged)
	}
}

// failingBody is a request body that cannot be read, which is what a dropped connection looks like
// to the proxy halfway through a request.
type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingBody) Close() error             { return nil }

// A file too large to be a CA is refused before it is parsed: a full disk that left a gigabyte of
// zeros where the key was is not a certificate that happens to be long.
func TestACAFileTooLargeToBeOneIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, caFile)
	if err := os.WriteFile(path, make([]byte, maxCAFile+1), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadOrCreateCA(dir); err == nil {
		t.Fatal("an oversized file should be refused")
	} else if !strings.Contains(err.Error(), "too large") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

// A certificate that may not sign other certificates cannot be the proxy's CA: it would make the
// install start and then fail every interception at the handshake.
func TestACertificateThatMayNotSignIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, caFile), leafPEM(t), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadOrCreateCA(dir); err == nil {
		t.Fatal("a leaf certificate should be refused as a CA")
	} else if !strings.Contains(err.Error(), "may not sign") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

// A CA folder that cannot be made is reported where it happens, naming the folder: the alternative
// is a proxy that starts and cannot be trusted by anything.
func TestACAFolderThatCannotBeMadeIsReported(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	blocked := filepath.Join(parent, "not-a-folder")
	if err := os.WriteFile(blocked, []byte("a file where the folder should go"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadOrCreateCA(filepath.Join(blocked, "proxy")); err == nil {
		t.Fatal("a CA folder that cannot be made should be reported")
	} else if !strings.Contains(err.Error(), "CA folder") {
		t.Errorf("the refusal should name what failed: %v", err)
	}
}

// CONNECT is answered by the connect handler, and never reaches the rewrite path. Saying so is
// cheaper than a rewrite that cannot run, and this test is what keeps the early return honest.
func TestAConnectNeverReachesTheRewritePath(t *testing.T) {
	t.Parallel()
	p, err := New(t.TempDir(), Config{})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	req := httptest.NewRequest(http.MethodConnect, "https://"+fakeHost, nil)
	if _, resp := p.request(req, nil); resp != nil {
		t.Fatalf("a CONNECT should be left to the connect handler, got %+v", resp)
	}
}

// The certificate cache signs each host once and hands the failure back: a host whose certificate
// could not be made must not leave a half-entry that the next connection trusts.
func TestTheCertCacheSignsOnceAndKeepsAFailure(t *testing.T) {
	t.Parallel()
	cache := newCertCache()
	signed := 0
	gen := func() (*tls.Certificate, error) {
		signed++
		return &tls.Certificate{Certificate: [][]byte{{1, 2, 3}}}, nil
	}
	first, err := cache.Fetch(fakeHost, gen)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	second, err := cache.Fetch(fakeHost, gen)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if signed != 1 {
		t.Errorf("the certificate was signed %d times for one host", signed)
	}
	if first != second {
		t.Error("the cache handed back a different certificate")
	}

	boom := fmt.Errorf("cannot sign")
	if _, err := cache.Fetch("other.test", func() (*tls.Certificate, error) { return nil, boom }); err == nil {
		t.Fatal("a signing failure should be returned")
	}
	if _, cached := cache.certs["other.test"]; cached {
		t.Error("a failed signing left an entry behind")
	}
}

// leafPEM is a certificate that is not a CA: the shape a caller gets from a web server, and not
// what a trust anchor is.
func leafPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "a leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	combined := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return append(combined, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})...)
}

// The address a caller names is the address it gets: a sandbox's argv carries the port pasta
// forwards, so a proxy that bound something else would leave a workspace forwarding into nothing.
func TestTheProxyBindsTheAddressItIsGiven(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := New(dir, Config{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	addr := first.Addr()
	second, err := New(dir, Config{Addr: addr})
	if err == nil {
		t.Fatalf("a port already bound should be refused, got %s", second.Addr())
	}
	third, err := New(dir, Config{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("a fresh address should be accepted: %v", err)
	}
	if third.Addr() == addr {
		t.Log("the kernel reused the port, which is its business; the assertion above is the one that matters")
	}
}

// A rotation is a grant swap under a running proxy: the next request is decided against the new
// set, and nothing is restarted. Before the swap the placeholder is substituted; after it the
// host is not one the secret names, so the request is tunnelled and the placeholder travels as
// text — which is what "the new set is in force" looks like from outside.
func TestGrantsCanBeSwappedUnderARunningProxy(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin, Grant{
		Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder,
		Hosts: []string{fakeHost}, Headers: true,
	})
	client := h.client(t, h.token, h.caCert, origin.cert(t))

	status, body := do(t, client, http.MethodGet, "https://"+fakeHost+"/v1/things",
		map[string]string{"Authorization": "Bearer " + thePlaceholder}, "")
	if status != http.StatusOK || !strings.Contains(body, "auth=Bearer "+theValue) {
		t.Fatalf("the first grant should substitute: %d %s", status, body)
	}

	h.proxy.SetGrants([]Grant{{
		Name: "OTHER", Value: "other-value", Placeholder: "$MSB_OTHER",
		Hosts: []string{"api.other.test"}, Headers: true,
	}})

	status, body = do(t, client, http.MethodGet, "https://"+fakeHost+"/v1/things",
		map[string]string{"Authorization": "Bearer " + thePlaceholder}, "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	if strings.Contains(body, theValue) {
		t.Fatalf("the old grant still substituted after the swap: %q", body)
	}
	if !strings.Contains(body, "auth=Bearer "+thePlaceholder) {
		t.Errorf("the new set should no longer cover this host: %q", body)
	}
}

// caPEM is the certificate half of the install's CA file, which is what a sandbox has to trust.
func caPEM(t *testing.T, dir string) []byte {
	t.Helper()
	block, _ := pem.Decode(readCAFile(t, dir))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("%s should start with a certificate", CAPath(dir))
	}
	return pem.EncodeToMemory(block)
}

func readCAFile(t *testing.T, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(CAPath(dir))
	if err != nil {
		t.Fatalf("read the CA: %v", err)
	}
	return data
}

// A granted name that answers with an address inside the machine is refused, not dialled: that is how
// a name on the internet becomes the machine's own services, and a secret sent there is gone.
func TestASecretIsNotSentToAnAddressInsideTheMachine(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin, Grant{
		Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder,
		Hosts: []string{fakeHost}, Headers: true,
	})
	// The name is granted and the request is a substitution; only the answer is wrong.
	h.proxy.out = &pinner{
		lookup: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("10.0.0.5")}, nil
		},
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
		seen: map[string]pinnedAnswer{},
	}
	client := h.client(t, h.token, h.caCert, origin.cert(t))

	status, body := do(t, client, http.MethodGet, "https://"+fakeHost+"/v1/things",
		map[string]string{"Authorization": "Bearer " + thePlaceholder}, "")
	if status != http.StatusForbidden {
		t.Fatalf("a private answer should be refused with 403, got %d %s", status, body)
	}
	if !strings.Contains(body, "inside this machine") {
		t.Errorf("the refusal should say why: %q", body)
	}
	if origin.hits.Load() != 0 {
		t.Errorf("the upstream was contacted through a private answer")
	}
	if logged := h.logs.all(); !strings.Contains(logged, "inside this machine") {
		t.Errorf("the log should say why: %q", logged)
	}
}

// The connection goes to the address that was resolved and checked, which is the whole gate: a
// second lookup is a window an answer can change in.
func TestTheConnectionGoesToTheAddressThatWasResolved(t *testing.T) {
	t.Parallel()
	origin := newOrigin(t)
	h := newHarness(t, origin, Grant{
		Name: "TEST_TOKEN", Value: theValue, Placeholder: thePlaceholder,
		Hosts: []string{fakeHost}, Headers: true,
	})
	client := h.client(t, h.token, h.caCert, origin.cert(t))

	status, body := do(t, client, http.MethodGet, "https://"+fakeHost+"/v1/things",
		map[string]string{"Authorization": "Bearer " + thePlaceholder}, "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	dials := h.logs.dials()
	if len(dials) == 0 {
		t.Fatal("the proxy did not dial anything")
	}
	if want := net.JoinHostPort(testPublicIP, "443"); dials[0] != want {
		t.Errorf("the proxy dialled %q, want the address it resolved (%q)", dials[0], want)
	}
}

// One answer serves every request inside the window: a page of API calls is one lookup, and a host
// that moves is followed when the answer goes stale.
func TestOneAnswerServesEveryRequestInsideTheWindow(t *testing.T) {
	t.Parallel()
	lookups := 0
	pin := &pinner{
		lookup: func(context.Context, string) ([]net.IP, error) {
			lookups++
			return []net.IP{net.ParseIP(testPublicIP)}, nil
		},
		seen: map[string]pinnedAnswer{},
	}
	for range 3 {
		if _, err := pin.resolve(context.Background(), fakeHost); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	}
	if lookups != 1 {
		t.Errorf("the host was looked up %d times inside the window", lookups)
	}
	// A stale answer is looked up again, which is how a host that moved is followed.
	pin.mu.Lock()
	pin.seen[fakeHost] = pinnedAnswer{ips: pin.seen[fakeHost].ips, looked: time.Now().Add(-2 * resolveTTL)}
	pin.mu.Unlock()
	if _, err := pin.resolve(context.Background(), fakeHost); err != nil {
		t.Fatalf("resolve again: %v", err)
	}
	if lookups != 2 {
		t.Errorf("a stale answer should be looked up again, got %d lookups", lookups)
	}
}

// The address classes a value must never travel to, and the ones it may.
func TestWhatCountsAsInsideTheMachine(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"127.0.0.1":                          true,
		"::1":                                true,
		"10.0.0.5":                           true,
		"192.168.1.10":                       true,
		"172.16.0.1":                         true,
		"169.254.169.254":                    true,
		"fe80::1":                            true,
		"fd00::1":                            true,
		"0.0.0.0":                            true,
		"198.51.100.10":                      false,
		"93.184.216.34":                      false,
		"2606:2800:220:1:248:1893:25c8:1946": false,
	}
	for raw, want := range cases {
		if got := insideTheMachine(net.ParseIP(raw)); got != want {
			t.Errorf("insideTheMachine(%s) = %v, want %v", raw, got, want)
		}
	}
}

// What the pin refuses, and what it does when nothing answers: each of these is a way a request
// could otherwise be forwarded with no address that was ever decided on.
func TestThePinRefusesWhatItCannotUse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		lookup func(context.Context, string) ([]net.IP, error)
		says   string
	}{
		{
			name:   "the resolver fails",
			lookup: func(context.Context, string) ([]net.IP, error) { return nil, errors.New("no such host") },
			says:   "resolving",
		},
		{
			name:   "the name has no address",
			lookup: func(context.Context, string) ([]net.IP, error) { return nil, nil },
			says:   "no address",
		},
		{
			name:   "the answer is inside the machine",
			lookup: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("169.254.169.254")}, nil },
			says:   "inside this machine",
		},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()
			pin := &pinner{lookup: one.lookup, seen: map[string]pinnedAnswer{}}
			if _, err := pin.resolve(context.Background(), fakeHost); err == nil {
				t.Fatalf("a lookup that cannot be used should be refused")
			} else if !strings.Contains(err.Error(), one.says) {
				t.Errorf("the refusal should say %q: %v", one.says, err)
			}
		})
	}

	// A dial that reaches nothing reports the host and its addresses rather than a bare error.
	pin := &pinner{
		lookup: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP(testPublicIP)}, nil
		},
		dial: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("refused") },
		seen: map[string]pinnedAnswer{},
	}
	if _, err := pin.dialPinned(context.Background(), "tcp", net.JoinHostPort(fakeHost, "443"), fakeHost); err == nil {
		t.Fatal("a dial that answered nothing should be an error")
	} else if !strings.Contains(err.Error(), fakeHost) {
		t.Errorf("the error should name the host: %v", err)
	}

	// An address that is not host:port cannot be pinned, and the proxy says so rather than dialling it.
	p, err := New(t.TempDir(), Config{})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	if _, err := p.dial(context.Background(), "tcp", "no-port-here"); err != nil {
		t.Logf("a dial of %q is the test's own dialler's answer: %v", "no-port-here", err)
	}
	if _, err := p.out.dialPinned(context.Background(), "tcp", "no-port-here", fakeHost); err == nil {
		t.Fatal("an address without a port cannot be pinned")
	}
}
