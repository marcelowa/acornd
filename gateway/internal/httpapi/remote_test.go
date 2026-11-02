package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/identity"
	"github.com/marcelowa/acornd/gateway/internal/people"
	"github.com/marcelowa/acornd/gateway/internal/remote"
	"github.com/marcelowa/acornd/gateway/internal/update"
)

// fakeCloudflare is the account the screen talks to: enough of it to see the order the handler works in.
type fakeCloudflare struct {
	zones      map[string]remote.Zone
	zoneErr    error
	tunnels    []remote.Tunnel
	ingress    string
	records    []remote.DNSRecord
	groups     []remote.Group
	apps       []remote.App
	policies   []remote.Policy
	idps       []remote.IdentityProvider
	nextID     int
	createdIn  []string
	teamDomain string
	// onGroupWrite runs after a group is created or rewritten: a test that needs the book to move while the apply
	// is in the air hangs the change here, because that is the only place the two orders can be made to differ.
	onGroupWrite func(include []string)
	groupWrites  int
}

func newCloudflare() *fakeCloudflare {
	return &fakeCloudflare{nextID: 0, teamDomain: "team.cloudflareaccess.com"}
}

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

// Zones is the setup screen's first question, answered from the zones this fake was told about. Sorted, because the
// real one answers in Cloudflare's order and nothing here may depend on it.
func (f *fakeCloudflare) Zones(context.Context) ([]remote.Zone, error) {
	out := make([]remote.Zone, 0, len(f.zones))
	for _, zone := range f.zones {
		out = append(out, zone)
	}
	slices.SortFunc(out, func(a, b remote.Zone) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (f *fakeCloudflare) Tunnels(context.Context, string) ([]remote.Tunnel, error) {
	return f.tunnels, nil
}

func (f *fakeCloudflare) CreateTunnel(context.Context, string, string) (remote.Tunnel, error) {
	tunnel := remote.Tunnel{ID: f.id("t"), Name: "acornd"}
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

func (f *fakeCloudflare) AccessOrganization(context.Context, string) (string, error) {
	return f.teamDomain, nil
}

func (f *fakeCloudflare) AccessGroups(context.Context, string) ([]remote.Group, error) {
	return f.groups, nil
}

func (f *fakeCloudflare) CreateAccessGroup(_ context.Context, _, name string, include []string) (remote.Group, error) {
	group := remote.Group{ID: f.id("g"), Name: name, Emails: include}
	f.groups = append(f.groups, group)
	f.createdIn = append(f.createdIn, "group")
	f.groupWritten(include)
	return group, nil
}

func (f *fakeCloudflare) UpdateAccessGroup(_ context.Context, _, id, name string, include []string) (remote.Group, error) {
	for index := range f.groups {
		if f.groups[index].ID == id {
			f.groups[index].Emails = include
			f.groupWritten(include)
			return f.groups[index], nil
		}
	}
	return remote.Group{}, errors.New("no such group")
}

func (f *fakeCloudflare) AccessApps(context.Context, string) ([]remote.App, error) {
	return f.apps, nil
}

func (f *fakeCloudflare) CreateAccessApp(_ context.Context, _, name, hostname, _ string) (remote.App, error) {
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
	return screenWith(t, api, false, "")
}

// screenWith is screen with the two facts an enable reads: whether this boot built the door, and the --host the run
// was started with. Both are facts of the gate, not of the file (ADR 0036 §6).
func screenWith(t *testing.T, api *fakeCloudflare, publisher bool, runHost string) (http.Handler, *people.Book, string) {
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
	gate := Gate{Door: stubDoor{caller: identity.Caller{Admin: true}}, People: opened, Dir: dir, Publisher: publisher, RunHost: runHost}
	return admitted(t, gate), opened, dir
}

// cloudflareAt points the remote routes at a test server through a real client, so a test reads the sentence the
// client writes rather than a fake's imitation of it.
func cloudflareAt(t *testing.T, base string) {
	t.Helper()
	previous := remoteAPI
	remoteAPI = func(token string) remote.API { return &remote.Client{Token: token, Base: base} }
	t.Cleanup(func() { remoteAPI = previous })
}

// saveDoorFile writes what an apply leaves behind: the hostname, and the two values the door is built from.
func saveDoorFile(t *testing.T, dir, audience, domain string) {
	t.Helper()
	setup := remote.Setup{
		Desired: remote.Desired{Hostname: "console.example.com", ZoneID: "z", AccountID: "a"},
		State:   remote.State{TunnelID: "t", Audience: audience, TeamDomain: domain},
	}
	if err := remote.Save(dir, setup, time.Now()); err != nil {
		t.Fatalf("save the door: %v", err)
	}
}

// accountAt is Cloudflare enough for the preview: the zone list and the account's login domain, in the shapes the API
// answers with. Every Authorization header it was sent is kept, so a test can prove which token made the call.
func accountAt(t *testing.T, zones, authDomain string) (*httptest.Server, *[]string) {
	t.Helper()
	sent := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = append(sent, r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/zones":
			_, _ = fmt.Fprintf(w, `{"success":true,"result":%s}`, zones)
		case strings.HasSuffix(r.URL.Path, "/access/organizations"):
			_, _ = fmt.Fprintf(w, `{"success":true,"result":{"auth_domain":%q}}`, authDomain)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &sent
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

// The team domain travels with the status, named the way the interface reads it: it is half of what the door is
// built from, and the screen shows it beside the audience without opening the file itself (ADR 0036 §5).
func TestTheRemoteStatusCarriesTheTeamDomain(t *testing.T) {
	api := newCloudflare()
	handler, _, dir := screen(t, api)
	if err := remote.Save(dir, remote.Setup{
		Desired: remote.Desired{Hostname: "console.example.com", ZoneID: "z", AccountID: "a"},
		State:   remote.State{TunnelID: "t", Audience: "aud-1", TeamDomain: "team.cloudflareaccess.com"},
	}, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/remote", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var answer remoteState
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if answer.TeamDomain != "team.cloudflareaccess.com" || answer.Audience != "aud-1" {
		t.Errorf("the status says %+v, want the team domain beside the audience", answer)
	}
	if !strings.Contains(recorder.Body.String(), `"teamDomain"`) {
		t.Errorf("the answer does not name the field the screen reads: %s", recorder.Body.String())
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
	if state.TeamDomain != "team.cloudflareaccess.com" {
		t.Errorf("the apply learned the team domain and did not answer with it: %+v", state)
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
	t.Setenv("ACORND_CLOUDFLARE_TOKEN", "")
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

// The book can move while the apply that follows it is in the air, because the two changes are a second apart and
// each starts its own reconcile. The slower apply must not write the older list last: a revoked address would be
// back in the group until the next boot (ADR 0033 §3).
func TestTheGroupConvergesWhenTheBookMovesDuringAnApply(t *testing.T) {
	api := newCloudflare()
	_, opened, dir := screen(t, api)
	if _, err := opened.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add ana: %v", err)
	}
	if _, err := opened.Add("ben@example.com", "Ben"); err != nil {
		t.Fatalf("add ben: %v", err)
	}
	if err := remote.Save(dir, remote.Setup{
		Desired:  remote.Desired{Hostname: "console.example.com", ZoneID: "z", AccountID: "a"},
		State:    remote.State{TunnelID: "t"},
		APIToken: "api-token-secret",
	}, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	// The first write of the group is the one that lands before the revoke: the book changes right after it, and
	// the reconcile has to notice and write the new list rather than stop on the older one.
	moved := false
	api.onGroupWrite = func(include []string) {
		if moved || !slices.Contains(include, "ana@example.com") {
			return
		}
		moved = true
		if err := opened.Revoke("ana@example.com"); err != nil {
			t.Errorf("revoke during the apply: %v", err)
		}
	}
	server := &Server{gate: Gate{People: opened, Dir: dir}}
	server.reconcileAfterPeople()

	if api.groupWrites < 2 {
		t.Fatalf("the group was written %d times, want a second pass after the book moved", api.groupWrites)
	}
	var last []string
	for _, group := range api.groups {
		last = group.Emails
	}
	if slices.Contains(last, "ana@example.com") {
		t.Errorf("the revoked address is back in the group: %v", last)
	}
	if !slices.Contains(last, "ben@example.com") {
		t.Errorf("the group does not name ben, who never left: %v", last)
	}
}

// The check answers from what the install recorded, and it needs no API token: an install whose token was deleted
// can still ask whether the door opens.
func TestTheDoorCheckNeedsNoApiToken(t *testing.T) {
	t.Setenv("ACORND_CLOUDFLARE_TOKEN", "")
	t.Setenv("ACORND_IDENTITY_ISSUER", "")
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

// The screen's first press is a question and not a write: the token is asked what it can see, and the answer is the
// zones it may write a record in, the account each lives in, and the login domain the door will be checked against.
// The token stays where it was pasted — storing it is the apply's job (ADR 0033 §4, ADR 0036 §6).
func TestThePreviewAnswersWhatTheTokenCanSee(t *testing.T) {
	server, sent := accountAt(t,
		`[{"id":"z1","name":"acornd.app","account":{"id":"acc-1"}},{"id":"z2","name":"example.com","account":{"id":"acc-1"}}]`,
		"acornd.cloudflareaccess.com")
	handler, _, dir := screen(t, newCloudflare())
	cloudflareAt(t, server.URL)
	saveDoorFile(t, dir, "aud", "acornd.cloudflareaccess.com")
	before, err := remote.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	before.APIToken = "stored-token"
	if err := remote.Save(dir, before, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/remote/preview", strings.NewReader(`{"token":"pasted-token"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var answer previewAnswer
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(answer.Zones) != 2 {
		t.Fatalf("the preview answered %+v, want both zones the token can see", answer.Zones)
	}
	if answer.Zones[0].Name != "acornd.app" || answer.Zones[0].ID != "z1" || answer.Zones[0].AccountID != "acc-1" {
		t.Errorf("the first zone is %+v, want its name, its id and its account", answer.Zones[0])
	}
	if answer.TeamDomain != "acornd.cloudflareaccess.com" {
		t.Errorf("teamDomain = %q, want the account's login domain", answer.TeamDomain)
	}
	if !strings.Contains(recorder.Body.String(), `"teamDomain"`) {
		t.Errorf("the answer does not name the field the screen reads: %s", recorder.Body.String())
	}
	if len(*sent) == 0 || (*sent)[0] != "Bearer pasted-token" {
		t.Errorf("the calls carried %v, want the token that was pasted", *sent)
	}
	after, err := remote.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if after.APIToken != "stored-token" {
		t.Error("the preview stored the token it was given, which only an apply may do")
	}
}

// A token missing a permission is the whole point of the screen: the refusal is the client's own sentence, naming the
// scope to add, rather than "could not validate" (ADR 0033 §2).
func TestThePreviewNamesTheScopeTheTokenIsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Unauthorized to access requested resource"}]}`))
	}))
	defer server.Close()
	handler, _, _ := screen(t, newCloudflare())
	cloudflareAt(t, server.URL)
	request := httptest.NewRequest(http.MethodPost, "/api/remote/preview", strings.NewReader(`{"token":"pasted-token"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Zone · DNS · Edit") {
		t.Errorf("the refusal does not name the missing scope: %s", recorder.Body.String())
	}
}

// An account whose Access has never been used answers with no login domain, and that is not a failure: the zones are
// still the answer, and the apply fills the team domain in when a door is made (ADR 0036 §6).
func TestAnAccountWithNoAccessShowsItsZonesAndNoTeamDomain(t *testing.T) {
	server, _ := accountAt(t, `[{"id":"z1","name":"acornd.app","account":{"id":"acc-1"}}]`, "")
	handler, _, _ := screen(t, newCloudflare())
	cloudflareAt(t, server.URL)
	request := httptest.NewRequest(http.MethodPost, "/api/remote/preview", strings.NewReader(`{"token":"pasted-token"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var answer previewAnswer
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(answer.Zones) != 1 || answer.Zones[0].Name != "acornd.app" {
		t.Errorf("the zones are %+v, want the one the token can see", answer.Zones)
	}
	if answer.TeamDomain != "" {
		t.Errorf("teamDomain = %q, want none recorded", answer.TeamDomain)
	}
}

// A token that may see zones but not organizations still answers the question it was asked: the team domain is a
// display value, and the scope it would need is named by the apply that needs it.
func TestATeamDomainTheTokenCannotReadIsNotAFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zones" {
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"z1","name":"acornd.app","account":{"id":"acc-1"}}]}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Unauthorized"}]}`))
	}))
	defer server.Close()
	handler, _, _ := screen(t, newCloudflare())
	cloudflareAt(t, server.URL)
	request := httptest.NewRequest(http.MethodPost, "/api/remote/preview", strings.NewReader(`{"token":"pasted-token"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var answer previewAnswer
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(answer.Zones) != 1 || answer.TeamDomain != "" {
		t.Errorf("the answer is %+v, want the zone and no team domain", answer)
	}
}

// A form with nothing to preview is refused in the words that name what to paste, before Cloudflare is asked.
func TestThePreviewNeedsAToken(t *testing.T) {
	t.Setenv("ACORND_CLOUDFLARE_TOKEN", "")
	handler, _, _ := screen(t, newCloudflare())
	request := httptest.NewRequest(http.MethodPost, "/api/remote/preview", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "Cloudflare API token") {
		t.Errorf("the refusal does not say what is missing: %s", recorder.Body.String())
	}
}

// The apply completes a label with the zone before anything is created, and refuses a name in another zone rather
// than writing a record into a zone this install does not manage (ADR 0036 §6).
func TestTheApplyCompletesALabelWithItsZone(t *testing.T) {
	cases := []struct {
		name, hostname, want string
		status               int
	}{
		{name: "a label", hostname: "console", want: "console.example.com", status: http.StatusOK},
		{name: "a whole name", hostname: "console.example.com", want: "console.example.com", status: http.StatusOK},
		{name: "another zone", hostname: "console.acornd.app", want: "", status: http.StatusBadRequest},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			api := newCloudflare()
			api.zones = map[string]remote.Zone{"example.com": {ID: "zone-1", Account: "acc-1"}}
			handler, _, dir := screen(t, api)
			body := strings.NewReader(`{"hostname":"` + test.hostname + `","zone":"example.com","token":"api-token"}`)
			request := httptest.NewRequest(http.MethodPost, "/api/remote/apply", body)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.status, recorder.Body.String())
			}
			if test.status != http.StatusOK {
				if len(api.createdIn) != 0 {
					t.Errorf("a refused hostname created %v", api.createdIn)
				}
				if !strings.Contains(recorder.Body.String(), "example.com") {
					t.Errorf("the refusal does not name the zone: %s", recorder.Body.String())
				}
				return
			}
			var answer remoteState
			if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if answer.Hostname != test.want {
				t.Errorf("the answer says %q, want %q", answer.Hostname, test.want)
			}
			setup, err := remote.Load(dir)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if setup.Desired.Hostname != test.want {
				t.Errorf("the file records %q, want %q", setup.Desired.Hostname, test.want)
			}
		})
	}
}

// A door in the file and no door in this process is the one fact the screen turns into Turn on: the apply wrote the
// file after the boot that is serving. A boot that built the door says false however the file reads, because a
// restart would change nothing (ADR 0036 §6).
func TestTheStatusSaysWhetherARestartIsWhatIsMissing(t *testing.T) {
	cases := []struct {
		name                string
		configured          bool
		audience            string
		publisher, expected bool
	}{
		{name: "a recorded door this boot has not built", configured: true, audience: "aud-1", expected: true},
		{name: "a door this boot built", configured: true, audience: "aud-1", publisher: true, expected: false},
		{name: "a half-finished apply", configured: true, expected: false},
		{name: "no door at all", expected: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if test.configured {
				saveDoorFile(t, dir, test.audience, "team.cloudflareaccess.com")
			}
			gate := Gate{Dir: dir, Publisher: test.publisher, Door: stubDoor{caller: identity.Caller{Admin: true}}, People: claimed(t)}
			recorder := httptest.NewRecorder()
			admitted(t, gate).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/remote", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			var answer remoteState
			if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if answer.RestartRequired != test.expected {
				t.Errorf("restartRequired = %v, want %v", answer.RestartRequired, test.expected)
			}
		})
	}
}

// The apply's answer carries the same fact, so the screen can draw Turn on without asking the status again.
func TestTheApplyAnswerSaysWhetherARestartIsMissing(t *testing.T) {
	cases := []struct {
		name                string
		publisher, expected bool
	}{
		{name: "this boot has no door", expected: true},
		{name: "this boot built it", publisher: true, expected: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			api := newCloudflare()
			api.zones = map[string]remote.Zone{"example.com": {ID: "zone-1", Account: "acc-1"}}
			handler, _, _ := screenWith(t, api, test.publisher, "")
			body := strings.NewReader(`{"hostname":"console","zone":"example.com","token":"api-token"}`)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/remote/apply", body))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
			}
			var answer remoteState
			if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if answer.RestartRequired != test.expected {
				t.Errorf("restartRequired = %v, want %v", answer.RestartRequired, test.expected)
			}
		})
	}
}

// enableScreen is the console itself, for the tests that press Turn on: a claimed book, the admin's proof, an update tier
// to restart through, and a restart that is a function — a test must not become the console.
func enableScreen(t *testing.T, publisher bool, runHost string) (*Server, string, chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	gate := Gate{
		Dir:       dir,
		Publisher: publisher,
		RunHost:   runHost,
		Door:      stubDoor{caller: identity.Caller{Admin: true}},
		People:    claimed(t),
	}
	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}
	server := New(nil, static, update.New(dir, ""), gate)
	restarts := make(chan struct{}, 4)
	server.restart = func() error {
		restarts <- struct{}{}
		return nil
	}
	return server, dir, restarts
}

// enabled presses the button and answers what the screen was told.
func enabled(t *testing.T, server *Server) (int, map[string]any, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/remote/enable", nil))
	var answer map[string]any
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return recorder.Code, answer, recorder.Body.String()
}

// A file that does not record a door is nothing to turn on, and the refusal names the piece an apply would write
// next: a restart here would look like the button did something (ADR 0036 §6).
func TestTheEnableRefusesAHalfFinishedDoor(t *testing.T) {
	cases := []struct {
		name      string
		write     bool
		audience  string
		team      string
		refusalIs string
	}{
		{name: "no file", refusalIs: "nothing to turn on"},
		{name: "no audience", write: true, team: "team.cloudflareaccess.com", refusalIs: "no audience"},
		{name: "no team domain", write: true, audience: "aud-1", refusalIs: "no team domain"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server, dir, restarts := enableScreen(t, false, "")
			if test.write {
				saveDoorFile(t, dir, test.audience, test.team)
			}
			code, _, body := enabled(t, server)
			if code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", code, body)
			}
			if !strings.Contains(body, test.refusalIs) {
				t.Errorf("the refusal does not say %q: %s", test.refusalIs, body)
			}
			select {
			case <-restarts:
				t.Error("a refused enable restarted the console")
			default:
			}
		})
	}
}

// A door this boot already built is nothing to do, and saying so is the answer: a restart with no reason is a console
// that disappears for a moment for a button that did nothing (ADR 0036 §6).
func TestTheEnableSaysWhenThereIsNothingToDo(t *testing.T) {
	server, dir, restarts := enableScreen(t, true, "")
	saveDoorFile(t, dir, "aud-1", "team.cloudflareaccess.com")
	code, answer, body := enabled(t, server)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", code, body)
	}
	if answer["restarting"] != false {
		t.Errorf("the answer is %v, want restarting: false", answer)
	}
	select {
	case <-restarts:
		t.Error("a door that is already on restarted the console")
	default:
	}
}

// One press turns it on: the answer leaves, and the process becomes one that reads remote.json as the door — the same
// exec the update screen finishes with, once and once only (ADR 0036 §6).
func TestTheEnableRestartsTheConsoleOnce(t *testing.T) {
	server, dir, restarts := enableScreen(t, false, "127.0.0.1")
	saveDoorFile(t, dir, "aud-1", "team.cloudflareaccess.com")
	code, answer, body := enabled(t, server)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", code, body)
	}
	if answer["restarting"] != true {
		t.Errorf("the answer is %v, want restarting: true", answer)
	}
	select {
	case <-restarts:
	case <-time.After(5 * time.Second):
		t.Fatal("the press answered restarting and nothing restarted")
	}
	select {
	case <-restarts:
		t.Error("the console restarted twice for one press")
	case <-time.After(100 * time.Millisecond):
	}
}

// A restart keeps the arguments the process already has, so a console started on every interface cannot become a door
// install by pressing the button: the sentence says to start it again without --host (ADR 0036 §5–§6).
func TestTheEnableRefusesARunStartedOnEveryInterface(t *testing.T) {
	server, dir, restarts := enableScreen(t, false, "0.0.0.0")
	saveDoorFile(t, dir, "aud-1", "team.cloudflareaccess.com")
	code, _, body := enabled(t, server)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", code, body)
	}
	for _, want := range []string{"--host 0.0.0.0", "without --host"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not say %q: %s", want, body)
		}
	}
	select {
	case <-restarts:
		t.Error("a run that cannot become a door restarted anyway")
	default:
	}
}

// A build with no update tier cannot replace itself, and names the command that does it instead of pretending.
func TestTheEnableNeedsAnUpdateTier(t *testing.T) {
	dir := t.TempDir()
	saveDoorFile(t, dir, "aud-1", "team.cloudflareaccess.com")
	gate := Gate{Dir: dir, Door: stubDoor{caller: identity.Caller{Admin: true}}, People: claimed(t)}
	server := New(nil, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}, nil, gate)
	code, _, body := enabled(t, server)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", code, body)
	}
	if !strings.Contains(body, "start it again") {
		t.Errorf("the refusal does not say what to do: %s", body)
	}
}

// Which listen addresses are this machine only, in the terms the run uses: a name, or an address the IP parser knows
// to be loopback. A door on one address and a hole on the others is the failure this list prevents (ADR 0032 §8).
func TestWhichAddressesAreThisMachineOnly(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":   true,
		"::1":         true,
		"localhost":   true,
		"LOCALHOST":   true,
		"0.0.0.0":     false,
		"192.0.2.1":   false,
		"example.com": false,
		"":            false,
	}
	for host, want := range cases {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// The check judges the redirect and the keys from the issuer the door is built from. A file-only install sets no
// variable, so the issuer comes from the team domain an apply wrote down — and the environment, when it set one, is
// the override it always was (ADR 0036 §5, ADR 0033 §8).
func TestTheDoorCheckUsesTheIssuerTheDoorUses(t *testing.T) {
	cases := []struct {
		name        string
		environment string
		want        string
	}{
		{name: "the file", want: "https://team.cloudflareaccess.com"},
		{name: "the environment", environment: "https://env.cloudflareaccess.com", want: "https://env.cloudflareaccess.com"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			asked := make(chan string, 1)
			previous := doorCheck
			doorCheck = func(_ context.Context, _ *http.Client, _, issuer string) remote.DoorReport {
				asked <- issuer
				return remote.DoorReport{Detail: "a test's answer"}
			}
			t.Cleanup(func() { doorCheck = previous })
			t.Setenv("ACORND_IDENTITY_ISSUER", test.environment)
			handler, _, dir := screen(t, newCloudflare())
			saveDoorFile(t, dir, "aud-1", "team.cloudflareaccess.com")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/remote/check", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
			}
			select {
			case got := <-asked:
				if got != test.want {
					t.Errorf("the check was given the issuer %q, want %q", got, test.want)
				}
			default:
				t.Fatal("the check never ran")
			}
		})
	}
}

// groupWritten is the hook every group write goes through, and the count a test reads to see how many applies ran.
func (f *fakeCloudflare) groupWritten(include []string) {
	f.groupWrites++
	if f.onGroupWrite != nil {
		f.onGroupWrite(include)
	}
}
