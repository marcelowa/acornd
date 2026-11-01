// The Cloudflare half: the calls this package needs, and nothing else. Every call names the permission it
// needs, so a refusal says which one is missing rather than handing back "Authentication error".
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to the Cloudflare API with an admin's token (ADR 0033 §2).
type Client struct {
	// Token is the API token. It is used here and at reconcile time, and is offered for deletion afterwards:
	// what the running process holds is the tunnel's own token.
	Token string
	// Base is the API root; empty means the real one, which is what a test overrides.
	Base string
	// HTTP is the client; empty means one with a timeout, because an apply that hangs forever on a network
	// call is an install nobody can finish setting up.
	HTTP *http.Client
}

// Zone is one domain in the account.
type Zone struct {
	ID      string
	Name    string
	Account string
}

// response is the envelope every v4 endpoint answers with.
type response struct {
	Success bool `json:"success"`
	Result  json.RawMessage
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// scope words one permission, in the words of the token screen, so a 403 can name it.
type scope string

const (
	scopeTunnel scope = "Account · Cloudflare Tunnel · Edit"
	scopeDNS    scope = "Zone · DNS · Edit"
	scopeApps   scope = "Account · Access: Apps and Policies · Edit"
	scopeGroups scope = "Account · Access: Organizations, Identity Providers and Groups · Edit"
)

// ZoneByName resolves the zone the hostname belongs to, and the account it lives in. It is how the admin names
// a domain instead of two ids, and it is why the token does not need Account · Read: a zone says which account
// it is in.
func (c *Client) ZoneByName(ctx context.Context, name string) (Zone, error) {
	var found []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
	}
	query := url.Values{"name": {name}, "per_page": {"1"}}
	if err := c.call(ctx, scopeDNS, http.MethodGet, "/zones?"+query.Encode(), nil, &found); err != nil {
		return Zone{}, err
	}
	for _, zone := range found {
		if zone.Name == name {
			return Zone{ID: zone.ID, Name: zone.Name, Account: zone.Account.ID}, nil
		}
	}
	return Zone{}, fmt.Errorf("this token cannot see a zone called %q", name)
}

func (c *Client) Tunnels(ctx context.Context, account string) ([]Tunnel, error) {
	var tunnels []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.call(ctx, scopeTunnel, http.MethodGet, "/accounts/"+account+"/cfd_tunnel?is_deleted=false", nil, &tunnels); err != nil {
		return nil, err
	}
	out := make([]Tunnel, 0, len(tunnels))
	for _, tunnel := range tunnels {
		out = append(out, Tunnel{ID: tunnel.ID, Name: tunnel.Name})
	}
	return out, nil
}

func (c *Client) CreateTunnel(ctx context.Context, account, name string) (Tunnel, error) {
	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	// A remotely-managed tunnel is one whose configuration lives in Cloudflare rather than in a file beside
	// it, which is what lets the ingress be written by this package.
	body := map[string]any{"name": name, "config_src": "cloudflare"}
	if err := c.call(ctx, scopeTunnel, http.MethodPost, "/accounts/"+account+"/cfd_tunnel", body, &created); err != nil {
		return Tunnel{}, err
	}
	return Tunnel{ID: created.ID, Name: created.Name}, nil
}

func (c *Client) TunnelToken(ctx context.Context, account, tunnel string) (string, error) {
	var token string
	if err := c.call(ctx, scopeTunnel, http.MethodGet, "/accounts/"+account+"/cfd_tunnel/"+tunnel+"/token", nil, &token); err != nil {
		return "", err
	}
	return token, nil
}

// TunnelIngress answers the service a remotely-managed tunnel forwards to, or "" when it has no route: a tunnel
// that exists and goes nowhere is the state this step exists to find.
func (c *Client) TunnelIngress(ctx context.Context, account, tunnel string) (string, error) {
	var configuration struct {
		Config *struct {
			Ingress []struct {
				Service string `json:"service"`
			} `json:"ingress"`
		} `json:"config"`
	}
	if err := c.call(ctx, scopeTunnel, http.MethodGet, "/accounts/"+account+"/cfd_tunnel/"+tunnel+"/configurations", nil, &configuration); err != nil {
		return "", err
	}
	if configuration.Config == nil || len(configuration.Config.Ingress) == 0 {
		return "", nil
	}
	return configuration.Config.Ingress[0].Service, nil
}

// SetTunnelIngress writes the route: this console, and a 404 for anything else. The catch-all is required — a
// tunnel whose rules do not end in one is refused by Cloudflare — and it is also the honest answer for a path
// this install does not serve.
func (c *Client) SetTunnelIngress(ctx context.Context, account, tunnel, hostname, service string) error {
	body := map[string]any{"config": map[string]any{"ingress": []any{
		map[string]any{"hostname": hostname, "service": service},
		map[string]any{"service": "http_status:404"},
	}}}
	return c.call(ctx, scopeTunnel, http.MethodPut, "/accounts/"+account+"/cfd_tunnel/"+tunnel+"/configurations", body, nil)
}

// UpdateCNAME points a record at a tunnel, keeping its proxied flag: a record that stops being proxied stops
// being Access's, and the door vanishes with it.
func (c *Client) UpdateCNAME(ctx context.Context, zone string, record DNSRecord, target string) (DNSRecord, error) {
	body := map[string]any{"type": "CNAME", "name": record.Name, "content": target, "proxied": true}
	var out struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	if err := c.call(ctx, scopeDNS, http.MethodPut, "/zones/"+zone+"/dns_records/"+record.ID, body, &out); err != nil {
		return DNSRecord{}, err
	}
	return DNSRecord{ID: out.ID, Name: out.Name, Type: out.Type, Content: out.Content}, nil
}

func (c *Client) DNSRecords(ctx context.Context, zone, hostname string) ([]DNSRecord, error) {
	var records []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	query := url.Values{"name": {hostname}}
	if err := c.call(ctx, scopeDNS, http.MethodGet, "/zones/"+zone+"/dns_records?"+query.Encode(), nil, &records); err != nil {
		return nil, err
	}
	out := make([]DNSRecord, 0, len(records))
	for _, record := range records {
		out = append(out, DNSRecord{ID: record.ID, Name: record.Name, Type: record.Type, Content: record.Content})
	}
	return out, nil
}

func (c *Client) CreateCNAME(ctx context.Context, zone, hostname, target string) (DNSRecord, error) {
	var created struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	// Proxied, always: a record that is not proxied points at the tunnel's address in public DNS and skips
	// Access entirely, which would make the door a decoration.
	body := map[string]any{"type": "CNAME", "name": hostname, "content": target, "proxied": true, "ttl": 1}
	if err := c.call(ctx, scopeDNS, http.MethodPost, "/zones/"+zone+"/dns_records", body, &created); err != nil {
		return DNSRecord{}, err
	}
	return DNSRecord{ID: created.ID, Name: created.Name, Type: created.Type, Content: created.Content}, nil
}

func (c *Client) IdentityProviders(ctx context.Context, account string) ([]IdentityProvider, error) {
	var providers []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := c.call(ctx, scopeGroups, http.MethodGet, "/accounts/"+account+"/access/identity_providers", nil, &providers); err != nil {
		return nil, err
	}
	out := make([]IdentityProvider, 0, len(providers))
	for _, provider := range providers {
		out = append(out, IdentityProvider{ID: provider.ID, Type: provider.Type})
	}
	return out, nil
}

func (c *Client) CreateOneTimePIN(ctx context.Context, account string) (IdentityProvider, error) {
	var created struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	body := map[string]any{"name": "One-time PIN", "type": "onetimepin", "config": map[string]any{}}
	if err := c.call(ctx, scopeGroups, http.MethodPost, "/accounts/"+account+"/access/identity_providers", body, &created); err != nil {
		return IdentityProvider{}, err
	}
	return IdentityProvider{ID: created.ID, Type: created.Type}, nil
}

// AccessOrganization answers the account's team domain, which is the host every assertion from this edge is
// issued by and the one the keys live under. It needs the Access scope that already covers organizations: the
// same permission the group and the provider use, because it is the same setting (ADR 0036 §5).
func (c *Client) AccessOrganization(ctx context.Context, account string) (string, error) {
	var organization struct {
		AuthDomain string `json:"auth_domain"`
	}
	if err := c.call(ctx, scopeGroups, http.MethodGet, "/accounts/"+account+"/access/organizations", nil, &organization); err != nil {
		return "", err
	}
	return organization.AuthDomain, nil
}

func (c *Client) AccessGroups(ctx context.Context, account string) ([]Group, error) {
	var groups []groupPayload
	if err := c.call(ctx, scopeGroups, http.MethodGet, "/accounts/"+account+"/access/groups", nil, &groups); err != nil {
		return nil, err
	}
	out := make([]Group, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.group())
	}
	return out, nil
}

func (c *Client) CreateAccessGroup(ctx context.Context, account, name string, emails []string) (Group, error) {
	var created groupPayload
	if err := c.call(ctx, scopeGroups, http.MethodPost, "/accounts/"+account+"/access/groups", groupBody(name, emails), &created); err != nil {
		return Group{}, err
	}
	return created.group(), nil
}

func (c *Client) UpdateAccessGroup(ctx context.Context, account, id, name string, emails []string) (Group, error) {
	var updated groupPayload
	if err := c.call(ctx, scopeGroups, http.MethodPut, "/accounts/"+account+"/access/groups/"+id, groupBody(name, emails), &updated); err != nil {
		return Group{}, err
	}
	return updated.group(), nil
}

// groupPayload is an Access group as the API carries it: a list of include rules, one per address.
type groupPayload struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Include []struct {
		Email struct {
			Email string `json:"email"`
		} `json:"email"`
	} `json:"include"`
}

func (g groupPayload) group() Group {
	emails := make([]string, 0, len(g.Include))
	for _, include := range g.Include {
		if include.Email.Email != "" {
			emails = append(emails, include.Email.Email)
		}
	}
	return Group{ID: g.ID, Name: g.Name, Emails: emails}
}

func groupBody(name string, emails []string) map[string]any {
	include := make([]any, 0, len(emails))
	for _, email := range emails {
		include = append(include, map[string]any{"email": map[string]any{"email": email}})
	}
	return map[string]any{"name": name, "include": include}
}

func (c *Client) AccessApps(ctx context.Context, account string) ([]App, error) {
	var apps []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Domain   string `json:"domain"`
		Audience string `json:"aud"`
	}
	if err := c.call(ctx, scopeApps, http.MethodGet, "/accounts/"+account+"/access/apps", nil, &apps); err != nil {
		return nil, err
	}
	out := make([]App, 0, len(apps))
	for _, app := range apps {
		out = append(out, App{ID: app.ID, Name: app.Name, Domain: app.Domain, Audience: app.Audience})
	}
	return out, nil
}

func (c *Client) CreateAccessApp(ctx context.Context, account, name, hostname, idp string) (App, error) {
	var created struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Domain   string `json:"domain"`
		Audience string `json:"aud"`
	}
	body := map[string]any{
		"name":   name,
		"domain": hostname,
		"type":   "self_hosted",
		// A month is Access's ceiling: both the application and the account cap a session there, so a year is
		// not a value the platform accepts and the family re-enters an emailed code monthly (ADR 0036 §6).
		"session_duration": "1mo",
		// The one-time PIN is the only identity provider on this application, so the page asks for an address
		// rather than offering a picker in which the other entries admit nobody.
		"auto_redirect_to_identity": true,
		"allowed_idps":              []string{idp},
		// A browser that is not on a policy is refused by default: the policy below is the only way in.
	}
	if err := c.call(ctx, scopeApps, http.MethodPost, "/accounts/"+account+"/access/apps", body, &created); err != nil {
		return App{}, err
	}
	return App{ID: created.ID, Name: created.Name, Domain: created.Domain, Audience: created.Audience}, nil
}

func (c *Client) AccessPolicies(ctx context.Context, account, app string) ([]Policy, error) {
	var policies []policyPayload
	if err := c.call(ctx, scopeApps, http.MethodGet, "/accounts/"+account+"/access/apps/"+app+"/policies", nil, &policies); err != nil {
		return nil, err
	}
	out := make([]Policy, 0, len(policies))
	for _, policy := range policies {
		out = append(out, policy.policy())
	}
	return out, nil
}

func (c *Client) CreateAccessPolicy(ctx context.Context, account, app, group string) (Policy, error) {
	var created policyPayload
	body := map[string]any{
		"name":     "the install's people",
		"decision": "allow",
		"include":  []any{map[string]any{"group": map[string]any{"id": group}}},
	}
	if err := c.call(ctx, scopeApps, http.MethodPost, "/accounts/"+account+"/access/apps/"+app+"/policies", body, &created); err != nil {
		return Policy{}, err
	}
	return created.policy(), nil
}

type policyPayload struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Include []struct {
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	} `json:"include"`
}

func (p policyPayload) policy() Policy {
	policy := Policy{ID: p.ID, Name: p.Name}
	for _, include := range p.Include {
		if include.Group.ID != "" {
			policy.Group = include.Group.ID
		}
	}
	return policy
}

// call is every request: one envelope, one refusal, and the scope named when the token is what is wrong.
func (c *Client) call(ctx context.Context, needs scope, method, path string, body any, out any) error {
	raw, status, err := c.send(ctx, method, path, body)
	if err != nil {
		return err
	}
	return answer(method, path, status, needs, raw, out)
}

// send is the request and its bytes: anything that can go wrong before the answer is read.
func (c *Client) send(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	base := c.Base
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, base+path, payload)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	httpResponse, err := client.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, 1<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return raw, httpResponse.StatusCode, nil
}

// answer is the envelope, the refusal, and the value inside it.
func answer(method, path string, status int, needs scope, raw []byte, out any) error {
	var envelope response
	decodeErr := json.Unmarshal(raw, &envelope)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		// 403 here is almost always the token rather than the account: name the permission the call needed,
		// because "Authentication error" is what Cloudflare says either way.
		return fmt.Errorf("the call %s %s was refused: it needs a token with %q, and this one does not have it%s",
			method, path, needs, reason(envelope))
	case status >= 300 || (decodeErr == nil && !envelope.Success):
		return fmt.Errorf("%s %s answered %d%s", method, path, status, reason(envelope))
	case decodeErr != nil:
		return fmt.Errorf("%s %s: %w", method, path, decodeErr)
	}
	if out == nil || len(envelope.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("%s %s: reading the answer: %w", method, path, err)
	}
	return nil
}

// reason is Cloudflare's own words, when it had any.
func reason(envelope response) string {
	if len(envelope.Errors) == 0 {
		return ""
	}
	parts := make([]string, 0, len(envelope.Errors))
	for _, e := range envelope.Errors {
		parts = append(parts, fmt.Sprintf("%s (%d)", e.Message, e.Code))
	}
	return ": " + join(parts, "; ")
}

func join(parts []string, sep string) string {
	// strings.Join, not a builder of our own: the standard library's version does the same thing with one
	// allocation, and a hand-written one here was only ever a slower spelling of it.
	return strings.Join(parts, sep)
}

// ErrNoToken is a run that has nowhere to get a token from.
var ErrNoToken = errors.New("remote access needs a Cloudflare API token: set ACORND_CLOUDFLARE_TOKEN or paste one in the console")
