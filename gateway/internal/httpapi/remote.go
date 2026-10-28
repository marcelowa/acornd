// The remote-access screen's two calls: what this install recorded, and the one that makes Cloudflare match it.
//
// They exist so the first contact with the door does not need a terminal: the owner pastes the API token and the
// hostname, and the console does what `acornfall remote apply` does. The token is used for the call and dropped
// — it is the most powerful credential in the install and ADR 0033 §4 says the running process does not keep it.
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/remote"
)

// remoteAPI is how the handler reaches Cloudflare. A variable so a test can answer without an account.
var remoteAPI = func(token string) remote.API { return &remote.Client{Token: token} }

// remoteApplyBudget is how long one press waits on Cloudflare. Longer than a boot's patience, shorter than the
// point where somebody presses the button again.
const remoteApplyBudget = 120 * time.Second

type remoteStep struct {
	Name    string `json:"name"`
	Created bool   `json:"created"`
	Moved   bool   `json:"moved,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type remoteState struct {
	Configured bool         `json:"configured"`
	Hostname   string       `json:"hostname,omitempty"`
	ZoneID     string       `json:"zoneId,omitempty"`
	AccountID  string       `json:"accountId,omitempty"`
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
	writeJSON(w, http.StatusOK, remoteState{
		Configured: setup.Configured(),
		Hostname:   setup.Desired.Hostname,
		ZoneID:     setup.Desired.ZoneID,
		AccountID:  setup.Desired.AccountID,
		Audience:   setup.State.Audience,
		Updated:    setup.Updated,
		TunnelID:   setup.State.TunnelID,
	})
}

// remoteApply makes Cloudflare match what the owner asks for, and answers the steps it took — the same list the
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
	// Written whatever happened: what an apply made before it failed is what a resume reads instead of starting
	// over, and losing it is how a second tunnel appears.
	if saveErr := remote.Save(s.gate.Dir, remote.Setup{Desired: desire, State: state}, time.Now()); saveErr != nil {
		http.Error(w, "the apply ran but its ids could not be written: "+saveErr.Error(), http.StatusInternalServerError)
		return
	}
	answer := answerFor(desire, state, steps)
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
	if request.token == "" {
		request.token = strings.TrimSpace(os.Getenv("ACORNFALL_CLOUDFLARE_TOKEN"))
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
	setup, err := remote.Load(s.gate.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return applyRequest{}, false
	}
	request.setup = setup
	return request, true
}

// answerFor is what the screen renders: the state it will show next time, and what this press changed.
func answerFor(desire remote.Desired, state remote.State, steps []remote.Step) remoteState {
	answer := remoteState{
		Configured: true,
		Hostname:   desire.Hostname,
		ZoneID:     desire.ZoneID,
		AccountID:  desire.AccountID,
		Audience:   state.Audience,
		TunnelID:   state.TunnelID,
		Steps:      make([]remoteStep, 0, len(steps)),
	}
	for _, step := range steps {
		answer.Steps = append(answer.Steps, remoteStep{Name: step.Name, Created: step.Created, Moved: step.Moved, Detail: step.Detail})
	}
	return answer
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
