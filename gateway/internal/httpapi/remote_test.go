package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/identity"
	"github.com/acornfall/acornfall/gateway/internal/people"
	"github.com/acornfall/acornfall/gateway/internal/remote"
)

// fakeCloudflare is the account the screen talks to: enough of it to see the order the handler works in.
type fakeCloudflare struct {
	zones     map[string]remote.Zone
	zoneErr   error
	tunnels   []remote.Tunnel
	ingress   string
	records   []remote.DNSRecord
	groups    []remote.Group
	apps      []remote.App
	policies  []remote.Policy
	idps      []remote.IdentityProvider
	nextID    int
	createdIn []string
}

func newCloudflare() *fakeCloudflare { return &fakeCloudflare{nextID: 0} }

func (f *fakeCloudflare) id(prefix string) string {
	f.nextID++
	return prefix + string(rune('a'+f.nextID%26))
}

func (f *fakeCloudflare) ZoneByName(_ context.Context, name string) (remote.Zone, error) {
	if f.zoneErr != nil {
		return remote.Zone{}, f.zoneErr
	}
	if zone, ok := f.zones[name]; ok {
		return zone, nil
	}
	return remote.Zone{}, errors.New("no such zone")
}

func (f *fakeCloudflare) Tunnels(context.Context, string) ([]remote.Tunnel, error) {
	return f.tunnels, nil
}

func (f *fakeCloudflare) CreateTunnel(context.Context, string, string) (remote.Tunnel, error) {
	tunnel := remote.Tunnel{ID: f.id("t"), Name: "acornfall"}
	f.tunnels = append(f.tunnels, tunnel)
	f.createdIn = append(f.createdIn, "tunnel")
	return tunnel, nil
}

func (f *fakeCloudflare) TunnelToken(context.Context, string, string) (string, error) {
	return "token", nil
}

func (f *fakeCloudflare) TunnelIngress(context.Context, string, string) (string, error) {
	return f.ingress, nil
}

func (f *fakeCloudflare) SetTunnelIngress(_ context.Context, _, _, _, service string) error {
	f.ingress = service
	f.createdIn = append(f.createdIn, "ingress")
	return nil
}

func (f *fakeCloudflare) DNSRecords(context.Context, string, string) ([]remote.DNSRecord, error) {
	return f.records, nil
}

func (f *fakeCloudflare) CreateCNAME(_ context.Context, _, hostname, target string) (remote.DNSRecord, error) {
	record := remote.DNSRecord{ID: f.id("r"), Name: hostname, Type: "CNAME", Content: target}
	f.records = append(f.records, record)
	f.createdIn = append(f.createdIn, "dns")
	return record, nil
}

func (f *fakeCloudflare) UpdateCNAME(_ context.Context, _ string, record remote.DNSRecord, target string) (remote.DNSRecord, error) {
	record.Content = target
	return record, nil
}

func (f *fakeCloudflare) IdentityProviders(context.Context, string) ([]remote.IdentityProvider, error) {
	return f.idps, nil
}

func (f *fakeCloudflare) CreateOneTimePIN(context.Context, string) (remote.IdentityProvider, error) {
	provider := remote.IdentityProvider{ID: f.id("idp"), Type: "onetimepin"}
	f.idps = append(f.idps, provider)
	f.createdIn = append(f.createdIn, "pin")
	return provider, nil
}

func (f *fakeCloudflare) AccessGroups(context.Context, string) ([]remote.Group, error) {
	return f.groups, nil
}

func (f *fakeCloudflare) CreateAccessGroup(_ context.Context, _, name string, include []string) (remote.Group, error) {
	group := remote.Group{ID: f.id("g"), Name: name, Emails: include}
	f.groups = append(f.groups, group)
	f.createdIn = append(f.createdIn, "group")
	return group, nil
}

func (f *fakeCloudflare) UpdateAccessGroup(_ context.Context, _, id, name string, include []string) (remote.Group, error) {
	for index := range f.groups {
		if f.groups[index].ID == id {
			f.groups[index].Emails = include
			return f.groups[index], nil
		}
	}
	return remote.Group{}, errors.New("no such group")
}

func (f *fakeCloudflare) AccessApps(context.Context, string) ([]remote.App, error) {
	return f.apps, nil
}

func (f *fakeCloudflare) CreateAccessApp(_ context.Context, _, name, hostname string) (remote.App, error) {
	app := remote.App{ID: f.id("a"), Name: name, Domain: hostname, Audience: "audience-" + f.id("x")}
	f.apps = append(f.apps, app)
	f.createdIn = append(f.createdIn, "app")
	return app, nil
}

func (f *fakeCloudflare) AccessPolicies(context.Context, string, string) ([]remote.Policy, error) {
	return f.policies, nil
}

func (f *fakeCloudflare) CreateAccessPolicy(_ context.Context, _, _, group string) (remote.Policy, error) {
	policy := remote.Policy{ID: f.id("p"), Group: group}
	f.policies = append(f.policies, policy)
	f.createdIn = append(f.createdIn, "policy")
	return policy, nil
}

// screen builds the console's handler with a door that proves the admin, a book with one person, and a fake
// account behind the two remote routes.
func screen(t *testing.T, api *fakeCloudflare) (http.Handler, *people.Book, string) {
	t.Helper()
	dir := t.TempDir()
	opened, err := people.Open(filepath.Join(dir, people.File))
	if err != nil {
		t.Fatalf("open book: %v", err)
	}
	if _, err := opened.Claim("admin@example.com", "Admin"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	previous := remoteAPI
	remoteAPI = func(string) remote.API { return api }
	t.Cleanup(func() { remoteAPI = previous })
	gate := Gate{Door: stubDoor{caller: identity.Caller{Admin: true}}, People: opened, Dir: dir}
	return admitted(t, gate), opened, dir
}

func TestRemoteStatusSaysNothingIsConfiguredYet(t *testing.T) {
	api := newCloudflare()
	handler, _, _ := screen(t, api)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/remote", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var state remoteState
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.Configured {
		t.Errorf("a fresh install is configured: %+v", state)
	}
}

func TestRemoteApplyMakesWhatTheFormAskedFor(t *testing.T) {
	api := newCloudflare()
	api.zones = map[string]remote.Zone{"example.com": {ID: "zone-1", Account: "acc-1"}}
	handler, _, dir := screen(t, api)
	body := strings.NewReader(`{"hostname":"console.example.com","zone":"example.com","token":"api-token"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/remote/apply", body)
	request.Header.Set("content-type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var state remoteState
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !state.Configured || state.Hostname != "console.example.com" || state.Audience == "" {
		t.Errorf("the answer says %+v", state)
	}
	if len(state.Steps) == 0 {
		t.Error("the answer carries no steps")
	}
	// What it made is written down, because that is what a second press resumes from.
	setup, err := remote.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !setup.Configured() || setup.State.TunnelID == "" {
		t.Errorf("the apply wrote %+v", setup)
	}
}

// A screen that cannot reach Cloudflare says so in the shape the page renders, and keeps what it made.
func TestRemoteApplyReportsAFailedApplyWithItsSteps(t *testing.T) {
	api := newCloudflare()
	api.zoneErr = errors.New("Cloudflare said no")
	handler, _, _ := screen(t, api)
	body := strings.NewReader(`{"hostname":"console.example.com","zone":"example.com","token":"api-token"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/remote/apply", body)
	request.Header.Set("content-type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "Cloudflare said no") {
		t.Errorf("the refusal does not say what happened: %s", recorder.Body.String())
	}
}

// A form with no token to use is refused before anything is created, and the refusal names what to fix.
func TestRemoteApplyWithoutATokenCreatesNothing(t *testing.T) {
	t.Setenv("ACORNFALL_CLOUDFLARE_TOKEN", "")
	api := newCloudflare()
	handler, _, dir := screen(t, api)
	body := strings.NewReader(`{"hostname":"console.example.com","zone":"example.com"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/remote/apply", body)
	request.Header.Set("content-type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if len(api.createdIn) != 0 {
		t.Errorf("a refused apply created %v", api.createdIn)
	}
	if setup, err := remote.Load(dir); err != nil {
		t.Fatalf("load: %v", err)
	} else if setup.Configured() {
		t.Error("a refused apply was written down")
	}
}

// The token is kept, 0600, so the door follows the book without a service unit carrying it — and it never
// crosses the status boundary, which is the half of ADR 0033 §4 that still holds.
func TestRemoteApplyKeepsTheTokenButNeverReturnsIt(t *testing.T) {
	api := newCloudflare()
	api.zones = map[string]remote.Zone{"example.com": {ID: "zone-1", Account: "acc-1"}}
	handler, _, dir := screen(t, api)
	body := strings.NewReader(`{"hostname":"console.example.com","zone":"example.com","token":"api-token-secret"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/remote/apply", body)
	request.Header.Set("content-type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	payload, err := os.ReadFile(filepath.Join(dir, remote.File))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(payload), "api-token-secret") {
		t.Error("the API token was not kept, so the door cannot follow the book")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/remote", nil))
	if strings.Contains(recorder.Body.String(), "api-token-secret") {
		t.Error("the status returned the API token")
	}
	var answer remoteState
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !answer.TokenStored {
		t.Error("the status does not say the token is stored")
	}
}

// Forget is the other half: the token leaves the install, and what is at Cloudflare stays.
func TestForgettingTheTokenKeepsTheDoor(t *testing.T) {
	api := newCloudflare()
	handler, _, dir := screen(t, api)
	if err := remote.Save(dir, remote.Setup{
		Desired:  remote.Desired{Hostname: "console.example.com", ZoneID: "z", AccountID: "a"},
		State:    remote.State{TunnelID: "t"},
		APIToken: "api-token-secret",
	}, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/remote/forget-token", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	setup, err := remote.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if setup.APIToken != "" {
		t.Error("the token survived the forget")
	}
	if setup.Desired.Hostname == "" {
		t.Error("forget removed the door")
	}
}

// A person added in the book is in the group after the reconcile that follows the write (ADR 0033 §3).
func TestAPersonAddedInTheBookReachesTheDoor(t *testing.T) {
	api := newCloudflare()
	_, opened, dir := screen(t, api)
	if _, err := opened.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := remote.Save(dir, remote.Setup{
		Desired:  remote.Desired{Hostname: "console.example.com", ZoneID: "z", AccountID: "a"},
		State:    remote.State{TunnelID: "t"},
		APIToken: "api-token-secret",
	}, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	server := &Server{gate: Gate{People: opened, Dir: dir}}
	server.reconcileAfterPeople()

	found := false
	for _, group := range api.groups {
		for _, email := range group.Emails {
			if email == "ana@example.com" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("the group does not name the new person: %+v", api.groups)
	}
}

// The check answers from what the install recorded, and it needs no API token: an install whose token was deleted
// can still ask whether the door opens.
func TestTheDoorCheckNeedsNoApiToken(t *testing.T) {
	t.Setenv("ACORNFALL_CLOUDFLARE_TOKEN", "")
	t.Setenv("ACORNFALL_IDENTITY_ISSUER", "")
	api := newCloudflare()
	handler, _, dir := screen(t, api)
	if err := remote.Save(dir, remote.Setup{Desired: remote.Desired{Hostname: "127.0.0.1:1", ZoneID: "z", AccountID: "a"}}, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/remote/check", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var report remote.DoorReport
	if err := json.Unmarshal(recorder.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.Guarded {
		t.Errorf("a hostname that answers nothing reported guarded: %+v", report)
	}
	if report.Detail == "" {
		t.Error("the report carries no sentence")
	}
}

// An install with no door has nothing to check, and says so rather than reporting a failure of a door it never made.
func TestTheDoorCheckRefusesAnInstallWithNoDoor(t *testing.T) {
	api := newCloudflare()
	handler, _, _ := screen(t, api)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/remote/check", nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
}
