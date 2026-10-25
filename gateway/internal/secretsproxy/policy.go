package secretsproxy

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
)

// Grant is one secret this proxy may substitute. The value never leaves the process except into a
// request for a host the grant names: it is not in a response, a log line, or an error.
type Grant struct {
	// Name is the vault's name for the secret, and the only thing a refusal may say about it.
	Name string
	// Value is the real secret.
	Value string
	// Placeholder is the literal the guest holds. Compared, never printed.
	Placeholder string
	// Hosts are the destinations the value may be sent to: exact names, or a leading "*." as in
	// the vault's own grammar (ADR 0021), which refuses a bare "*" before it can reach here.
	Hosts []string
	// Headers, Query and Body are where the placeholder may be replaced. A placeholder found
	// anywhere else is a refusal rather than a pass: a secret in a URL's query or a body that
	// echoes it is the leak this feature exists to prevent, and forwarding it unchanged would
	// spend the agent's mistake on an untrusted server.
	Headers bool
	Query   bool
	Body    bool
}

// verdict is what one request gets.
type verdict int

const (
	// forward sends the request untouched: no placeholder was in it, or none this workspace has.
	forward verdict = iota
	// substitute rewrites the request in place and sends it.
	substitute
	// refuse answers the client without the upstream being contacted at all.
	refuse
)

// decision is the whole answer for one request, worked out before anything is changed so that a
// refusal leaves the request exactly as it arrived.
type decision struct {
	action verdict
	// reason is one sentence for the client and the log: a name and a host, never a value.
	reason string
	// grants are the secrets this request carried, in the order the vault listed them.
	grants []Grant
}

// decide works out what one request may have done to it.
func decide(grants []Grant, host string, header http.Header, rawQuery string, body []byte) decision {
	var matched []Grant
	for _, grant := range grants {
		if grant.Placeholder == "" {
			continue
		}
		found := findPlaces(grant.Placeholder, header, rawQuery, body)
		if !found.any() {
			continue
		}
		if refusal := found.refusal(grant, host); refusal != "" {
			return decision{action: refuse, reason: refusal}
		}
		matched = append(matched, grant)
	}
	if len(matched) == 0 {
		return decision{action: forward}
	}
	return decision{action: substitute, grants: matched}
}

// places is where one placeholder was found in a request: the three locations a grant can enable.
type places struct {
	header bool
	query  bool
	body   bool
}

func (p places) any() bool { return p.header || p.query || p.body }

// refusal is the first reason this placement may not be substituted, or "" when it may. Where the
// placeholder is and where the grant allows it to be are separate questions, which is why a
// request can be refused on a host the secret does name.
func (p places) refusal(grant Grant, host string) string {
	switch {
	case !allowedHost(grant.Hosts, host):
		return fmt.Sprintf("%s is not allowed to reach %s", grant.Name, host)
	case p.header && !grant.Headers:
		return fmt.Sprintf("%s may not be substituted in a header", grant.Name)
	case p.query && !grant.Query:
		return fmt.Sprintf("%s may not be substituted in a query", grant.Name)
	case p.body && !grant.Body:
		return fmt.Sprintf("%s may not be substituted in a body", grant.Name)
	}
	return ""
}

// findPlaces looks for one placeholder in the locations a request has. The proxy's own credential
// is not one of them (see placeholderInHeader).
func findPlaces(placeholder string, header http.Header, rawQuery string, body []byte) places {
	return places{
		header: placeholderInHeader(header, placeholder),
		query:  strings.Contains(rawQuery, placeholder),
		body:   len(body) > 0 && bytes.Contains(body, []byte(placeholder)),
	}
}

// allowsAnyHost is whether this workspace substitutes anything for a destination at all. A host
// with no grant is tunnelled rather than intercepted, which keeps a client that pins a
// certificate working for everything that is not a secret's destination.
func allowsAnyHost(grants []Grant, host string) bool {
	for _, grant := range grants {
		if allowedHost(grant.Hosts, host) {
			return true
		}
	}
	return false
}

// allowedHost matches a host against a grant's patterns: exact, or a leading "*." meaning the
// suffix and at least one label before it. A bare "*" is refused when the secret is written, and
// it does not match here either, so a mistake upstream fails closed.
func allowedHost(patterns []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSuffix(pattern, "."))
		if suffix, wildcard := strings.CutPrefix(pattern, "*."); wildcard {
			// "*." covers subdomains and the apex is a separate question the vault's own grammar
			// answers: a pattern with no wildcard names one host and nothing else.
			if len(host) > len(suffix)+1 && strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == pattern {
			return true
		}
	}
	return false
}

// placeholderInHeader reports whether any header value carries the placeholder. The proxy's own
// credential is skipped: it is ours, it is checked before this runs, and a token that happened to
// look like a placeholder must not be rewritten or refused.
func placeholderInHeader(header http.Header, placeholder string) bool {
	for name, values := range header {
		if strings.EqualFold(name, "Proxy-Authorization") {
			continue
		}
		for _, value := range values {
			if strings.Contains(value, placeholder) {
				return true
			}
		}
	}
	return false
}

// rewrite replaces every granted placeholder in the locations each grant enables. It is called
// only after decide has accepted the request, so every placeholder here is one that may move.
func rewrite(header http.Header, rawQuery *string, body *[]byte, grants []Grant) {
	for _, grant := range grants {
		if grant.Headers {
			replaceInHeader(header, grant)
		}
		if grant.Query && rawQuery != nil {
			*rawQuery = strings.ReplaceAll(*rawQuery, grant.Placeholder, grant.Value)
		}
		if grant.Body && body != nil {
			*body = bytes.ReplaceAll(*body, []byte(grant.Placeholder), []byte(grant.Value))
		}
	}
}

// replaceInHeader rewrites one grant's placeholder in the headers, skipping the proxy's own
// credential: it is ours, and forwarding it upstream is a different bug.
func replaceInHeader(header http.Header, grant Grant) {
	for name, values := range header {
		if strings.EqualFold(name, "Proxy-Authorization") {
			continue
		}
		for index, value := range values {
			values[index] = strings.ReplaceAll(value, grant.Placeholder, grant.Value)
		}
	}
}
