package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// fake is a Cloudflare account in memory. Every call is recorded, so a test can assert not only what was
// created but that a second run created nothing at all.
type fake struct {
	calls      []string
	tunnels    []Tunnel
	records    []DNSRecord
	idps       []IdentityProvider
	groups     []Group
	apps       []App
	policies   []Policy
	failOn     string
	nextID     int
	orgAccount string
	ingress    string
	teamDomain string
	appIDP     string
	// visible is what the token can see, which is the question the setup screen asks before an apply.
	visible []Zone
}

func newFake() *fake { return &fake{teamDomain: "team.cloudflareaccess.com"} }

func (f *fake) id() string {
	f.nextID++
	return fmt.Sprintf("id-%d", f.nextID)
}

func (f *fake) record(call string) error {
	f.calls = append(f.calls, call)
	if f.failOn != "" && strings.HasPrefix(call, f.failOn) {
		return fmt.Errorf("%s is unavailable", call)
	}
	return nil
}

func (f *fake) created() []string {
	out := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		// Anything that changes the account, whatever its verb: a route written is as much a write as a
		// tunnel created, and a helper that only knew about "create" would have missed it.
		if strings.HasPrefix(call, "create") || strings.HasPrefix(call, "update") || strings.HasPrefix(call, "set") {
			out = append(out, call)
		}
	}
	return out
}

// Zones is the setup screen's first question: what can this token see. It records the call like the rest, so a test
// can assert the preview asked without creating anything.
func (f *fake) Zones(context.Context) ([]Zone, error) {
	if err := f.record("zones"); err != nil {
		return nil, err
	}
	return f.visible, nil
}

func (f *fake) Tunnels(context.Context, string) ([]Tunnel, error) {
	if err := f.record("tunnels"); err != nil {
		return nil, err
	}
	return f.tunnels, nil
}

func (f *fake) CreateTunnel(_ context.Context, _, name string) (Tunnel, error) {
	if err := f.record("createTunnel"); err != nil {
		return Tunnel{}, err
	}
	tunnel := Tunnel{ID: f.id(), Name: name}
	f.tunnels = append(f.tunnels, tunnel)
	return tunnel, nil
}

func (f *fake) TunnelToken(_ context.Context, _, id string) (string, error) {
	if err := f.record("tunnelToken"); err != nil {
		return "", err
	}
	return "token-for-" + id, nil
}

func (f *fake) TunnelIngress(context.Context, string, string) (string, error) {
	if err := f.record("tunnelIngress"); err != nil {
		return "", err
	}
	return f.ingress, nil
}

func (f *fake) SetTunnelIngress(_ context.Context, _, _, hostname, service string) error {
	if err := f.record("setTunnelIngress"); err != nil {
		return err
	}
	f.ingress = service
	f.calls = append(f.calls, "hostname="+hostname)
	return nil
}

func (f *fake) UpdateCNAME(_ context.Context, _ string, record DNSRecord, target string) (DNSRecord, error) {
	if err := f.record("updateCNAME"); err != nil {
		return DNSRecord{}, err
	}
	record.Content = target
	for index, existing := range f.records {
		if existing.ID == record.ID {
			f.records[index] = record
		}
	}
	return record, nil
}

func (f *fake) DNSRecords(context.Context, string, string) ([]DNSRecord, error) {
	if err := f.record("dnsRecords"); err != nil {
		return nil, err
	}
	return f.records, nil
}

func (f *fake) CreateCNAME(_ context.Context, _, hostname, target string) (DNSRecord, error) {
	if err := f.record("createCNAME"); err != nil {
		return DNSRecord{}, err
	}
	record := DNSRecord{ID: f.id(), Name: hostname, Type: "CNAME", Content: target}
	f.records = append(f.records, record)
	return record, nil
}

func (f *fake) IdentityProviders(context.Context, string) ([]IdentityProvider, error) {
	if err := f.record("identityProviders"); err != nil {
		return nil, err
	}
	return f.idps, nil
}

func (f *fake) CreateOneTimePIN(context.Context, string) (IdentityProvider, error) {
	if err := f.record("createOneTimePIN"); err != nil {
		return IdentityProvider{}, err
	}
	provider := IdentityProvider{ID: f.id(), Type: "onetimepin"}
	f.idps = append(f.idps, provider)
	return provider, nil
}

func (f *fake) AccessOrganization(_ context.Context, account string) (string, error) {
	if err := f.record("accessOrganization"); err != nil {
		return "", err
	}
	f.orgAccount = account
	return f.teamDomain, nil
}
func (f *fake) AccessGroups(context.Context, string) ([]Group, error) {
	if err := f.record("accessGroups"); err != nil {
		return nil, err
	}
	return f.groups, nil
}

func (f *fake) CreateAccessGroup(_ context.Context, _, name string, emails []string) (Group, error) {
	if err := f.record("createAccessGroup"); err != nil {
		return Group{}, err
	}
	group := Group{ID: f.id(), Name: name, Emails: emails}
	f.groups = append(f.groups, group)
	return group, nil
}

func (f *fake) UpdateAccessGroup(_ context.Context, _, id, name string, emails []string) (Group, error) {
	if err := f.record("updateAccessGroup"); err != nil {
		return Group{}, err
	}
	for index := range f.groups {
		if f.groups[index].ID == id {
			f.groups[index].Name, f.groups[index].Emails = name, emails
			return f.groups[index], nil
		}
	}
	return Group{}, errors.New("no such group")
}

func (f *fake) AccessApps(context.Context, string) ([]App, error) {
	if err := f.record("accessApps"); err != nil {
		return nil, err
	}
	return f.apps, nil
}

func (f *fake) CreateAccessApp(_ context.Context, _, name, hostname, idp string) (App, error) {
	if err := f.record("createAccessApp"); err != nil {
		return App{}, err
	}
	f.appIDP = idp
	app := App{ID: f.id(), Name: name, Domain: hostname, Audience: f.id()}
	f.apps = append(f.apps, app)
	return app, nil
}

func (f *fake) AccessPolicies(context.Context, string, string) ([]Policy, error) {
	if err := f.record("accessPolicies"); err != nil {
		return nil, err
	}
	return f.policies, nil
}

func (f *fake) CreateAccessPolicy(_ context.Context, _, app, group string) (Policy, error) {
	if err := f.record("createAccessPolicy"); err != nil {
		return Policy{}, err
	}
	policy := Policy{ID: f.id(), Name: "allow the group", Group: group}
	f.policies = append(f.policies, policy)
	return policy, nil
}

func desired() Desired {
	return Desired{Hostname: "testing.acornd.app", ZoneID: "zone", AccountID: "acc"}
}

// The first apply creates every piece, in the order the door needs them: the tunnel and its record, then the
// way in, then who may come through.
func TestTheFirstApplyCreatesEverything(t *testing.T) {
	api := newFake()
	state, steps, err := (&Provisioner{API: api}).Apply(context.Background(), desired(), State{}, []string{"ana@example.com"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := []string{"createTunnel", "setTunnelIngress", "createCNAME", "createOneTimePIN", "createAccessGroup", "createAccessApp", "createAccessPolicy"}
	if got := api.created(); !reflect.DeepEqual(got, want) {
		t.Errorf("created %v, want %v", got, want)
	}
	if !state.Ready() {
		t.Errorf("state = %+v, want everything the door needs", state)
	}
	if state.TunnelToken != "token-for-"+state.TunnelID {
		t.Errorf("the token was not kept: %q", state.TunnelToken)
	}
	if state.Audience == "" {
		t.Error("the application's audience tag was not recorded, and an assertion is checked against it")
	}
	if api.appIDP != state.IdentityID {
		t.Errorf("the application was made for provider %q, want the one this apply created (%q): a browser has to see the PIN form", api.appIDP, state.IdentityID)
	}
	if len(steps) != 8 {
		t.Fatalf("%d steps, want one per piece", len(steps))
	}
	for _, step := range steps {
		if !step.Created {
			t.Errorf("%s was not reported as created", step.Name)
		}
	}
}

// The team domain is what the door's issuer and keys URL are made of, and the apply is what learns it: read
// from the account, written beside the ids, and reported as a step of its own. The first run is the one that
// found it; every run after keeps it (ADR 0036 §5).
func TestTheApplyRecordsTheTeamDomain(t *testing.T) {
	api := newFake()
	state, steps, err := (&Provisioner{API: api}).Apply(context.Background(), desired(), State{}, []string{"ana@example.com"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if state.TeamDomain != "team.cloudflareaccess.com" {
		t.Errorf("team domain = %q, want the account's", state.TeamDomain)
	}
	if api.orgAccount != "acc" {
		t.Errorf("the team domain was read for account %q, want the one the zone named", api.orgAccount)
	}
	if !slices.Contains(api.calls, "accessOrganization") {
		t.Errorf("the apply never asked the account for its team domain: %v", api.calls)
	}
	found := false
	for _, step := range steps {
		if step.Name == "team domain" {
			found = step.Created && step.Detail == "team.cloudflareaccess.com"
		}
	}
	if !found {
		t.Errorf("the team domain was not reported as learned: %+v", steps)
	}

	// The second apply finds the same domain and keeps it, so a boot says nothing changed.
	_, steps, err = (&Provisioner{API: api}).Apply(context.Background(), desired(), state, []string{"ana@example.com"})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	for _, step := range steps {
		if step.Name == "team domain" && step.Created {
			t.Errorf("the second apply reported the team domain as newly learned: %+v", step)
		}
	}
}

// The same file round-trips the domain through an apply and a write, because a boot reads it from there rather
// than from four variables.
func TestTheTeamDomainIsSavedWithTheRest(t *testing.T) {
	dir := t.TempDir()
	state, _, err := (&Provisioner{API: newFake()}).Apply(context.Background(), desired(), State{}, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := Save(dir, Setup{Desired: desired(), State: state}, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.State.TeamDomain != "team.cloudflareaccess.com" {
		t.Errorf("the saved state holds %q", loaded.State.TeamDomain)
	}
	if env := loaded.Env(); env["ACORND_IDENTITY_ISSUER"] != "https://team.cloudflareaccess.com" {
		t.Errorf("a saved setup names no issuer: %v", env)
	}
}

// The second apply is a no-op: it finds what the first one made and creates nothing. This is the property that
// makes a reconciler safe to run at every boot.
func TestASecondApplyCreatesNothing(t *testing.T) {
	api := newFake()
	provisioner := &Provisioner{API: api}
	first, _, err := provisioner.Apply(context.Background(), desired(), State{}, []string{"ana@example.com"})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	api.calls = nil

	second, steps, err := provisioner.Apply(context.Background(), desired(), first, []string{"ana@example.com"})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	// The group is the one thing rewritten every time: it follows the book, so writing it again with the same
	// addresses is how a change in the book is noticed at all. The route is read before it is written, so a
	// second apply leaves it alone.
	if got := api.created(); !reflect.DeepEqual(got, []string{"updateAccessGroup"}) {
		t.Errorf("the second apply created %v, want only the group rewritten", got)
	}
	for _, step := range steps {
		if step.Created && step.Name != "access group" {
			t.Errorf("%s was created twice", step.Name)
		}
	}
	if second.TunnelID != first.TunnelID || second.AppID != first.AppID {
		t.Error("the second apply replaced what the first made")
	}
	if second.TunnelToken != first.TunnelToken {
		t.Error("the second apply threw away the tunnel token")
	}
}

// A run that died halfway left its ids behind, and the next one picks up from the first thing it does not have.
func TestAnApplyResumesFromTheIdsItWrote(t *testing.T) {
	api := newFake()
	prior := State{TunnelID: "t-existing", TunnelToken: "kept", DNSRecordID: "r-existing"}
	api.tunnels = []Tunnel{{ID: "t-existing", Name: TunnelName}}
	api.ingress = DefaultLocal
	api.records = []DNSRecord{{ID: "r-existing", Name: "testing.acornd.app", Type: "CNAME", Content: "t-existing.cfargotunnel.com"}}

	state, steps, err := (&Provisioner{API: api}).Apply(context.Background(), desired(), prior, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := api.created(); !reflect.DeepEqual(got, []string{"createOneTimePIN", "createAccessGroup", "createAccessApp", "createAccessPolicy"}) {
		t.Errorf("created %v, want only what was missing", got)
	}
	if state.TunnelToken != "kept" {
		t.Errorf("the token was re-read when the record already had one: %q", state.TunnelToken)
	}
	if !steps[0].Created && steps[0].Name != "tunnel" {
		t.Errorf("the first step was %+v", steps[0])
	}
}

// The group is the owners' list of people, so it is rewritten whenever that list changes rather than created
// once and forgotten.
func TestTheGroupFollowsTheBook(t *testing.T) {
	api := newFake()
	provisioner := &Provisioner{API: api}
	first, _, err := provisioner.Apply(context.Background(), desired(), State{}, []string{"ana@example.com"})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(api.groups) != 1 || !reflect.DeepEqual(api.groups[0].Emails, []string{"ana@example.com"}) {
		t.Fatalf("the group does not hold the book: %+v", api.groups)
	}

	if _, _, err := provisioner.Apply(context.Background(), desired(), first, []string{"Ana@Example.com", "bo@example.com", " ana@example.com "}); err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := api.groups[0].Emails; !reflect.DeepEqual(got, []string{"ana@example.com", "bo@example.com"}) {
		t.Errorf("the group holds %v, want the book lowercased, trimmed and without repeats", got)
	}
}

// An account somebody configured by hand is adopted, not duplicated: a tunnel, an application and a group with
// our names are ours, whatever made them.
func TestWhatAlreadyExistsIsAdopted(t *testing.T) {
	api := newFake()
	api.tunnels = []Tunnel{{ID: "made-by-hand", Name: TunnelName}}
	api.ingress = DefaultLocal
	api.records = []DNSRecord{{ID: "r-hand", Name: "testing.acornd.app", Type: "CNAME", Content: "made-by-hand.cfargotunnel.com"}}
	api.idps = []IdentityProvider{{ID: "pin", Type: "onetimepin"}}
	api.groups = []Group{{ID: "g-hand", Name: GroupName}}
	api.apps = []App{{ID: "a-hand", Name: AppName, Domain: "testing.acornd.app", Audience: "aud-hand"}}
	api.policies = []Policy{{ID: "p-hand", Group: "g-hand"}}

	state, steps, err := (&Provisioner{API: api}).Apply(context.Background(), desired(), State{}, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := api.created(); !reflect.DeepEqual(got, []string{"updateAccessGroup"}) {
		t.Errorf("created %v, want nothing but the group rewritten", got)
	}
	if state.TunnelID != "made-by-hand" || state.AppID != "a-hand" || state.Audience != "aud-hand" || state.PolicyID != "p-hand" {
		t.Errorf("state = %+v, want the ids that were already there", state)
	}
	for _, step := range steps {
		// The group is rewritten and the team domain is learned: neither is a thing this account already had,
		// and everything else was found rather than made.
		if step.Created && step.Name != "access group" && step.Name != "team domain" {
			t.Errorf("%s was created although it existed", step.Name)
		}
	}
}

// A tunnel belonging to something else is never adopted, even if nothing else matches: it is not ours to use.
func TestATunnelThatIsNotOursIsLeftAlone(t *testing.T) {
	api := newFake()
	api.tunnels = []Tunnel{{ID: "someone-elses", Name: "waisman-server"}}
	if _, _, err := (&Provisioner{API: api}).Apply(context.Background(), desired(), State{}, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(api.tunnels) != 2 {
		t.Fatalf("tunnels = %+v, want ours created beside theirs", api.tunnels)
	}
}

// A failure halfway keeps what it wrote, so the next run resumes rather than starting over — and the error says
// which step it was on.
func TestAFailureKeepsWhatItHadAlreadyWritten(t *testing.T) {
	api := newFake()
	api.failOn = "createAccessApp"
	state, steps, err := (&Provisioner{API: api}).Apply(context.Background(), desired(), State{}, nil)
	if err == nil {
		t.Fatal("a failing apply reported success")
	}
	if !strings.Contains(err.Error(), "access application") {
		t.Errorf("err = %q, want the step named", err)
	}
	if state.TunnelID == "" || state.DNSRecordID == "" || state.GroupID == "" {
		t.Errorf("state = %+v, want everything written before the failure", state)
	}
	if state.AppID != "" {
		t.Errorf("the failed step recorded an id: %+v", state)
	}
	if state.TeamDomain == "" {
		t.Error("the team domain was not recorded, and the door is made of it")
	}
	if len(steps) != 6 {
		t.Errorf("%d steps reported, want the six that finished", len(steps))
	}
}

// A setup screen cannot create anything from a hostname that is not one, and the refusal says what is missing.
func TestADesireThatIsNotOneIsRefused(t *testing.T) {
	api := newFake()
	cases := []Desired{
		{Hostname: "", ZoneID: "z", AccountID: "a"},
		{Hostname: "not-a-hostname", ZoneID: "z", AccountID: "a"},
		{Hostname: "testing.acornd.app", ZoneID: "", AccountID: "a"},
		{Hostname: "testing.acornd.app", ZoneID: "z", AccountID: ""},
	}
	for _, desire := range cases {
		if _, _, err := (&Provisioner{API: api}).Apply(context.Background(), desire, State{}, nil); err == nil {
			t.Errorf("%+v was accepted", desire)
		}
	}
	if len(api.calls) != 0 {
		t.Errorf("a refused desire still called the API: %v", api.calls)
	}
}

// The token is the thing most likely to be wrong, and "Authentication error" is what Cloudflare says whether a
// permission is missing or the token is dead. The refusal names the permission the call needed, in the words of
// the screen the admin creates tokens on (ADR 0033 §2).
func TestARefusedCallNamesThePermissionItNeeded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
	}))
	defer server.Close()

	client := &Client{Token: "bad", Base: server.URL}
	_, err := client.Tunnels(context.Background(), "acc")
	if err == nil {
		t.Fatal("a refused call reported success")
	}
	for _, want := range []string{"Cloudflare Tunnel", "Authentication error"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	good := &Client{Token: "good", Base: server.URL}
	if _, err := good.Tunnels(context.Background(), "acc"); err != nil {
		t.Errorf("a permitted call failed: %v", err)
	}
}

// An answer that is not the envelope is reported as that rather than as an empty success: a truncated response
// would otherwise look like an account with nothing in it.
func TestAnAnswerThatIsNotTheEnvelopeIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>maintenance</html>`))
	}))
	defer server.Close()
	client := &Client{Token: "t", Base: server.URL}
	if _, err := client.Tunnels(context.Background(), "acc"); err == nil {
		t.Fatal("an unreadable answer reported success")
	}
}

// The zone is how the admin names a domain instead of memorising two ids, and it answers the account too, so a
// token without Account · Read is enough.
func TestAZoneNamesItsAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zones" || r.URL.Query().Get("name") != "acornd.app" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"z1","name":"acornd.app","account":{"id":"a1"}}]}`))
	}))
	defer server.Close()
	zone, err := (&Client{Token: "t", Base: server.URL}).ZoneByName(context.Background(), "acornd.app")
	if err != nil {
		t.Fatalf("zone: %v", err)
	}
	if zone.ID != "z1" || zone.Account != "a1" {
		t.Errorf("zone = %+v, want its id and its account", zone)
	}
	if _, err := (&Client{Token: "t", Base: server.URL}).ZoneByName(context.Background(), "nope.example"); err == nil {
		t.Error("a zone the token cannot see was accepted")
	}
}

// The setup screen's first question, answered by the account: every zone this token can see, each with the account
// it lives in, because that is where the tunnel will be made (ADR 0036 §6).
func TestTheZonesAreWhatTheTokenCanSee(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zones" || r.URL.Query().Get("per_page") != "50" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":[` +
			`{"id":"z1","name":"acornd.app","account":{"id":"a1"}},` +
			`{"id":"z2","name":"example.com","account":{"id":"a2"}}]}`))
	}))
	defer server.Close()
	zones, err := (&Client{Token: "t", Base: server.URL}).Zones(context.Background())
	if err != nil {
		t.Fatalf("zones: %v", err)
	}
	want := []Zone{{ID: "z1", Name: "acornd.app", Account: "a1"}, {ID: "z2", Name: "example.com", Account: "a2"}}
	if !slices.Equal(zones, want) {
		t.Errorf("zones = %+v, want %+v", zones, want)
	}
}

// A token that cannot list zones is refused in the client's own words, naming the permission the screen has to tell
// the person to add (ADR 0033 §2).
func TestTheZonesCallNamesThePermissionItNeeded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Unauthorized to access requested resource"}]}`))
	}))
	defer server.Close()
	_, err := (&Client{Token: "t", Base: server.URL}).Zones(context.Background())
	if err == nil {
		t.Fatal("a refused call reported success")
	}
	if !strings.Contains(err.Error(), string(scopeDNS)) {
		t.Errorf("the refusal does not name the scope: %v", err)
	}
}

// The account's own login domain, read from the endpoint that has always held it: the apply no longer asks a
// person for a value Cloudflare already knows.
func TestTheAccountNamesItsTeamDomain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/acc/access/organizations" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":{"auth_domain":"acornd.cloudflareaccess.com"}}`))
	}))
	defer server.Close()
	domain, err := (&Client{Token: "t", Base: server.URL}).AccessOrganization(context.Background(), "acc")
	if err != nil {
		t.Fatalf("organization: %v", err)
	}
	if domain != "acornd.cloudflareaccess.com" {
		t.Errorf("domain = %q", domain)
	}
}

// A group is a list of include rules, one per address: what is read back is what was written.
func TestAGroupIsAListOfAddresses(t *testing.T) {
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&posted)
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"g1","name":"acornd","include":[{"email":{"email":"ana@example.com"}},{"email":{"email":"bo@example.com"}}]}}`))
	}))
	defer server.Close()
	group, err := (&Client{Token: "t", Base: server.URL}).CreateAccessGroup(context.Background(), "acc", GroupName, []string{"ana@example.com", "bo@example.com"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !reflect.DeepEqual(group.Emails, []string{"ana@example.com", "bo@example.com"}) {
		t.Errorf("group = %+v", group)
	}
	include, _ := posted["include"].([]any)
	if len(include) != 2 {
		t.Errorf("posted %v, want one rule per address", posted)
	}
}

// The application is the door's own settings: a session of a month, which is Access's ceiling, and the one-time
// PIN as the only identity provider — so a browser is asked for an address rather than offered a picker. A
// browser off the policy is still refused, which is what the policy is for (ADR 0036 §6).
func TestTheAccessAppIsAMonthAndThePINIsTheOnlyWayIn(t *testing.T) {
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&posted)
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"a1","name":"acornd","domain":"console.example.com","aud":"aud-1"}}`))
	}))
	defer server.Close()
	if _, err := (&Client{Token: "t", Base: server.URL}).CreateAccessApp(context.Background(), "acc", AppName, "console.example.com", "idp-1"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if posted["session_duration"] != "720h" {
		t.Errorf("session_duration = %v, want 720h (a month is written as hours; the API refused \"1mo\")", posted["session_duration"])
	}
	if posted["auto_redirect_to_identity"] != true {
		t.Errorf("auto_redirect_to_identity = %v, want true", posted["auto_redirect_to_identity"])
	}
	if got, _ := posted["allowed_idps"].([]any); !reflect.DeepEqual(got, []any{"idp-1"}) {
		t.Errorf("allowed_idps = %v, want only the one-time PIN", posted["allowed_idps"])
	}
}

// The setup file is the record: it holds the tunnel's token, so it is 0600, and it is written whole so a crash
// leaves either the old state or the new one.
func TestTheSetupIsAFileWithItsToken(t *testing.T) {
	dir := t.TempDir()
	setup := Setup{
		Desired:  Desired{Hostname: "testing.acornd.app", ZoneID: "z", AccountID: "a"},
		State:    State{TunnelID: "t", TunnelToken: "secret", AppID: "app", Audience: "aud", TeamDomain: "team.cloudflareaccess.com"},
		APIToken: "api-token",
	}
	if err := Save(dir, setup, time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, File))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600: the file holds a credential", mode)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.State.TunnelToken != "secret" || loaded.Desired.Hostname != "testing.acornd.app" || loaded.APIToken != "api-token" {
		t.Errorf("loaded = %+v", loaded)
	}
	if loaded.State.TeamDomain != "team.cloudflareaccess.com" {
		t.Errorf("the team domain did not survive the file: %+v", loaded.State)
	}
	if payload, err := os.ReadFile(filepath.Join(dir, File)); err != nil || !strings.Contains(string(payload), `"team_domain"`) {
		t.Errorf("the file does not name the team domain it holds (%v): %s", err, payload)
	}
	if loaded.Updated != "2026-10-30T12:00:00Z" {
		t.Errorf("updated = %q", loaded.Updated)
	}
	if !loaded.State.Ready() {
		t.Error("a setup with a tunnel, an app and an audience is not ready")
	}
}

// An install nobody configured has no file, and that is not an error: it is the state before the first apply.
func TestAnUnconfiguredInstallHasNoSetup(t *testing.T) {
	setup, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if setup.Configured() {
		t.Error("an install with no file reports a configuration")
	}
}

// A file that cannot be read is reported rather than treated as empty: an install whose record vanished would
// otherwise make a second tunnel.
func TestASetupThatCannotBeReadIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, File), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("an unreadable setup read as empty")
	}
}

// The four values the door needs, derived from the audience and the team's domain, so nobody has to remember
// what the certs URL is. The scheme is part of the issuer: a claim is compared against it byte for byte, and a
// bare host there is a door that refuses every assertion the edge signs (ADR 0036 §5).
func TestTheSetupNamesTheIdentityConfiguration(t *testing.T) {
	setup := Setup{State: State{Audience: "aud-1", TeamDomain: "team.cloudflareaccess.com/"}}
	env := setup.Env()
	for name, want := range map[string]string{
		"ACORND_IDENTITY":          "access",
		"ACORND_IDENTITY_AUDIENCE": "aud-1",
		"ACORND_IDENTITY_ISSUER":   "https://team.cloudflareaccess.com",
		"ACORND_IDENTITY_KEYS":     "https://team.cloudflareaccess.com/cdn-cgi/access/certs",
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q", name, env[name], want)
		}
	}
}

// A half-finished apply has no team domain, so there is no issuer to compare and no certs URL to fetch. The
// values are empty rather than the strings "https://" and "https:///cdn-cgi/access/certs", which would be a
// door built out of nothing.
func TestASetupWithNoTeamDomainNamesNoIssuer(t *testing.T) {
	env := (Setup{State: State{Audience: "aud-1"}}).Env()
	if env["ACORND_IDENTITY_AUDIENCE"] != "aud-1" {
		t.Errorf("the audience was dropped: %q", env["ACORND_IDENTITY_AUDIENCE"])
	}
	for _, name := range []string{"ACORND_IDENTITY_ISSUER", "ACORND_IDENTITY_KEYS"} {
		if env[name] != "" {
			t.Errorf("%s = %q, want empty while there is no team domain", name, env[name])
		}
	}
}

// A tunnel that exists and goes nowhere is a door into a wall: the route is written on the first apply, left
// alone when it already points at this console, and rewritten when the console moves.
func TestTheTunnelIsPointedAtThisConsole(t *testing.T) {
	api := newFake()
	provisioner := &Provisioner{API: api}
	state, steps, err := provisioner.Apply(context.Background(), desired(), State{}, []string{"ana@example.com"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if api.ingress != DefaultLocal {
		t.Errorf("the tunnel points at %q, want %q", api.ingress, DefaultLocal)
	}
	found := false
	for _, step := range steps {
		if step.Name == "ingress" {
			found = step.Created && step.Detail == DefaultLocal
		}
	}
	if !found {
		t.Errorf("the route was not reported: %+v", steps)
	}

	// Pointed somewhere else, the next apply moves it.
	desire := desired()
	desire.Local = "http://127.0.0.1:9000"
	if _, steps, err := provisioner.Apply(context.Background(), desire, state, []string{"ana@example.com"}); err != nil {
		t.Fatalf("second apply: %v", err)
	} else if steps[1].Name != "ingress" || !steps[1].Created {
		t.Errorf("the route was not rewritten: %+v", steps)
	}
	if api.ingress != "http://127.0.0.1:9000" {
		t.Errorf("the tunnel points at %q", api.ingress)
	}
}

// A record whose name is right and whose target is not is the state a deleted or recreated tunnel leaves: the
// tunnel a token cannot be rotated on is replaced by a new one, and the door then points at nothing. The apply
// re-points it rather than reporting the name as done.
func TestARecordPointingAtAnotherTunnelIsRepointed(t *testing.T) {
	api := newFake()
	api.tunnels = []Tunnel{{ID: "t-existing", Name: TunnelName}}
	api.ingress = DefaultLocal
	api.records = []DNSRecord{{ID: "r-existing", Name: "console.example.com", Type: "CNAME", Content: "t-gone.cfargotunnel.com"}}
	prior := State{TunnelID: "t-existing", TunnelToken: "kept", DNSRecordID: "r-existing"}

	state, steps, err := (&Provisioner{API: api}).Apply(context.Background(), desired(), prior, []string{"ana@example.com"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if state.DNSRecordID != "r-existing" {
		t.Errorf("the record was replaced rather than re-pointed: %q", state.DNSRecordID)
	}
	if got := api.records[0].Content; got != "t-existing.cfargotunnel.com" {
		t.Errorf("the record points at %q", got)
	}
	for _, step := range steps {
		if step.Name == "dns" && (!step.Moved || step.Created) {
			t.Errorf("the re-point was reported as %+v, want a move", step)
		}
	}
	calls := strings.Join(api.created(), " ")
	if !strings.Contains(calls, "updateCNAME") {
		t.Errorf("the apply did %v, want the record re-pointed", api.created())
	}
	if strings.Contains(calls, "createCNAME") {
		t.Errorf("the apply made a second record: %v", api.created())
	}
}
