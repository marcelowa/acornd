package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/acornfall/acornfall/gateway/internal/supervise"
)

// supervisorCommand is the acornfall binary's other life: it is what runs as a sandbox's first
// process, holding the namespaces open and running the commands a workspace asks for.
//
// It is a subcommand rather than a second binary because it is the same code either way and the
// binary is already on disk; the sandbox binds it read-only at a fixed path so a workspace does
// not need acornfall installed inside its image. Nothing here names a sandbox engine: an engine
// starts it and gives it a socket, and this runs whatever argv it is sent.
func supervisorCommand(argv []string) {
	flags := flag.NewFlagSet("sandbox-supervisor", flag.ContinueOnError)
	socket := flags.String("socket", "", "the Unix socket to serve on")
	idle := flags.Duration("idle-timeout", 0, "stop the sandbox when nothing has run in it for this long (0 never stops)")
	if err := flags.Parse(argv); err != nil {
		os.Exit(2)
	}
	if *socket == "" {
		fmt.Fprintln(os.Stderr, "usage: acornfall sandbox-supervisor --socket PATH [--idle-timeout 30m]")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := supervise.New(*socket, "", *idle).WithLogger(log.Printf)
	if err := server.Serve(ctx); err != nil {
		log.Fatalf("sandbox-supervisor: %v", err)
	}
}
