package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/control"
	"github.com/marcelowa/acornd/gateway/internal/support"
	"github.com/marcelowa/acornd/gateway/internal/update"
	"github.com/marcelowa/acornd/gateway/internal/vault"
)

type supportArgs struct {
	verb      string
	dataDir   string
	workspace string
	out       string
}

// supportCommand is the local half of the support bundle: preview what will be collected, or
// write the redacted tar.gz. The OS user owns the install, so the shell is the approval; the
// console's route asks the room's owner instead.
func supportCommand(argv []string) {
	args, ok := parseSupportArgs(argv)
	if !ok {
		return
	}
	bundle := collectSupport(args)
	switch args.verb {
	case "preview":
		printSupportPreview(bundle.Preview)
	case "bundle":
		writeSupportBundle(bundle, args)
	}
}

func parseSupportArgs(argv []string) (supportArgs, bool) {
	if len(argv) == 0 {
		usageSupport()
		os.Exit(2)
	}
	verb := argv[0]
	if verb != "preview" && verb != "bundle" {
		usageSupport()
		os.Exit(2)
	}
	flags := flag.NewFlagSet("support "+verb, flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornd folder (default: ~/.acornd)")
	workspace := flags.String("workspace", "", "the workspace to diagnose")
	out := flags.String("out", "", "where to write the bundle (default: ./acornd-support-<workspace>-<time>.tar.gz)")
	flagArgs, _ := splitFlags(argv[1:], map[string]bool{"data-dir": true, "workspace": true, "out": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	if *workspace == "" {
		log.Fatalf("acornd support %s: --workspace is required", verb)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		log.Fatalf("acornd support: %v", err)
	}
	return supportArgs{verb: verb, dataDir: dir, workspace: *workspace, out: *out}, true
}

func collectSupport(args supportArgs) *support.Bundle {
	workspace, err := supportWorkspace(args.dataDir, args.workspace)
	if err != nil {
		log.Fatalf("acornd support: %v", err)
	}
	secrets, warnings := supportSecrets(args.dataDir, workspace.Path)
	bundle, err := support.Collect(support.Options{
		DataDir:       args.dataDir,
		Workspace:     workspace.Name,
		WorkspacePath: workspace.Path,
		Engine:        workspace.Engine,
		Home:          userHome(),
		Version:       update.BuildVersion,
		Secrets:       secrets,
		Warnings:      warnings,
	})
	if err != nil {
		log.Fatalf("acornd support: %v", err)
	}
	return bundle
}

func userHome() string {
	home, _ := os.UserHomeDir()
	return home
}

func supportSecrets(dataDir, workspacePath string) ([]string, []string) {
	store, err := vault.Open(filepath.Join(dataDir, "secrets.json"))
	if err != nil {
		return nil, []string{"the vault could not be opened for redaction: " + err.Error()}
	}
	entries, err := store.Resolve(workspacePath)
	if err != nil {
		return nil, []string{"the vault could not be read for redaction: " + err.Error()}
	}
	values := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Value != "" {
			values = append(values, entry.Value)
		}
	}
	return values, nil
}

func writeSupportBundle(bundle *support.Bundle, args supportArgs) {
	raw, err := bundle.TarGz()
	if err != nil {
		log.Fatalf("acornd support: %v", err)
	}
	path := args.out
	if path == "" {
		path = fmt.Sprintf("acornd-support-%s-%s.tar.gz", safeName(args.workspace), time.Now().UTC().Format("20060102-150405"))
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		log.Fatalf("acornd support: %v", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	fmt.Printf("support bundle: %s (%d bytes)\n", absolute, len(raw))
	fmt.Println("Review it before sharing; it is a local file and nothing was uploaded.")
}

func usageSupport() {
	fmt.Fprint(os.Stderr, `usage: acornd support preview --workspace NAME [--data-dir DIR]
       acornd support bundle  --workspace NAME [--out PATH] [--data-dir DIR]

  preview  list the files and redactions without writing anything
  bundle   write a redacted .tar.gz for a bug report
`)
}

func supportWorkspace(dataDir, name string) (control.Workspace, error) {
	file, err := control.LoadRegistryFile(filepath.Join(dataDir, "workspaces.json"))
	if err != nil {
		return control.Workspace{}, err
	}
	for _, workspace := range file.Workspaces {
		if workspace.Name == name || strings.EqualFold(workspace.Title, name) {
			return workspace, nil
		}
	}
	names := make([]string, 0, len(file.Workspaces))
	for _, workspace := range file.Workspaces {
		names = append(names, workspace.Name)
	}
	return control.Workspace{}, fmt.Errorf("no workspace %q (have %s)", name, strings.Join(names, ", "))
}

func printSupportPreview(preview support.Preview) {
	fmt.Printf("workspace: %s\n", preview.Workspace)
	fmt.Printf("created:   %s\n", preview.Created.UTC().Format(time.RFC3339))
	fmt.Printf("files:     %d\n", len(preview.Files))
	for _, file := range preview.Files {
		fmt.Printf("  %8d  %s\n", file.Size, file.Name)
	}
	fmt.Println("redactions:")
	for _, note := range preview.Redactions {
		fmt.Printf("  %s\n", note)
	}
	if len(preview.Warnings) == 0 {
		return
	}
	fmt.Println("warnings:")
	for _, warning := range preview.Warnings {
		fmt.Printf("  %s\n", warning)
	}
}

func safeName(name string) string {
	return strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(name)
}
