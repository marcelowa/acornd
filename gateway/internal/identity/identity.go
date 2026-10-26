// Package identity answers who is calling the console, and refuses when it cannot tell.
// See docs/adr/0032-who-may-call-the-console.md.
//
// A door is a proof, never an address. Two exist: the install's token file, which proves the owner
// from the command line, and a signed assertion the edge attaches to every request it forwards,
// which proves a person from a browser. Loopback is not one of them — ADR 0012 records that a
// workspace's guest can reach the gateway's own API, so treating "it came from this machine" as the
// owner would make every agent in every workspace the owner. A request from 127.0.0.1 with no proof
// is refused like any other.
package identity

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Caller is who is calling: the install's owner, proven locally, or a person, proven remotely.
//
// Owner is the install owner and is deliberately not a member of anyone else's workspace (ADR 0032
// decision 4): owning the install means lifecycle, credentials and policy, not reading a
// conversation. Email is empty only while the owner has not been claimed with `acornfall people`.
type Caller struct {
	Owner bool
	Email string
}

// Door proves who is calling. Prove answers a Caller, or an error whose text is the sentences the
// caller reads: a refusal that does not say why is a support case, and the people behind these
// doors are family, not operators.
type Door interface {
	Prove(*http.Request) (Caller, error)
}

var (
	// ErrNoProof is the refusal a request gets when no door could prove it. It is the one error the
	// edge still answers 401 for, so it must not be reused for anything else.
	ErrNoProof = errors.New("this console needs to know who is calling: run `acornfall token` on the machine that runs it, or sign in through the address you were sent")
	// ErrWrongToken is the token door's refusal. It is separate from ErrNoProof because the fix is
	// different: the token exists, and it is not the install's.
	ErrWrongToken = errors.New("that is not this install's token: read it again with `acornfall token`")
)

// Any admits a request proved by any of its doors, and reports every door's reason when none did.
// The reasons are joined rather than dropped because the common failure is a browser with no
// assertion sent to a console with no token door, and one sentence saying both is what points at
// the tunnel rather than at the pasted token.
func Any(doors ...Door) Door {
	return anyDoor(doors)
}

type anyDoor []Door

func (d anyDoor) Prove(r *http.Request) (Caller, error) {
	reasons := make([]string, 0, len(d))
	for _, door := range d {
		caller, err := door.Prove(r)
		if err == nil {
			return caller, nil
		}
		reasons = append(reasons, err.Error())
	}
	if len(reasons) == 0 {
		return Caller{}, ErrNoProof
	}
	return Caller{}, fmt.Errorf("%w — %s", ErrNoProof, strings.Join(reasons, "; "))
}
