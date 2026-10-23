package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/sandbox"
)

/*
workspaceCommand is the operator's way to point a workspace at a folder of their choosing.
The interface cannot do this: it creates workspaces inside the workspaces folder by name,
which is what keeps it inside one directory. This edits the registry directly, and the
gateway picks the change up on its next start.
*/
func workspaceCommand(argv []string) {
	if len(argv) > 0 {
		switch argv[0] {
		case "list":
			workspaceList(argv[1:])
			return
		case "add":
			workspaceAdd(argv[1:])
			return
		case "remove":
			workspaceRemove(argv[1:])
			return
		case "help", "-h", "--help":
			workspaceUsage(os.Stdout)
			return
		}
	}
	workspaceUsage(os.Stderr)
	os.Exit(2)
}

func workspaceUsage(w *os.File) {
	fmt.Fprint(w, `usage: acornfall workspace <command>

  list                 every workspace, and the engine and image it runs on
  add <name> [folder]  register a folder as a workspace; the folder defaults to ./<name>
  remove <name>        unregister a workspace; its folder and its history stay

acornfall workspace add -h    explains the flags it takes
`)
}

// workspaceAdd is the only verb with flags, because it is the only verb that decides anything
// about a workspace: everything after creation is either an edit in the interface or a
// hand-edited registry.
func workspaceAdd(argv []string) {
	flags := flag.NewFlagSet("workspace add", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	image := flags.String("image", "", "sandbox image for this workspace, e.g. golang:1.24 (default: "+sandbox.DefaultImage+")")
	egress := flags.String("egress", "", `what the sandbox can reach: "public" (default), "private" (this machine's LAN and tailnet), or "off"`)
	engine := flags.String("sandbox", "", "sandbox engine: microsandbox, bwrap, or host (default: what this machine can run)")
	keepAlive := flags.Bool("keep-alive", false, "keep this workspace's sandbox running when nobody is watching it (for daemons and cron jobs)")
	noNesting := flags.Bool("no-vm-nesting", false, "refuse a hypervisor inside this workspace's sandbox")
	flags.Usage = func() { fmt.Fprint(os.Stderr, addUsage) }
	// Flags after the name and the folder are documented above, and the standard flag package
	// stops at the first positional. Splitting them is what makes
	// `workspace add demo ~/work/demo --sandbox bwrap` mean what it says.
	flagArgs, positional := splitFlags(argv, map[string]bool{
		"data-dir": true, "image": true, "sandbox": true, "egress": true,
	})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	args := positional
	if len(args) < 1 {
		flags.Usage()
		os.Exit(2)
	}
	network, private, chosen, ok := addChoices(*egress, *engine)
	if !ok {
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	path := filepath.Join(dir, "workspaces.json")
	registry, err := control.LoadRegistryFile(path)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	name, absolute, err := addTarget(args, registry)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	chosen = recordWorkspace(dir, registry, addRecord{
		name:      name,
		absolute:  absolute,
		image:     *image,
		engine:    chosen,
		network:   network,
		private:   private,
		keepAlive: *keepAlive,
		noNesting: *noNesting,
	})
	fmt.Printf("added %s at %s\n", name, absolute)
	fmt.Printf("sandbox: %s, engine %s, egress %s\n", imageLabel(*image), engineLabel(chosen), egressLabel(network, private))
}

// addUsage is the flags a person may pass, and the one decision this command makes for them.
const addUsage = `usage: acornfall workspace add <name> [folder] [flags]

  --sandbox ENGINE   microsandbox (needs a hypervisor), bwrap (needs user namespaces), or
                     host (no isolation); default: what this machine can run
  --image IMAGE      the sandbox image (default: ` + sandbox.DefaultImage + `)
  --egress WHERE     public (default), private (this machine's LAN and tailnet too), or off
  --keep-alive       keep the sandbox running when nobody is watching it
  --no-vm-nesting    refuse a hypervisor inside the sandbox
  --data-dir DIR     the acornfall folder (default: ~/.acornfall)

The folder defaults to ./<name>. The engine is recorded here, once: a workspace keeps the
engine it was created with.
`

// addTarget is the name and the folder the name goes with, refused when the folder is not there
// or the name is taken: both are things the person can fix, so both say what is wrong.
func addTarget(positional []string, registry control.RegistryFile) (name, absolute string, err error) {
	name = positional[0]
	folder := name
	if len(positional) > 1 {
		folder = positional[1]
	}
	absolute, err = filepath.Abs(folder)
	if err != nil {
		return "", "", err
	}
	if info, statErr := os.Stat(absolute); statErr != nil || !info.IsDir() {
		return "", "", fmt.Errorf("no folder at %s", absolute)
	}
	for _, workspace := range registry.Workspaces {
		if workspace.Name == name {
			return "", "", fmt.Errorf("workspace %q already exists", name)
		}
	}
	return name, absolute, nil
}

// addChoices validates the two flags that can be wrong before anything is created, and answers
// with the engine that was asked for: blank means "whatever this machine can run", which is
// decided later, where the machine is known.
func addChoices(egress, engine string) (network string, private bool, chosen string, ok bool) {
	network, private, err := parseEgress(egress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acornfall: %v\n", err)
		return "", false, "", false
	}
	if !sandbox.ValidEngine(engine) {
		fmt.Fprintf(os.Stderr, "acornfall: --sandbox is one of %s\n", strings.Join(sandbox.EngineNames(), ", "))
		return "", false, "", false
	}
	return network, private, engine, true
}

// addRecord is one workspace as `workspace add` decided it: the flags, and what this machine can
// run, which is the last decision made here.
type addRecord struct {
	name      string
	absolute  string
	image     string
	engine    string
	network   string
	private   bool
	keepAlive bool
	noNesting bool
}

// recordWorkspace appends the workspace and writes the registry, and answers with the engine it
// recorded. Left blank, an engine would mean "whatever this machine prefers", and an unrecorded
// choice is one that changes if the workspace moves.
func recordWorkspace(dir string, registry control.RegistryFile, record addRecord) string {
	chosen := record.engine
	if chosen == "" {
		chosen = sandbox.NewTier(dir, "", nil).Default()
	}
	// A pointer with a false or true value is a decision; nil is "nest when it can".
	var nesting *bool
	if record.noNesting {
		decision := true
		nesting = &decision
	}
	registry.Workspaces = append(registry.Workspaces, control.Workspace{
		Name: record.name, Path: record.absolute, Title: record.name, Image: record.image,
		Network: record.network, Engine: chosen, KeepAlive: record.keepAlive,
		Private: record.private, DisableVMNesting: nesting,
	})
	if err := control.SaveRegistryFile(registry); err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	return chosen
}

func workspaceList(argv []string) {
	flags := flag.NewFlagSet("workspace list", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	flagArgs, _ := splitFlags(argv, map[string]bool{"data-dir": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	registry, err := control.LoadRegistryFile(filepath.Join(dir, "workspaces.json"))
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	if len(registry.Workspaces) == 0 {
		fmt.Println("no workspaces registered")
		return
	}
	for _, workspace := range registry.Workspaces {
		marker := " "
		if workspace.General {
			marker = "*"
		}
		fmt.Printf("%s %-16s %-14s %-30s %s\n", marker, workspace.Name,
			engineLabel(workspace.Engine), sandboxLabel(workspace), workspace.Path)
	}
	if registry.WorkspacesDir != "" {
		fmt.Printf("\nworkspaces folder: %s\n", registry.WorkspacesDir)
	}
}

func workspaceRemove(argv []string) {
	flags := flag.NewFlagSet("workspace remove", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: acornfall workspace remove <name>\n\nIts folder and its history stay; only the registration goes.\n")
	}
	flagArgs, positional := splitFlags(argv, map[string]bool{"data-dir": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	args := positional
	if len(args) != 1 {
		flags.Usage()
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	path := filepath.Join(dir, "workspaces.json")
	registry, err := control.LoadRegistryFile(path)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	kept := registry.Workspaces[:0]
	removed := false
	for _, workspace := range registry.Workspaces {
		if workspace.Name != args[0] {
			kept = append(kept, workspace)
			continue
		}
		if workspace.General {
			log.Fatalf("acornfall: the general workspace cannot be removed")
		}
		removed = true
	}
	if !removed {
		log.Fatalf("acornfall: no workspace %q", args[0])
	}
	registry.Workspaces = kept
	if err := control.SaveRegistryFile(registry); err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	fmt.Printf("removed %s (its folder and history are untouched)\n", args[0])
}

// parseEgress turns the one question into the two fields a workspace record keeps.
func parseEgress(value string) (network string, private bool, err error) {
	switch value {
	case "", "public":
		return "", false, nil
	case "private":
		return "", true, nil
	case "off":
		return control.NetworkOff, false, nil
	}
	return "", false, fmt.Errorf(`--egress is "public", "private", or "off", not %q`, value)
}

func egressLabel(network string, private bool) string {
	switch {
	case network == control.NetworkOff:
		return "off"
	case private:
		return "public internet and this machine's private networks"
	}
	return "public internet only"
}

// splitFlags separates an argument list into flags and positionals the way people write
// them: `add demo /path --sandbox bwrap`, not only `--sandbox bwrap add demo /path`. An
// unknown flag is left for flag.Parse to refuse, so a typo still says so.
func splitFlags(argv []string, takesValue map[string]bool) (flags, positional []string) {
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if takesValue[name] && i+1 < len(argv) {
			flags = append(flags, argv[i+1])
			i++
		}
	}
	return flags, positional
}

// sandboxLabel is what a workspace's sandbox will be, for the list column: short enough to
// scan, specific enough to answer "which image is this one running".
func sandboxLabel(workspace control.Workspace) string {
	image := workspace.Image
	if image == "" {
		image = sandbox.DefaultImage
	}
	return image + ", " + shortEgress(workspace.Network, workspace.Private)
}

func shortEgress(network string, private bool) string {
	switch {
	case network == control.NetworkOff:
		return "net off"
	case private:
		return "net on, private"
	}
	return "net on"
}

// engineLabel names the engine a workspace runs on. A workspace that named none says so: the
// engine it runs on is this machine's answer, and the gateway records it the first time it
// starts there rather than this command guessing at it.
func engineLabel(engine string) string {
	if engine == "" {
		return "this machine's (unrecorded)"
	}
	return engine
}

func imageLabel(image string) string {
	if image == "" {
		return "image " + sandbox.DefaultImage + " (default)"
	}
	return "image " + image
}
