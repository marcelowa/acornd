package main

import (
	"context"
	"log"
	"strings"
	"sync"

	"github.com/marcelowa/acornd/gateway/internal/cloudflared"
	"github.com/marcelowa/acornd/gateway/internal/remote"
)

// tunnelReport is the connector's state as this process knows it, for the Remote screen. It is read by the
// HTTP edge on every status call and written by the supervisor as it dials, connects, dies and restarts.
type tunnelReport struct {
	mu     sync.Mutex
	status cloudflared.Status
}

// set records a state. The callback shape is the supervisor's.
func (r *tunnelReport) set(status cloudflared.Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
}

// get is the two fields the screen shows.
func (r *tunnelReport) get() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status.State, r.status.Detail
}

// startTunnel keeps the connector alive for an install with a door in front of it.
//
// The family never starts this. A console reached through Cloudflare Access is a hostname, a route, and a
// process dialling out; the first two are what `remote apply` writes, and this is what makes the third true
// without anybody opening a terminal after a reboot. The question is asked of `remote.json`, not of the
// environment (ADR 0036 §5): an install with no record has no tunnel to run and says nothing, and one whose
// apply stopped before the tunnel had a token gets one line rather than a connector dialling nothing.
func startTunnel(ctx context.Context, dataDir, explicit string, report *tunnelReport) {
	setup, err := remote.Load(dataDir)
	if err != nil {
		report.set(cloudflared.Status{State: "not started", Detail: err.Error()})
		log.Printf("tunnel: remote access could not be read: %v", err)
		return
	}
	if !setup.Configured() {
		report.set(cloudflared.Status{State: "off", Detail: "no remote access is recorded yet"})
		return
	}
	token := strings.TrimSpace(setup.State.TunnelToken)
	if token == "" {
		report.set(cloudflared.Status{State: "not started", Detail: "run `acornd remote apply`, then start again"})
		log.Printf("tunnel: not configured yet: run `acornd remote apply` to make the door, then start again")
		return
	}
	path := strings.TrimSpace(explicit)
	report.set(cloudflared.Status{State: cloudflared.StateStarting})
	if path == "" {
		path, err = cloudflared.Ensure(ctx, dataDir, nil)
		if err != nil {
			// Refused, not fatal: the console still serves on loopback, and a person who reads this line can
			// install the connector themselves and name it with --cloudflared.
			report.set(cloudflared.Status{State: "not started", Detail: err.Error()})
			log.Printf("tunnel: %v", err)
			return
		}
	}
	supervisor := &cloudflared.Supervisor{Path: path, Token: token, Log: log.Printf, Report: report.set}
	go func() {
		if err := supervisor.Run(ctx); err != nil {
			log.Printf("tunnel: %v", err)
		}
	}()
}
