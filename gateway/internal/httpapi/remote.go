// The remote-access screen's two calls: what this install recorded, and the one that makes Cloudflare match it.
//
// They exist so the first contact with the door does not need a terminal: the admin pastes the API token and the
// hostname, and the console does what `acornd remote apply` does. The token is used for the call and dropped
// — it is the most powerful credential in the install and ADR 0033 §4 says the running process does not keep it.
package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/remote"
)

// remoteAPI is how the handler reaches Cloudflare. A variable so a test can answer without an account.
var remoteAPI = func(token string) remote.API { return &remote.Client{Token: token} }

// remoteApplyBudget is how long one press waits on Cloudflare. Longer than a boot's patience, shorter than the
// point where somebody presses the button again.
const remoteApplyBudget = 120 * time.Second

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
	TeamDomain string       `json:"teamDomain,omitempty"`
	Audience   string       `json:"audience,omitempty"`
	Updated    string       `json:"updated,omitempty"`
	TunnelID   string       `json:"tunnelId,omitempty"`
	Steps      []remoteStep `json:"steps,omitempty"`
}

// remoteStatus answers what is recorded. Nothing secret: the tunnel's token and the API token never leave the
// install folder and the environment.
func (s *Server) remoteStatus(w http.ResponseWriter, r *http.Request) {
	setup, err := remote.Load(s.gate.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, remoteStateFor(setup))
}

// remoteStateFor is what the screen reads: ids and a yes/no about the token, never the token.
func remoteStateFor(setup remote.Setup) remoteState {
	return remoteState{
		Configured:  setup.Configured(),
		TokenStored: setup.APIToken != "",
		Hostname:    setup.Desired.Hostname,
		ZoneID:      setup.Desired.ZoneID,
		AccountID:   setup.Desired.AccountID,
		TeamDomain:  setup.State.TeamDomain,
		Audience:    setup.State.Audience,
		Updated:     setup.Updated,
		TunnelID:    setup.State.TunnelID,
	}
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
	answer := answerFor(desire, state, steps)
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
	// The stored token is the fallback behind the body and the environment: the admin pasted it once, the door
	// follows the book from then on (ADR 0033 §3, §4 as amended).
	setup, err := remote.Load(s.gate.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return applyRequest{}, false
	}
	request.setup = setup
	if request.token == "" {
		request.token = strings.TrimSpace(os.Getenv("ACORND_CLOUDFLARE_TOKEN"))
	}
	if request.token == "" {
		request.token = strings.TrimSpace(setup.APIToken)
	}
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
	writeJSON(w, http.StatusOK, remoteStateFor(setup))
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
func answerFor(desire remote.Desired, state remote.State, steps []remote.Step) remoteState {
	answer := remoteState{
		Configured: true,
		Hostname:   desire.Hostname,
		ZoneID:     desire.ZoneID,
		AccountID:  desire.AccountID,
		TeamDomain: state.TeamDomain,
		Audience:   state.Audience,
		TunnelID:   state.TunnelID,
		Steps:      make([]remoteStep, 0, len(steps)),
	}
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
	writeJSON(w, http.StatusOK, remote.CheckDoor(ctx, nil, setup.Desired.Hostname, os.Getenv("ACORND_IDENTITY_ISSUER")))
}

// remoteStatusFor maps a Cloudflare refusal onto an HTTP one: a token that cannot do this is the person's to fix
// (400), and anything else is the far side failing (502).
func remoteStatusFor(err error) int {
	if errors.Is(err, remote.ErrNoToken) {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
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
