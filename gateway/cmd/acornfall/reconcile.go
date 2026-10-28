package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/remote"
)

// reconcileBudget is how long a start waits on Cloudflare before giving up on it. A boot is not the place to
// hang on somebody else's API: the console serves either way, and a door that is briefly stale is better than a
// machine that looks dead because a dashboard call is slow.
const reconcileBudget = 90 * time.Second

// reconcileRemote makes the account match what this install recorded, at every start.
//
// The declared state is `remote.json`; the reconciler is `apply`, which creates nothing twice. Running it at boot
// is what turns "remote access" from a command somebody remembers into a property: a group changed in the book,
// a record deleted in a dashboard, a tunnel replaced — all of it heals at the next start.
//
// Three ways it declines, each said once and none of them fatal:
//   - an install with no door in front of it has nothing to reconcile;
//   - an install that never ran an apply has no declared state to match;
//   - an install with no API token cannot talk to Cloudflare, and the person who can fix that is told so.
func reconcileRemote(ctx context.Context, dataDir string) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("ACORNFALL_IDENTITY")), "access") {
		return
	}
	setup, err := remote.Load(dataDir)
	if err != nil {
		log.Printf("tunnel: remote access could not be read: %v", err)
		return
	}
	if !setup.Configured() {
		return
	}
	token := strings.TrimSpace(os.Getenv("ACORNFALL_CLOUDFLARE_TOKEN"))
	if token == "" {
		// The API token is not stored: it is the account's, and keeping it in the install folder would make a
		// file in a workspace-adjacent directory worth stealing. The boot has it when the service has it.
		log.Printf("tunnel: not reconciled (no ACORNFALL_CLOUDFLARE_TOKEN); what is there still works, and `acornfall remote apply` changes it")
		return
	}
	book, admitted, err := remoteInputs(dataDir)
	if err != nil {
		log.Printf("tunnel: not reconciled: %v", err)
		return
	}
	bounded, cancel := context.WithTimeout(ctx, reconcileBudget)
	defer cancel()
	api := &remote.Client{Token: token}
	state, steps, err := applyDesire(bounded, dataDir, book.Desired, setup, admitted, api)
	if err != nil {
		log.Printf("tunnel: reconcile stopped: %v", err)
		return
	}
	log.Printf("tunnel: reconciled %s (%s); audience %s", state.Hostname, summarise(steps), state.Audience)
}

// summarise is the one line a boot log gets: what changed, and nothing about what did not. A start that says
// "seven kept" every time is a line people stop reading.
func summarise(steps []remote.Step) string {
	created, moved := 0, 0
	for _, step := range steps {
		if step.Created {
			created++
		}
		if step.Moved {
			moved++
		}
	}
	switch {
	case created == 0 && moved == 0:
		return "nothing to change"
	case created > 0 && moved > 0:
		return fmt.Sprintf("%d created, %d moved", created, moved)
	case created > 0:
		return fmt.Sprintf("%d created", created)
	default:
		return fmt.Sprintf("%d moved", moved)
	}
}
