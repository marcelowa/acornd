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
		case "purge":
			workspacePurge(argv[1:])
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
  remove <name>        forget a workspace; its folder and its history stay
  purge <name> --yes   delete a workspace's folder and its record; the only thing that deletes

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
	noDevices := flags.Bool("no-devices", false, "withhold /dev/net/tun and /dev/kvm from this bwrap sandbox")
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
		noDevices: *noDevices,
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
  --no-devices       withhold /dev/net/tun and /dev/kvm from a bwrap sandbox; the
                     default grants them so a workspace can run another acornfall
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
	noDevices bool
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
	var devices *bool
	if record.noDevices {
		decision := true
		devices = &decision
	}
	// A room, with nobody in it yet: this command has no way to say who its owner is, and the gateway's load
	// gives an unnamed room to the install's admin, which is who a folder added from a terminal belongs to.
	registry.Workspaces = append(registry.Workspaces, control.Workspace{
		Name: record.name, Path: record.absolute, Title: record.name, Kind: control.KindRoom,
		Image: record.image, Network: record.network, Engine: chosen, KeepAlive: record.keepAlive,
		Private: record.private, DisableVMNesting: nesting, DisableDevices: devices,
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
		if workspace.Personal() {
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
	record, err := recordFor(registry, args[0])
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	if record.Personal() {
		// Somebody's own room is never shared and never removed: forgetting it removes the one place they had, and
		// the admin can do that by revoking the person, which is a decision with a name.
		log.Fatalf("acornfall: %s is somebody's own room and cannot be forgotten", record.Name)
	}
	registry.Workspaces = withoutWorkspace(registry.Workspaces, record.Name)
	if err := control.SaveRegistryFile(registry); err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	fmt.Printf("forgot %s (its folder and history are untouched)\n", record.Name)
}

// purgeUsage is the one command in this file that deletes anything, and it says so before it does.
const purgeUsage = `usage: acornfall workspace purge <name> --yes

  --yes              delete the folder and the record; without it, purge only names the folder
  --data-dir DIR     the acornfall folder (default: ~/.acornfall)

Forgetting a workspace and deleting one are two acts, and this is the second (ADR 0034 §6). It
needs the record to find the folder: a forgotten folder is just a folder, and a command that
deleted a directory by a name nothing points at any more is how somebody removes the wrong one.
`

// workspacePurge deletes a workspace's folder and its record. It is the only thing in acornfall that
// deletes a workspace, it is never behind a route or a button, and it prints the folder first: a path is
// the one thing a person can check before an unrecoverable act.
func workspacePurge(argv []string) {
	dir, name, yes := purgeArgs(argv)
	path := filepath.Join(dir, "workspaces.json")
	registry, err := control.LoadRegistryFile(path)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	workspace, err := purgeTarget(registry, name)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	fmt.Printf("purge %s, folder %s\n", workspace.Name, workspace.Path)
	if !yes {
		fmt.Println("nothing was deleted: run it again with --yes")
		os.Exit(1)
	}
	if err := os.RemoveAll(workspace.Path); err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	registry.Workspaces = withoutWorkspace(registry.Workspaces, workspace.Name)
	if err := control.SaveRegistryFile(registry); err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	fmt.Printf("purged %s: the folder and the record are gone\n", workspace.Name)
}

// purgeArgs is the three things this command cannot guess: which install, which name, and whether the person
// meant it. It is separate from the deleting so that the deleting reads as one act.
func purgeArgs(argv []string) (dir, name string, yes bool) {
	flags := flag.NewFlagSet("workspace purge", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	confirmed := flags.Bool("yes", false, "delete the folder and the record")
	flags.Usage = func() { fmt.Fprint(os.Stderr, purgeUsage) }
	flagArgs, positional := splitFlags(argv, map[string]bool{"data-dir": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	if len(positional) != 1 {
		flags.Usage()
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	return dir, positional[0], *confirmed
}

// withoutWorkspace is the registry with one name taken out. It filters in place: the write index never passes the
// read index, so the list behind it is the same list, shorter.
func withoutWorkspace(workspaces []control.Workspace, name string) []control.Workspace {
	kept := workspaces[:0]
	for _, workspace := range workspaces {
		if workspace.Name != name {
			kept = append(kept, workspace)
		}
	}
	return kept
}

// purgeTarget is the record a purge deletes, and the refusal when there is none.
//
// Purge needs the record to find the folder (ADR 0034 §6), so it runs instead of forget and never after it:
// once a room is forgotten, its folder is just a folder, and deleting a directory by a name that no longer
// means anything is how somebody removes the wrong one. The install's own room is refused as well: the next
// start makes it again, so purging it would take the history and leave a room that looks the same.
func purgeTarget(registry control.RegistryFile, name string) (control.Workspace, error) {
	workspace, err := recordFor(registry, name)
	if err != nil {
		return control.Workspace{}, fmt.Errorf("no workspace %q: a forgotten folder is just a folder, and there is no record of where it was", name)
	}
	if workspace.Name == control.PersonalWorkspaceName {
		return control.Workspace{}, fmt.Errorf("%s is the install's own room: the next start makes it again, so its history would be gone with nothing to show for it", name)
	}
	return workspace, nil
}

// recordFor is the workspace a verb names, refused when the registry holds no such name.
func recordFor(registry control.RegistryFile, name string) (control.Workspace, error) {
	for _, workspace := range registry.Workspaces {
		if workspace.Name == name {
			return workspace, nil
		}
	}
	return control.Workspace{}, fmt.Errorf("no workspace %q", name)
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
