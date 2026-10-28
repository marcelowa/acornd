package remote

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// door is a fake edge: the hostname redirects to the team login, and the team publishes keys. Both are what a
// working door looks like from outside.
func door(t *testing.T, status int, location string, keys int) (*http.Client, string) {
	t.Helper()
	// The real check refuses anything but https, which is the property worth keeping, so the fake edge speaks TLS
	// too. The client skips verification because both certificates are made in this function and thrown away with
	// the servers — there is no authority to verify them against.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	team := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cdn-cgi/access/certs" {
			http.NotFound(w, r)
			return
		}
		if keys == 0 {
			_, _ = w.Write([]byte(`{"keys":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"keys":[{"kid":"one"}]}`))
	}))
	t.Cleanup(team.Close)
	host := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status >= 300 && status < 400 {
			http.Redirect(w, r, strings.ReplaceAll(location, "TEAM", team.URL), status)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(host.Close)
	return client, strings.TrimPrefix(host.URL, "https://") + "|" + team.URL
}

// split is the two addresses a test needs: the public hostname and the team's issuer.
func split(t *testing.T, both string) (string, string) {
	t.Helper()
	host, team, ok := strings.Cut(both, "|")
	if !ok {
		t.Fatalf("bad fixture: %q", both)
	}
	return host, team
}

// A guarded door is the whole point: the hostname sends a browser to the team's login, and the keys are there to
// verify what comes back.
func TestAGuardedDoorIsGuarded(t *testing.T) {
	client, both := door(t, http.StatusFound, "TEAM/cdn-cgi/access/login?kid=x", 1)
	host, team := split(t, both)
	report := CheckDoor(context.Background(), client, host, team)
	if !report.Guarded || !report.Keys {
		t.Fatalf("a guarded door reported %+v", report)
	}
	if report.Status != http.StatusFound || report.Login == "" {
		t.Errorf("the report says %+v", report)
	}
}

// The failure that looks like success: the console answering directly means nothing is in front of it. A 200 is
// worse than a door that does not open, and the sentence has to say so.
func TestAConsoleAnsweringDirectlyIsWorseThanADoorThatDoesNotOpen(t *testing.T) {
	client, both := door(t, http.StatusOK, "", 1)
	host, team := split(t, both)
	report := CheckDoor(context.Background(), client, host, team)
	if report.Guarded {
		t.Fatalf("an open console reported a guarded door: %+v", report)
	}
	if !strings.Contains(report.Detail, "nothing is in front of it") {
		t.Errorf("the sentence does not say what is wrong: %q", report.Detail)
	}
}

// Our own 401 is the same finding: the request reached the process, so the edge is not the thing refusing it.
func TestTheConsolesOwn401IsRefusedAsAGuardedDoor(t *testing.T) {
	client, both := door(t, http.StatusUnauthorized, "", 1)
	host, team := split(t, both)
	if report := CheckDoor(context.Background(), client, host, team); report.Guarded {
		t.Errorf("the console's own refusal counted as a guarded door: %+v", report)
	}
}

// A redirect to somewhere else is not this install's login: a hostname pointing at another team's Access app
// would send the family to a page that admits them nowhere.
func TestARedirectToAnotherLoginDomainIsRefused(t *testing.T) {
	client, both := door(t, http.StatusFound, "https://somebody-else.cloudflareaccess.com/login", 1)
	host, team := split(t, both)
	report := CheckDoor(context.Background(), client, host, team)
	if report.Guarded {
		t.Fatalf("a stranger's login counted: %+v", report)
	}
	if !strings.Contains(report.Detail, "not this install's login domain") {
		t.Errorf("the sentence does not say which part is wrong: %q", report.Detail)
	}
}

// A door whose keys cannot be read is a door that opens onto a 401: the assertion arrives and nobody can check it.
func TestADoorWithoutReachableKeysIsNotAGuardedDoor(t *testing.T) {
	client, both := door(t, http.StatusFound, "TEAM/cdn-cgi/access/login", 0)
	host, team := split(t, both)
	report := CheckDoor(context.Background(), client, host, team)
	if report.Guarded {
		t.Fatalf("a door with no keys counted: %+v", report)
	}
	if !strings.Contains(report.Detail, "keys") {
		t.Errorf("the sentence does not mention the keys: %q", report.Detail)
	}
}

// No hostname is the state of an install that never made the door, and the answer says what to do rather than
// pretending a check happened.
func TestAnInstallWithNoHostnameSaysSo(t *testing.T) {
	report := CheckDoor(context.Background(), nil, "", "")
	if report.Guarded || !strings.Contains(report.Detail, "no hostname") {
		t.Errorf("the report says %+v", report)
	}
}

// A hostname that does not answer is a failure with the reason, not a panic and not a pass.
func TestAHostnameThatDoesNotAnswerIsRefused(t *testing.T) {
	report := CheckDoor(context.Background(), &http.Client{}, "127.0.0.1:1", "https://team.cloudflareaccess.com")
	if report.Guarded {
		t.Fatalf("an unreachable hostname counted: %+v", report)
	}
	if !strings.Contains(report.Detail, "did not answer") {
		t.Errorf("the sentence does not say what happened: %q", report.Detail)
	}
}

// An install whose service has no issuer set — the variable is the operator's, not the file's — still gets an
// answer: a redirect to a Cloudflare Access login is the edge in front of the console, and the report names the team
// domain so the person can compare it with what they configured.
func TestWithoutAnIssuerACloudflareLoginIsEnough(t *testing.T) {
	// The redirect names a real Cloudflare login while the keys live on the local fake: the point of this case is
	// the half that can be judged without an issuer.
	client, both := door(t, http.StatusFound, "https://acornd.cloudflareaccess.com/cdn-cgi/access/login?kid=x", 1)
	host, _ := split(t, both)
	report := CheckDoor(context.Background(), client, host, "")
	// The redirect half passes and the keys half cannot be judged, so this is not a guarded door — and the
	// sentence says exactly which half is missing rather than blaming the door.
	if report.Guarded {
		t.Fatalf("a door with no issuer to check keys against reported guarded: %+v", report)
	}
	if !strings.Contains(report.Detail, "issuer") {
		t.Errorf("the sentence does not name the missing issuer: %q", report.Detail)
	}
}

// A redirect to somewhere that is not Cloudflare's login is refused even with no issuer to compare against.
func TestWithoutAnIssuerAStrangersLoginIsStillRefused(t *testing.T) {
	client, both := door(t, http.StatusFound, "https://somebody-else.example.com/login", 1)
	host, _ := split(t, both)
	report := CheckDoor(context.Background(), client, host, "")
	if report.Guarded {
		t.Fatalf("a stranger's login counted with no issuer set: %+v", report)
	}
}
