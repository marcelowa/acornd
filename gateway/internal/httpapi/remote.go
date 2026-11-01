// The remote-access screen's calls: what this install recorded, what a pasted token can see, the apply that makes
// Cloudflare match it, and the one press that turns the door on.
//
// They exist so the first contact with the door does not need a terminal: the admin pastes the API token, chooses a
// zone the token can see, types a hostname, and the console does what `acornd remote apply` does. The token is used
// for the call and dropped — it is the most powerful credential in the install and ADR 0033 §4 says the running
// process does not keep it. The apply stores it and nothing else here does.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/remote"
)

// remoteAPI is how the handler reaches Cloudflare. A variable so a test can answer without an account.
var remoteAPI = func(token string) remote.API { return &remote.Client{Token: token} }

// doorCheck asks the public hostname whether the door is up. A variable so a test can read the issuer it was given
// without opening a socket, the same way remoteAPI lets one answer an apply.
var doorCheck = remote.CheckDoor

// remoteApplyBudget is how long one press waits on Cloudflare. Longer than a boot's patience, shorter than the
// point where somebody presses the button again.
const remoteApplyBudget = 120 * time.Second

// remotePreviewBudget is how long the screen waits to hear what a token can see: two reads and no create, so a
// token that needs longer than this is one nobody would want to paste.
const remotePreviewBudget = 30 * time.Second

// remoteRestartGrace is how long an enable's answer has to leave before this process becomes itself again. The
// same half second the update screen gives its own answer, for the same reason: a body written after the exec
// never arrives (ADR 0036 §6).
const remoteRestartGrace = 500 * time.Millisecond

// peopleReconcileBudget is how long the door may take to follow a change in the book. It runs beside the write,
// so a slow Cloudflare delays nothing the person is looking at.
const peopleReconcileBudget = 30 * time.Second

type remoteStep struct {
	Name    string `json:"name"`
	Created bool   `json:"created"`
	Moved   bool   `json:"moved,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type remoteState struct {
	Configured bool `json:"configured"`
	// TokenStored says the API token is on this machine, so the door follows the book without anybody running a
	// command. The token itself never crosses this boundary.
	TokenStored bool   `json:"tokenStored"`
	Hostname    string `json:"hostname,omitempty"`
	ZoneID      string `json:"zoneId,omitempty"`
	AccountID   string `json:"accountId,omitempty"`
	// TeamDomain is the account's login domain, which the door's issuer and keys URL are derived from. The
	// screen shows it beside the audience for the same reason: the two values are what a browser is checked
	// against (ADR 0036 §5).
	TeamDomain string `json:"teamDomain,omitempty"`
	Audience   string `json:"audience,omitempty"`
	Updated    string `json:"updated,omitempty"`
	TunnelID   string `json:"tunnelId,omitempty"`
	// RestartRequired says the door is recorded and this process has none of it: remote.json was written after the
	// boot that is serving, so the tunnel and the assertion door start at the next restart and not before
	// (ADR 0036 §6). A boot that did build the door says false, because a restart would change nothing.
	RestartRequired bool         `json:"restartRequired"`
	Steps           []remoteStep `json:"steps,omitempty"`
}

// remoteStatus answers what is recorded. Nothing secret: the tunnel's token and the API token never leave the
// install folder and the environment.
func (s *Server) remoteStatus(w http.ResponseWriter, r *http.Request) {
	setup, err := remote.Load(s.gate.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, s.remoteStateFor(setup))
}

// remoteStateFor is what the screen reads: ids and a yes/no about the token, never the token.
func (s *Server) remoteStateFor(setup remote.Setup) remoteState {
	return remoteState{
		Configured:      setup.Configured(),
		TokenStored:     setup.APIToken != "",
		Hostname:        setup.Desired.Hostname,
		ZoneID:          setup.Desired.ZoneID,
		AccountID:       setup.Desired.AccountID,
		TeamDomain:      setup.State.TeamDomain,
		Audience:        setup.State.Audience,
		Updated:         setup.Updated,
		TunnelID:        setup.State.TunnelID,
		RestartRequired: s.restartRequired(setup),
	}
}

// restartRequired is the fact the screen turns into the Turn on button: the file records a door with an audience,
// and this boot built no verifier to check assertions against it, so only an exec can finish the job (ADR 0036 §6).
func (s *Server) restartRequired(setup remote.Setup) bool {
	return setup.Configured() && setup.State.Audience != "" && !s.gate.Publisher
}

// remoteApply makes Cloudflare match what the admin asks for, and answers the steps it took — the same list the
// command prints, in the same words, because it is the same apply.
func (s *Server) remoteApply(w http.ResponseWriter, r *http.Request) {
	request, ok := s.remoteRequest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), remoteApplyBudget)
	defer cancel()
	api := remoteAPI(request.token)
	zone, err := zoneByName(ctx, api, request.zone)
	if err != nil {
		http.Error(w, err.Error(), remoteStatusFor(err))
		return
	}
	desire := remote.Desired{Hostname: request.hostname, ZoneID: zone.ID, AccountID: zone.Account}
	state, steps, applyErr := (&remote.Provisioner{API: api}).Apply(ctx, desire, request.setup.State, request.admitted)
	// The token is stored only when the apply worked: a token that could not make the door is not a token the
	// boot should keep trying. A failed apply keeps whatever was stored before, so a working door is not
	// overwritten by a typo.
	stored := request.setup.APIToken
	if applyErr == nil {
		stored = request.token
	}
	// Written whatever happened: what an apply made before it failed is what a resume reads instead of starting
	// over, and losing it is how a second tunnel appears.
	if saveErr := remote.Save(s.gate.Dir, remote.Setup{Desired: desire, State: state, APIToken: stored}, time.Now()); saveErr != nil {
		http.Error(w, "the apply ran but its ids could not be written: "+saveErr.Error(), http.StatusInternalServerError)
		return
	}
	answer := s.answerFor(desire, state, steps)
	answer.TokenStored = stored != ""
	if applyErr != nil {
		// 502: Cloudflare, or the way to it, is what failed; the steps so far travel with the refusal so the
		// screen can show what did get made.
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": applyErr.Error(), "state": answer})
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// applyRequest is what the handler needs after the body has been read and checked; carrying it keeps the handler
// about order rather than about parsing.
type applyRequest struct {
	hostname string
	zone     string
	token    string
	setup    remote.Setup
	admitted []string
}

// remoteRequest reads the body and the install, and writes the refusal itself when either cannot be used.
func (s *Server) remoteRequest(w http.ResponseWriter, r *http.Request) (applyRequest, bool) {
	var body struct {
		Hostname string `json:"hostname"`
		Zone     string `json:"zone"`
		Token    string `json:"token"`
	}
	if !decode(w, r, &body) {
		return applyRequest{}, false
	}
	request := applyRequest{
		hostname: strings.TrimSpace(body.Hostname),
		zone:     strings.TrimSpace(body.Zone),
		token:    strings.TrimSpace(body.Token),
	}
	if request.hostname == "" || request.zone == "" {
		http.Error(w, "a hostname and the zone it belongs to are both needed", http.StatusBadRequest)
		return applyRequest{}, false
	}
	// A label is completed with the zone before anything is created, and a name in another zone is refused here
	// rather than by the API halfway through an apply (ADR 0036 §6). Desired.Validate is still the last word.
	hostname, err := remote.Hostname(request.hostname, request.zone)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return applyRequest{}, false
	}
	request.hostname = hostname
	// The stored token is the fallback behind the body and the environment: the admin pasted it once, the door
	// follows the book from then on (ADR 0033 §3, §4 as amended).
	setup, err := remote.Load(s.gate.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return applyRequest{}, false
	}
	request.setup = setup
	request.token = cloudflareToken(request.token, setup)
	if request.token == "" {
		http.Error(w, remote.ErrNoToken.Error(), http.StatusBadRequest)
		return applyRequest{}, false
	}
	if s.gate.People == nil {
		http.Error(w, "this install has no book of people yet", http.StatusConflict)
		return applyRequest{}, false
	}
	admitted, err := s.gate.People.Admitted()
	if err != nil {
		http.Error(w, "the book of people could not be read: "+err.Error(), http.StatusInternalServerError)
		return applyRequest{}, false
	}
	request.admitted = admitted
	return request, true
}

// remoteForgetToken removes the stored token. What is at Cloudflare stays; what stops is the door following the
// book until somebody pastes a token again.
func (s *Server) remoteForgetToken(w http.ResponseWriter, r *http.Request) {
	setup, err := remote.Load(s.gate.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	setup.APIToken = ""
	if err := remote.Save(s.gate.Dir, setup, time.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, s.remoteStateFor(setup))
}

// remotePreview answers the screen's first question before anything is stored: what can this token see? The zones
// are the choice the form offers, and the account's team domain is the value the door's issuer and keys URL are
// derived from (ADR 0036 §6). The token is used for the call and written nowhere: storing it is the apply's job,
// because a token that could not make a door is not one the boot should keep trying (ADR 0033 §4).
func (s *Server) remotePreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &body) {
		return
	}
	setup, err := remote.Load(s.gate.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	token := cloudflareToken(body.Token, setup)
	if token == "" {
		http.Error(w, remote.ErrNoToken.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), remotePreviewBudget)
	defer cancel()
	answer, err := preview(ctx, remoteAPI(token))
	if err != nil {
		// 400 rather than 502: the one thing this route was given is the token, so what it can fail at is a
		// sentence about that token — the client's own, naming the permission it is missing.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// remoteEnable is the button an apply ends with: the file now records a door, and this process has to become one
// that builds it. The answer leaves before the exec, so the screen can say "restarting" instead of losing the
// connection mid-sentence (ADR 0036 §6).
func (s *Server) remoteEnable(w http.ResponseWriter, r *http.Request) {
	setup, err := remote.Load(s.gate.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if missing := nothingToTurnOn(setup); missing != "" {
		http.Error(w, missing, http.StatusConflict)
		return
	}
	if s.gate.Publisher {
		// Nothing to do, and saying so is the answer: this boot already checks assertions against the file.
		writeJSON(w, http.StatusOK, map[string]any{"restarting": false})
		return
	}
	if host := strings.TrimSpace(s.gate.RunHost); host != "" && !loopbackHost(host) {
		// An exec keeps the argv the process already has, so a run started on every interface cannot become a
		// door install by restarting: the door would be one address and a hole on the others (ADR 0032 §8).
		http.Error(w, fmt.Sprintf("this console was started with --host %s, and a restart keeps the arguments it already has: stop it and start it again without --host, so it binds loopback and the tunnel carries the browser", host), http.StatusConflict)
		return
	}
	if s.update == nil || s.restart == nil {
		http.Error(w, "this build has no update tier, so it cannot restart itself: stop the console and start it again", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restarting": true})
	go func() {
		time.Sleep(remoteRestartGrace)
		if err := s.restart(); err != nil {
			log.Printf("remote: restarting to open the door failed: %v", err)
		}
	}()
}

// reconcileAfterPeople makes Cloudflare's group match the book after a person is added, revoked or restored.
// It is the same apply the boot runs, at the moment the change happened; it runs beside the write, so a slow
// Cloudflare delays nothing the person is looking at, and it needs a stored token — without one there is nobody
// to ask (ADR 0033 §3).
func (s *Server) reconcileAfterPeople() {
	if s.gate.People == nil {
		return
	}
	setup, err := remote.Load(s.gate.Dir)
	if err != nil || !setup.Configured() || setup.APIToken == "" {
		return
	}
	admitted, err := s.gate.People.Admitted()
	if err != nil {
		log.Printf("tunnel: the door did not follow the book: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), peopleReconcileBudget)
	defer cancel()
	state, _, err := (&remote.Provisioner{API: remoteAPI(setup.APIToken)}).Apply(ctx, setup.Desired, setup.State, admitted)
	if err != nil {
		log.Printf("tunnel: the door did not follow the book: %v", err)
		return
	}
	if err := remote.Save(s.gate.Dir, remote.Setup{Desired: setup.Desired, State: state, APIToken: setup.APIToken}, time.Now()); err != nil {
		log.Printf("tunnel: the door followed the book but its ids could not be written: %v", err)
	}
}

// answerFor is what the screen renders: the state it will show next time, and what this press changed.
func (s *Server) answerFor(desire remote.Desired, state remote.State, steps []remote.Step) remoteState {
	answer := s.remoteStateFor(remote.Setup{Desired: desire, State: state})
	answer.Configured = true
	answer.Steps = make([]remoteStep, 0, len(steps))
	for _, step := range steps {
		answer.Steps = append(answer.Steps, remoteStep{Name: step.Name, Created: step.Created, Moved: step.Moved, Detail: step.Detail})
	}
	return answer
}

// remoteCheck answers whether the door is up, from outside: the same request an unauthenticated browser makes, and
// the keys an assertion is verified against (ADR 0033 §8). It talks to the public hostname and the issuer, never to
// the Cloudflare API, so it needs no token — an install whose API token was deleted can still answer this.
func (s *Server) remoteCheck(w http.ResponseWriter, r *http.Request) {
	setup, err := remote.Load(s.gate.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !setup.Configured() {
		http.Error(w, "there is no door to check yet: make it first", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	// The issuer is the environment's when it set one — that is the override a test or a machine behind somebody
	// else's proxy uses — and otherwise the one the team domain in remote.json derives. A file-only install sets
	// no variable, and reading only the environment left the check able to judge the redirect and nothing else
	// (ADR 0036 §5, ADR 0033 §8).
	issuer := strings.TrimSpace(os.Getenv("ACORND_IDENTITY_ISSUER"))
	if issuer == "" {
		issuer = setup.Env()["ACORND_IDENTITY_ISSUER"]
	}
	writeJSON(w, http.StatusOK, doorCheck(ctx, nil, setup.Desired.Hostname, issuer))
}

// remoteStatusFor maps a Cloudflare refusal onto an HTTP one: a token that cannot do this is the person's to fix
// (400), and anything else is the far side failing (502).
func remoteStatusFor(err error) int {
	if errors.Is(err, remote.ErrNoToken) {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

// cloudflareToken is the token a remote route uses: what the body carried, then the environment's, then the one
// the console stored. The body wins because a person who just pasted a token means that one (ADR 0033 §4).
func cloudflareToken(fromBody string, setup remote.Setup) string {
	for _, token := range []string{fromBody, os.Getenv("ACORND_CLOUDFLARE_TOKEN"), setup.APIToken} {
		if trimmed := strings.TrimSpace(token); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// remoteZone is one zone as the form reads it: the name it shows, the id an apply needs, and the account the tunnel
// will live in.
type remoteZone struct {
	Name      string `json:"name"`
	ID        string `json:"id"`
	AccountID string `json:"accountId"`
}

// previewAnswer is what a token can see, in the shape the screen draws: the zones it may write a record in, and the
// login domain the door's issuer comes from. The team domain is a plain string rather than an optional one, so a
// screen reads "none recorded" from it instead of from a missing field.
type previewAnswer struct {
	Zones      []remoteZone `json:"zones"`
	TeamDomain string       `json:"teamDomain"`
}

// preview is the answer itself: every zone the token can see, and the team domain of the account the first one
// belongs to (ADR 0036 §6).
func preview(ctx context.Context, api remote.API) (previewAnswer, error) {
	zones, err := api.Zones(ctx)
	if err != nil {
		return previewAnswer{}, err
	}
	answer := previewAnswer{Zones: make([]remoteZone, 0, len(zones))}
	for _, zone := range zones {
		answer.Zones = append(answer.Zones, remoteZone{Name: zone.Name, ID: zone.ID, AccountID: zone.Account})
	}
	answer.TeamDomain = teamDomainOf(ctx, api, answer.Zones)
	return answer, nil
}

// teamDomainOf reads the login domain of the account the first zone is in. It is a display value: a token that may
// see zones but not organizations still shows the zones, and an account whose Access has never been used answers
// with none rather than with a failure, because neither is a reason to refuse the token (ADR 0036 §6).
func teamDomainOf(ctx context.Context, api remote.API, zones []remoteZone) string {
	if len(zones) == 0 || zones[0].AccountID == "" {
		return ""
	}
	domain, err := api.AccessOrganization(ctx, zones[0].AccountID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(domain)
}

// nothingToTurnOn is what an enable is still waiting for, or "" when the file records a door to open. It reads the
// same values the boot reads, so the sentence names the one an apply would write next (ADR 0036 §5–§6).
func nothingToTurnOn(setup remote.Setup) string {
	switch {
	case !setup.Configured():
		return "there is nothing to turn on yet: apply a hostname and a zone first, so the install has a door"
	case setup.State.Audience == "":
		return "this install's door is half-finished (no audience): run the apply again, then turn it on"
	case strings.TrimSpace(setup.State.TeamDomain) == "":
		return "this install's door is half-finished (no team domain): run the apply again, then turn it on"
	}
	return ""
}

// loopbackHost is whether a listen address is this machine only, in the same terms the run uses: the name, or an
// address the IP parser knows to be loopback. Anything else is somewhere on the network, and a door on one address
// with a hole on the others is not a door (ADR 0032 §8).
func loopbackHost(host string) bool {
	trimmed := strings.TrimSpace(host)
	if strings.EqualFold(trimmed, "localhost") {
		return true
	}
	address := net.ParseIP(trimmed)
	return address != nil && address.IsLoopback()
}

// zoneByName is the one lookup the apply does not do itself: the screen types a domain, Cloudflare wants an id.
func zoneByName(ctx context.Context, api remote.API, name string) (remote.Zone, error) {
	lookup, ok := api.(interface {
		ZoneByName(context.Context, string) (remote.Zone, error)
	})
	if !ok {
		return remote.Zone{}, errors.New("this build cannot look a zone up")
	}
	return lookup.ZoneByName(ctx, name)
}
