package main

import (
	"context"
	"log"
	"strings"

	"github.com/marcelowa/acornd/gateway/internal/cloudflared"
	"github.com/marcelowa/acornd/gateway/internal/remote"
)

// startTunnel keeps the connector alive for an install with a door in front of it.
//
// The family never starts this. A console reached through Cloudflare Access is a hostname, a route, and a
// process dialling out; the first two are what `remote apply` writes, and this is what makes the third true
// without anybody opening a terminal after a reboot. The question is asked of `remote.json`, not of the
// environment (ADR 0036 §5): an install with no record has no tunnel to run and says nothing, and one whose
// apply stopped before the tunnel had a token gets one line rather than a connector dialling nothing.
func startTunnel(ctx context.Context, dataDir, explicit string) {
	setup, err := remote.Load(dataDir)
	if err != nil {
		log.Printf("tunnel: remote access could not be read: %v", err)
		return
	}
	if !setup.Configured() {
		return
	}
	token := strings.TrimSpace(setup.State.TunnelToken)
	if token == "" {
		log.Printf("tunnel: not configured yet: run `acornd remote apply` to make the door, then start again")
		return
	}
	path := strings.TrimSpace(explicit)
	if path == "" {
		path, err = cloudflared.Ensure(ctx, dataDir, nil)
		if err != nil {
			// Refused, not fatal: the console still serves on loopback, and a person who reads this line can
			// install the connector themselves and name it with --cloudflared.
			log.Printf("tunnel: %v", err)
			return
		}
	}
	supervisor := &cloudflared.Supervisor{Path: path, Token: token, Log: log.Printf}
	go func() {
		if err := supervisor.Run(ctx); err != nil {
			log.Printf("tunnel: %v", err)
		}
	}()
}
