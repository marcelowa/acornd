// The door, checked from outside: the one question a person asks after setting it up, answered without asking
// somebody to open a browser.
//
// ADR 0033 §8. What it asserts is what a browser meets: the hostname resolves, TLS holds, Cloudflare Access
// answers instead of this process, and the keys an assertion is verified against are reachable. The interesting
// failure is the one that looks like success — a 200 or this install's own 401 means something reached the
// console *without* the edge in front of it, which is worse than a door that does not open.
package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DoorReport is what the check found, in the shape the screen renders: a verdict, a sentence, and the few facts
// underneath it.
type DoorReport struct {
	// Guarded is the whole answer: the hostname answered, and the answer came from Access rather than the console.
	Guarded bool `json:"guarded"`
	// Status is what the hostname answered with, for the sentence.
	Status int `json:"status"`
	// Login is where Access sends a browser that has no session — the team's own login page.
	Login string `json:"login,omitempty"`
	// Keys is whether the verification keys are reachable, because an assertion nobody can check is a door that
	// opens onto a 401.
	Keys bool `json:"keys"`
	// Detail is one line, in the words of the person who pressed the button.
	Detail string `json:"detail"`
}

// checkBudget is how long one check waits. Two round trips to the same edge, and an edge that needs longer than
// this is an edge that is down.
const checkBudget = 20 * time.Second

// CheckDoor fetches the public hostname the way an unauthenticated browser would, and then fetches the keys the
// assertion is verified against. A client is passed in so a test can answer both without a network.
func CheckDoor(ctx context.Context, client *http.Client, hostname, issuer string) DoorReport {
	ctx, cancel := context.WithTimeout(ctx, checkBudget)
	defer cancel()
	if client == nil {
		client = &http.Client{Timeout: checkBudget}
	}
	if strings.TrimSpace(hostname) == "" {
		return DoorReport{Detail: "this install has no hostname yet: make the door first"}
	}
	report, ok := fetchDoor(ctx, client, hostname, issuer)
	if !ok {
		return report
	}
	keys, err := checkKeys(ctx, client, issuer)
	report.Keys = keys
	if err != nil {
		report.Detail = "the door answers, but the keys it verifies with are not reachable: " + shortError(err)
		return report
	}
	report.Guarded = true
	report.Detail = "the door is up: an unauthenticated browser is sent to the login page, and the keys are reachable"
	return report
}

// fetchDoor asks the public hostname the way a browser with nobody signed in would, and judges the answer. The
// second return is whether the answer was the edge in front of the console rather than the console itself.
func fetchDoor(ctx context.Context, client *http.Client, hostname, issuer string) (DoorReport, bool) {
	address := "https://" + strings.TrimPrefix(strings.TrimPrefix(hostname, "https://"), "http://")
	report := DoorReport{}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/", nil)
	if err != nil {
		report.Detail = "the hostname is not a URL: " + err.Error()
		return report, false
	}
	// No redirect following: the redirect *is* the answer. Following it would land on the login page and turn a
	// guarded door into an ordinary 200.
	direct := *client
	direct.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := direct.Do(request)
	if err != nil {
		report.Detail = "the hostname did not answer: " + shortError(err)
		return report, false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
	report.Status = response.StatusCode
	report.Login = response.Header.Get("Location")
	switch {
	case response.StatusCode == http.StatusOK || response.StatusCode == http.StatusUnauthorized:
		// The console answered. Either it is open to the world, or the edge is not in front of it at all.
		report.Detail = "the console answered directly, so nothing is in front of it: this is worse than a door that does not open"
		return report, false
	case response.StatusCode < 300 || response.StatusCode >= 400:
		report.Detail = fmt.Sprintf("the hostname answered %d, which is neither the door nor a way in", response.StatusCode)
		return report, false
	case !pointsAtLogin(report.Login, issuer):
		report.Detail = fmt.Sprintf("the hostname sent a browser to %s, which is not this install's login domain", trimURL(report.Login))
		return report, false
	}
	return report, true
}

// pointsAtLogin reports whether the redirect goes to a Cloudflare Access login.
//
// With an issuer recorded, it must be that exact host: an install pointed at somebody else's team would send the
// family to a page that admits them nowhere. Without one — the issuer is an environment variable, and an install
// that has not set it yet still deserves an answer — the redirect has to be a Cloudflare Access login, which is the
// half of the claim that can be checked from the outside. A comparison of hosts rather than of whole URLs, because
// Cloudflare appends a path and a query this code has no business predicting.
func pointsAtLogin(location, issuer string) bool {
	if strings.TrimSpace(location) == "" {
		return false
	}
	to, err := url.Parse(location)
	if err != nil || to.Host == "" {
		return false
	}
	if strings.TrimSpace(issuer) == "" {
		return strings.HasSuffix(strings.ToLower(to.Host), ".cloudflareaccess.com")
	}
	team, err := url.Parse(pointsAt(issuer))
	if err != nil || team.Host == "" {
		return false
	}
	return strings.EqualFold(to.Host, team.Host)
}

// pointsAt is the issuer as a URL. An issuer is a bare host in some setups and a URL in others, and both mean the
// same page.
func pointsAt(issuer string) string {
	trimmed := strings.TrimSpace(issuer)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "://") {
		return trimmed
	}
	return "https://" + trimmed
}

// checkKeys fetches the certificate endpoint the verifier reads, and asks only that it answers with keys.
func checkKeys(ctx context.Context, client *http.Client, issuer string) (bool, error) {
	base := pointsAt(issuer)
	if base == "" {
		return false, fmt.Errorf("this install records no issuer, so an assertion cannot be checked")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/cdn-cgi/access/certs", nil)
	if err != nil {
		return false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%s answered %s", request.URL, response.Status)
	}
	var published struct {
		Keys []json.RawMessage `json:"keys"`
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(payload, &published); err != nil {
		return false, fmt.Errorf("the keys are not readable: %w", err)
	}
	if len(published.Keys) == 0 {
		return false, fmt.Errorf("the keys endpoint answered with none")
	}
	return true, nil
}

// shortError is one line for a screen: a transport error's own text runs to several.
func shortError(err error) string {
	text := err.Error()
	if len(text) > 120 {
		return text[:120] + "…"
	}
	return text
}

// trimURL keeps a redirect readable: the training query Cloudflare sends is longer than the sentence it belongs in.
func trimURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return raw
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.Path
}
