// Package remote provisions what carries a browser to this console: a Cloudflare Tunnel, the DNS record
// in front of it, and the Access application that decides who gets through. See
// docs/adr/0033-remote-access.md.
//
// The shape is declared state and a reconciler, not a wizard. The owner says which hostname the console
// answers on; everything else is derived from that, from the people file, and from what the Cloudflare
// account already has. Every id the apply writes down is kept, so a second run is a no-op and a run that
// died halfway resumes instead of starting over.
package remote

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Names this package creates. They are stable strings rather than generated ones, because a reconcile that
// cannot find what it made last time makes a second one.
const (
	TunnelName = "acornfall"
	GroupName  = "acornfall"
	AppName    = "acornfall"
	DSPrefix   = "acornfall-"
)

// Desired is what the owner asked for: the hostname the console answers on, the zone it belongs to, and the
// account all of it lives in.
type Desired struct {
	Hostname  string `json:"hostname"`
	ZoneID    string `json:"zone_id"`
	AccountID string `json:"account_id"`
}

// Validate is the refusal a setup screen shows before anything is created.
func (d Desired) Validate() error {
	host := strings.TrimSpace(d.Hostname)
	switch {
	case host == "":
		return errors.New("remote access needs a hostname, such as acornfall.example.com")
	case !strings.Contains(host, "."):
		return fmt.Errorf("%q is not a hostname", d.Hostname)
	case d.ZoneID == "":
		return errors.New("remote access needs the zone the hostname belongs to")
	case d.AccountID == "":
		return errors.New("remote access needs the account the tunnel lives in")
	}
	return nil
}

// State is what exists, as the apply left it. Every field is an id or a value the next run needs, so that a
// second apply finds instead of creating.
//
// TunnelToken is a credential and the file it lives in is 0600: it can run one tunnel and nothing else, which
// is why it is the one the supervised process holds rather than the API token that made it (ADR 0033 §4).
type State struct {
	Hostname    string `json:"hostname,omitempty"`
	ZoneID      string `json:"zone_id,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	TunnelID    string `json:"tunnel_id,omitempty"`
	TunnelToken string `json:"tunnel_token,omitempty"`
	DNSRecordID string `json:"dns_record_id,omitempty"`
	IdentityID  string `json:"identity_provider_id,omitempty"`
	GroupID     string `json:"group_id,omitempty"`
	AppID       string `json:"app_id,omitempty"`
	// Audience is the Access application's tag. It is what an assertion is issued *for*, so the verifier
	// refuses an assertion minted for anything else at the same edge (ADR 0032 §2).
	Audience string `json:"audience,omitempty"`
	PolicyID string `json:"policy_id,omitempty"`
}

// Ready reports whether everything the door needs is recorded.
func (s State) Ready() bool {
	return s.TunnelID != "" && s.TunnelToken != "" && s.AppID != "" && s.Audience != ""
}

// Step is one thing an apply did, in the order it did it, so the console can show a line per step rather
// than a spinner and a hope.
type Step struct {
	Name    string `json:"name"`
	Created bool   `json:"created"`
	Detail  string `json:"detail,omitempty"`
}

// API is the part of Cloudflare this package uses, and nothing else. It is an interface so the plan can be
// tested without an account: every rule in here is a decision about order and identity, and none of it needs
// the network to be wrong.
type API interface {
	Tunnels(ctx context.Context, account string) ([]Tunnel, error)
	CreateTunnel(ctx context.Context, account, name string) (Tunnel, error)
	TunnelToken(ctx context.Context, account, tunnel string) (string, error)
	DNSRecords(ctx context.Context, zone, hostname string) ([]DNSRecord, error)
	CreateCNAME(ctx context.Context, zone, hostname, target string) (DNSRecord, error)
	IdentityProviders(ctx context.Context, account string) ([]IdentityProvider, error)
	CreateOneTimePIN(ctx context.Context, account string) (IdentityProvider, error)
	AccessGroups(ctx context.Context, account string) ([]Group, error)
	CreateAccessGroup(ctx context.Context, account, name string, emails []string) (Group, error)
	UpdateAccessGroup(ctx context.Context, account, id, name string, emails []string) (Group, error)
	AccessApps(ctx context.Context, account string) ([]App, error)
	CreateAccessApp(ctx context.Context, account, name, hostname string) (App, error)
	AccessPolicies(ctx context.Context, account, app string) ([]Policy, error)
	CreateAccessPolicy(ctx context.Context, account, app, group string) (Policy, error)
}

// Tunnel is one Cloudflare Tunnel.
type Tunnel struct {
	ID   string
	Name string
}

// DNSRecord is one record in the zone.
type DNSRecord struct {
	ID      string
	Name    string
	Type    string
	Content string
}

// IdentityProvider is one way Access lets somebody sign in.
type IdentityProvider struct {
	ID   string
	Type string
}

// Group is a named list of addresses that an Access policy can allow.
type Group struct {
	ID     string
	Name   string
	Emails []string
}

// App is a self-hosted Access application: the door in front of one hostname.
type App struct {
	ID       string
	Name     string
	Domain   string
	Audience string
}

// Policy is who an Access application lets in.
type Policy struct {
	ID    string
	Name  string
	Group string
}

// Provisioner applies a Desired on top of a State.
type Provisioner struct {
	API API
}

// Apply makes Cloudflare match the desire, starting from what a previous run recorded, and answers the state
// to write down plus one line per step.
//
// Every step finds before it creates. That is what makes the second run a no-op, and it is also what makes
// adopting an account somebody already configured by hand safe: a tunnel, an application or a group with the
// same name is used rather than duplicated.
func (p *Provisioner) Apply(ctx context.Context, desired Desired, prior State, people []string) (State, []Step, error) {
	if err := desired.Validate(); err != nil {
		return prior, nil, err
	}
	state := prior
	state.Hostname, state.ZoneID, state.AccountID = desired.Hostname, desired.ZoneID, desired.AccountID
	steps := make([]Step, 0, 6)

	tunnel, step, err := p.tunnel(ctx, desired, prior)
	if err != nil {
		return state, steps, err
	}
	steps = append(steps, step)
	state.TunnelID = tunnel.ID
	state.TunnelToken, err = p.token(ctx, desired, prior, tunnel, step.Created)
	if err != nil {
		return state, steps, err
	}

	record, step, err := p.record(ctx, desired, prior, tunnel.ID)
	if err != nil {
		return state, steps, err
	}
	steps = append(steps, step)
	state.DNSRecordID = record.ID

	identity, step, err := p.identity(ctx, desired)
	if err != nil {
		return state, steps, err
	}
	steps = append(steps, step)
	state.IdentityID = identity.ID

	group, step, err := p.group(ctx, desired, prior, people)
	if err != nil {
		return state, steps, err
	}
	steps = append(steps, step)
	state.GroupID = group.ID

	app, step, err := p.app(ctx, desired, prior)
	if err != nil {
		return state, steps, err
	}
	steps = append(steps, step)
	state.AppID, state.Audience = app.ID, app.Audience

	policy, step, err := p.policy(ctx, desired, prior, app.ID, group.ID)
	if err != nil {
		return state, steps, err
	}
	steps = append(steps, step)
	state.PolicyID = policy.ID

	return state, steps, nil
}

func (p *Provisioner) tunnel(ctx context.Context, desired Desired, prior State) (Tunnel, Step, error) {
	tunnels, err := p.API.Tunnels(ctx, desired.AccountID)
	if err != nil {
		return Tunnel{}, Step{}, fmt.Errorf("listing tunnels: %w", err)
	}
	if found, ok := pickTunnel(tunnels, prior.TunnelID, TunnelName); ok {
		return found, Step{Name: "tunnel", Detail: found.Name}, nil
	}
	created, err := p.API.CreateTunnel(ctx, desired.AccountID, TunnelName)
	if err != nil {
		return Tunnel{}, Step{}, fmt.Errorf("creating the tunnel: %w", err)
	}
	return created, Step{Name: "tunnel", Created: true, Detail: created.Name}, nil
}

// token is the tunnel's own credential: read when this apply made the tunnel, and otherwise carried over from
// the state the last run wrote. It can run one tunnel and nothing else, which is why it is what the supervised
// process holds rather than the API token that made it (ADR 0033 §4).
func (p *Provisioner) token(ctx context.Context, desired Desired, prior State, tunnel Tunnel, created bool) (string, error) {
	if prior.TunnelToken != "" && !created && prior.TunnelID == tunnel.ID {
		return prior.TunnelToken, nil
	}
	token, err := p.API.TunnelToken(ctx, desired.AccountID, tunnel.ID)
	if err != nil {
		return "", fmt.Errorf("reading the tunnel's token: %w", err)
	}
	return token, nil
}

func (p *Provisioner) record(ctx context.Context, desired Desired, prior State, tunnelID string) (DNSRecord, Step, error) {
	records, err := p.API.DNSRecords(ctx, desired.ZoneID, desired.Hostname)
	if err != nil {
		return DNSRecord{}, Step{}, fmt.Errorf("listing dns records: %w", err)
	}
	for _, record := range records {
		if record.ID == prior.DNSRecordID || strings.EqualFold(record.Name, desired.Hostname) {
			return record, Step{Name: "dns", Detail: record.Name}, nil
		}
	}
	created, err := p.API.CreateCNAME(ctx, desired.ZoneID, desired.Hostname, tunnelID+".cfargotunnel.com")
	if err != nil {
		return DNSRecord{}, Step{}, fmt.Errorf("creating the dns record: %w", err)
	}
	return created, Step{Name: "dns", Created: true, Detail: created.Name}, nil
}

func (p *Provisioner) identity(ctx context.Context, desired Desired) (IdentityProvider, Step, error) {
	providers, err := p.API.IdentityProviders(ctx, desired.AccountID)
	if err != nil {
		return IdentityProvider{}, Step{}, fmt.Errorf("listing identity providers: %w", err)
	}
	for _, provider := range providers {
		if provider.Type == "onetimepin" {
			return provider, Step{Name: "one-time pin", Detail: "already in the account"}, nil
		}
	}
	created, err := p.API.CreateOneTimePIN(ctx, desired.AccountID)
	if err != nil {
		return IdentityProvider{}, Step{}, fmt.Errorf("adding the one-time pin provider: %w", err)
	}
	return created, Step{Name: "one-time pin", Created: true}, nil
}

// group is the one step that is rewritten rather than created once: the addresses the door admits are the
// owner's list of people, so the group follows the book on every apply (ADR 0033 §3).
func (p *Provisioner) group(ctx context.Context, desired Desired, prior State, people []string) (Group, Step, error) {
	emails := normalise(people)
	groups, err := p.API.AccessGroups(ctx, desired.AccountID)
	if err != nil {
		return Group{}, Step{}, fmt.Errorf("listing access groups: %w", err)
	}
	for _, group := range groups {
		if group.ID != prior.GroupID && group.Name != GroupName {
			continue
		}
		updated, err := p.API.UpdateAccessGroup(ctx, desired.AccountID, group.ID, GroupName, emails)
		if err != nil {
			return Group{}, Step{}, fmt.Errorf("updating the access group: %w", err)
		}
		return updated, Step{Name: "access group", Detail: fmt.Sprintf("%d addresses", len(emails))}, nil
	}
	created, err := p.API.CreateAccessGroup(ctx, desired.AccountID, GroupName, emails)
	if err != nil {
		return Group{}, Step{}, fmt.Errorf("creating the access group: %w", err)
	}
	return created, Step{Name: "access group", Created: true, Detail: fmt.Sprintf("%d addresses", len(emails))}, nil
}

func (p *Provisioner) app(ctx context.Context, desired Desired, prior State) (App, Step, error) {
	apps, err := p.API.AccessApps(ctx, desired.AccountID)
	if err != nil {
		return App{}, Step{}, fmt.Errorf("listing access applications: %w", err)
	}
	for _, app := range apps {
		if app.ID == prior.AppID || strings.EqualFold(app.Domain, desired.Hostname) {
			return app, Step{Name: "access app", Detail: app.Domain}, nil
		}
	}
	created, err := p.API.CreateAccessApp(ctx, desired.AccountID, AppName, desired.Hostname)
	if err != nil {
		return App{}, Step{}, fmt.Errorf("creating the access application: %w", err)
	}
	return created, Step{Name: "access app", Created: true, Detail: created.Domain}, nil
}

func (p *Provisioner) policy(ctx context.Context, desired Desired, prior State, app, group string) (Policy, Step, error) {
	policies, err := p.API.AccessPolicies(ctx, desired.AccountID, app)
	if err != nil {
		return Policy{}, Step{}, fmt.Errorf("listing access policies: %w", err)
	}
	for _, policy := range policies {
		if policy.ID == prior.PolicyID || policy.Group == group {
			return policy, Step{Name: "access policy", Detail: "the group may come in"}, nil
		}
	}
	created, err := p.API.CreateAccessPolicy(ctx, desired.AccountID, app, group)
	if err != nil {
		return Policy{}, Step{}, fmt.Errorf("creating the access policy: %w", err)
	}
	return created, Step{Name: "access policy", Created: true, Detail: "the group may come in"}, nil
}

// pickTunnel is the one place that decides which tunnel is ours: the id a previous run wrote, and otherwise
// the one named acornfall. A tunnel somebody made for something else is never adopted, whatever its name.
func pickTunnel(tunnels []Tunnel, id, name string) (Tunnel, bool) {
	for _, tunnel := range tunnels {
		if id != "" && tunnel.ID == id {
			return tunnel, true
		}
	}
	for _, tunnel := range tunnels {
		if tunnel.Name == name {
			return tunnel, true
		}
	}
	return Tunnel{}, false
}

// normalise is the address list as Access wants it: lowercase, trimmed, sorted, and without repeats, so two
// runs over the same book write the same group and nothing shows as a change that is not one.
func normalise(people []string) []string {
	seen := make(map[string]bool, len(people))
	out := make([]string, 0, len(people))
	for _, person := range people {
		address := strings.ToLower(strings.TrimSpace(person))
		if address == "" || seen[address] {
			continue
		}
		seen[address] = true
		out = append(out, address)
	}
	sort.Strings(out)
	return out
}
