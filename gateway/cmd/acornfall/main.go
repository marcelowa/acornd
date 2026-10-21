package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/httpapi"
	"github.com/acornfall/acornfall/gateway/internal/ready"
	"github.com/acornfall/acornfall/gateway/internal/sandbox"
	"github.com/acornfall/acornfall/gateway/internal/update"
	"github.com/acornfall/acornfall/gateway/internal/vault"
	"github.com/acornfall/acornfall/gateway/web"
)

func main() {
	// One place configures logging, and it is the entry point: a package that sets global
	// logger state when it is imported decides something the program did not ask for.
	log.SetFlags(log.Ltime)
	if len(os.Args) > 1 && os.Args[1] == "provider" {
		providers(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "workspace" {
		workspaceCommand(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "secret" {
		secretCommand(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "sandbox-supervisor" {
		// The sandbox's own first process: a sandbox calls this, a person does not.
		supervisorCommand(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "netns" {
		// The namespace pasta made, configured before the sandbox's own process starts: a
		// sandbox calls this, a person does not.
		netnsCommand(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		doctorCommand(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		versionCommand(os.Args[2:])
		return
	}
	if err := serve(os.Args[1:]); err != nil {
		log.Fatalf("acornfall: %v", err)
	}
}

func serve(argv []string) error {
	flags := flag.NewFlagSet("acornfall", flag.ContinueOnError)
	addr := flags.String("addr", "0.0.0.0:7777", "listen address (default: every interface)")
	token := flags.String("token", "", "shared secret guarding every request (default: none)")
	workspace := flags.String("workspace", "", "the general workspace's folder (default: the folder acornfall is started in)")
	home := flags.String("home", "", "the acornfall folder: config, credentials, state, workspaces (default: ~/.acornfall)")
	dataDir := flags.String("data-dir", "", "alias for --home")
	workspacesDir := flags.String("workspaces-dir", "", "where workspaces are created (default: ~/acornfall/workspaces)")
	sandboxState := flags.String("sandbox-state", "", "where sandboxes, images, and snapshots are kept (default: <home>/sandboxes); must be a short path")
	sandboxEngine := flags.String("sandbox-engine", "", "the default engine for workspaces that name none: microsandbox (needs a hypervisor), bwrap (needs user namespaces), or host (no isolation)")
	migrateDir := flags.String("migrate-dir", "", "release migrate scripts directory (default: the scripts baked into the binary)")
	disableVMNesting := flags.Bool("disable-vm-nesting", false, "keep a workspace's sandbox from nesting virtual machines; by default a sandbox nests when the engine and the machine can, and a workspace's own setting wins")
	model := flags.String("model", "", "default model as provider/modelId")
	workerCommand := flags.String("worker", "", `worker command (default: ensured bun over ensured sources)`)
	workerDir := flags.String("worker-dir", "", "worker directory (development: system bun over these sources, no ensure)")
	workerBin := flags.String("worker-bin", "", "single compiled worker binary (explicit opt-in; needs no sources)")
	if err := flags.Parse(argv); err != nil {
		return err
	}

	dir := *home
	if dir == "" {
		dir = *dataDir
	}
	resolved, err := resolveDataDir(dir)
	if err != nil {
		return err
	}
	dir = resolved
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// The vault is install-wide: one file beside credentials.json, never inside a
	// workspace and never mounted into a sandbox.
	secrets, err := vault.Open(filepath.Join(dir, "secrets.json"))
	if err != nil {
		return err
	}
	// The user's home is resolved here rather than inside control: the sandbox mounts `~/.agents`
	// at the same path the worker reads it from, and both have to be answering about the same
	// folder. The resource folders are created now so a sandbox always has somewhere to mount
	// from, even before anything has been written to them.
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot tell where the user's home is: %w", err)
	}
	if err := sandbox.EnsureGlobal(dir, userHome); err != nil {
		return err
	}
	// The general workspace's folder: the folder the install was started in, unless told
	// otherwise. An existing single-workspace install therefore keeps working where it was.
	general := *workspace
	if general == "" {
		if cwd, err := os.Getwd(); err == nil {
			general = cwd
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The self-update tier, owned early: boot readiness (migrate, bun, worker
	// sources) is decided before anything serves.
	updates := update.New(dir, "", *migrateDir)
	if err := updates.Open(); err != nil {
		return err
	}
	// A failing migrate stops boot and rolls back: the supervisor restarts
	// the last good build instead of a dead UI.
	if err := updates.RunPending(ctx); err != nil {
		return err
	}
	command, worker, workerManaged := resolveWorker(*workerCommand, *workerDir, *workerBin, updates)
	if workerManaged && updates.Status().Current != "dev" {
		// Best effort, never fatal: a missing toolchain with no network is
		// a loud refusal at workspace open, not a bricked boot.
		if _, err := updates.EnsureBun(ctx); err != nil {
			log.Printf("worker: %v", err)
		}
		if err := updates.EnsureWorker(ctx, updates.RuntimeTag()); err != nil {
			log.Printf("worker: %v", err)
		}
	}
	guard := httpapi.ResolveToken(*token, os.Getenv("ACORNFALL_TOKEN"))

	// The sandbox tier. One engine per install, and the worker is told which one, because it
	// attaches with that engine's client and the two must agree. Nothing here chooses for a
	// machine: naming the host engine is what makes a run unconfined.
	engineName := *sandboxEngine
	if engineName == "" {
		engineName = os.Getenv("ACORNFALL_SANDBOX_ENGINE")
	}
	if engineName == "host" && *sandboxState != "" {
		return errors.New("--sandbox-state belongs to the microsandbox engine; the host engine keeps no state")
	}
	engine, tier, err := sandboxTier(ctx, dir, *sandboxState, engineName)
	if err != nil {
		return err
	}

	go updates.StartLoop(ctx)

	workspaces := control.NewWorkspaces(control.WorkspacesOptions{
		Home:          dir,
		UserHome:      userHome,
		GeneralPath:   general,
		WorkspacesDir: *workspacesDir,
		WorkerCommand: command,
		WorkerDir:     worker,
		Update: func() control.UpdateSummary {
			status := updates.Status()
			return control.UpdateSummary{
				Current:         status.Current,
				Latest:          status.Latest,
				Available:       status.Available,
				Failed:          status.Failed,
				RestartRequired: status.RestartRequired,
			}
		},
		Model:     *model,
		Sandbox:   engine,
		Sandboxes: tier,
		Vault:     secrets,
		// Nesting is allowed unless refused: a workspace's sandbox nests when the
		// engine offers it and the machine can grant it, and boots without it otherwise.
		DisableVMNesting: *disableVMNesting,
	})
	if err := workspaces.Open(ctx); err != nil {
		return err
	}
	defer workspaces.Close()

	updates.RunsActive = workspaces.AnyRunning
	updates.StopRuns = workspaces.AbortAll
	updates.StopWorkers = workspaces.StopWorkers
	updates.Running = func() []update.Running {
		// The dialog names what stopping runs would stop, so the counts cross
		// the layer boundary as this tier's own shape.
		summary := workspaces.RunningSummary()
		runs := make([]update.Running, 0, len(summary))
		for _, item := range summary {
			runs = append(runs, update.Running{Workspace: item.Workspace, Runs: item.Runs})
		}
		return runs
	}
	updates.CleanVMs = func(ctx context.Context) error {
		// Paths, not names: engine sandbox names derive from the folder.
		var names []string
		for _, workspace := range workspaces.List() {
			names = append(names, workspace.Path)
		}
		stateDir := ""
		remove := func(context.Context, string) error { return nil }
		if engine != nil {
			stateDir = engine.Report().StateDir
			remove = engine.Remove
		}
		return update.CleanSandboxes(ctx, names, remove, stateDir)
	}

	// Readiness, plainly: every unsatisfied requirement is one log line
	// with its fix. Auto-installable pieces were ensured above; what is
	// still missing refuses loudly at use, never silently.
	for _, requirement := range ready.Check(readyInputForBoot(dir, updates, engine)) {
		if !requirement.OK {
			log.Printf("ready: %s not satisfied: %s (fix: %s)", requirement.Name, requirement.Detail, requirement.Fix)
		}
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.RequireToken(httpapi.New(workspaces, web.Files, updates).Handler(), guard),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	// Listen explicitly so the line below reports the address that was actually bound: with
	// a port of 0 the configured address says nothing about where to reach it.
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	bound := listener.Addr().String()
	log.Printf("acornfall listening on http://%s  home=%s", displayAddr(bound), dir)
	if guard != "" {
		log.Printf("auth: token required; open http://%s/?token=%s", prettyAddr(bound), guard)
	} else {
		log.Printf("auth: off (no token)")
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// sandboxTier builds the machine's sandbox tier, or returns nil when this run asks for none.
// Opening it installs nothing: the engine is fetched on the first workspace that needs it,
// so starting the gateway stays fast and a machine that cannot sandbox still runs everything
// else — with tools that refuse.
//
// ACORNFALL_NO_SANDBOX is that refusal on purpose, not a fallback: no engine is configured,
// nothing is installed or booted, and the worker's tools refuse. Tests use it so one command
// behaves the same on a machine with /dev/kvm and in CI, which auto-detecting did not.
func sandboxTier(ctx context.Context, home, state, engineName string) (*sandbox.Manager, *sandbox.Tier, error) {
	if os.Getenv("ACORNFALL_NO_SANDBOX") != "" {
		log.Printf("sandbox: disabled by ACORNFALL_NO_SANDBOX; commands and file tools will refuse")
		return nil, nil, nil
	}
	_ = ctx
	// Every engine this build has, one manager each, so a workspace can choose its own. The
	// default is the one --sandbox-engine names; a workspace that names one wins over it.
	tier := sandbox.NewTier(home, state, log.Printf)
	engine, err := tier.For(engineName)
	if err != nil {
		return nil, nil, err
	}
	if engineName == "" {
		// Not a silent choice: which engine a new workspace gets is a security decision, and the
		// reason is this machine's ability, so it is said rather than discovered in a record.
		log.Printf("sandbox: no engine named, and this machine can run %s, so that is the default (--sandbox-engine chooses another)", engine.Driver().Name())
	}
	report := engine.Report()
	if problem := engine.Problem(); problem != nil {
		log.Printf("sandbox: %v", problem)
		log.Printf("sandbox: commands and file tools will refuse until this is fixed; tools never run on the host")
	}
	log.Printf("sandbox: engine %s %s, state %s", report.Engine, report.Version, report.StateDir)
	if !report.Isolated {
		log.Printf("sandbox: the host engine is not isolated: commands and file tools run with this user's own reach")
	}
	return engine, tier, nil
}

// versionCommand prints the running tag: the version file, the build
// stamp, or dev. The update tier's exec gate runs it on staged binaries,
// and scripts check it without starting anything.
func versionCommand(argv []string) {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "data directory")
	if err := flags.Parse(argv); err != nil {
		return
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	version, err := versionString(dir)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	fmt.Println(version)
}

func versionString(home string) (string, error) {
	manager := update.New(home, "", "")
	if err := manager.Open(); err != nil {
		return "", err
	}
	return manager.Status().Current, nil
}

func providers(argv []string) {
	flags := flag.NewFlagSet("provider", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "data directory")
	if err := flags.Parse(argv); err != nil {
		return
	}
	args := flags.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: acornfall provider <list|add|remove>")
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	path := filepath.Join(dir, "credentials.json")
	switch args[0] {
	case "list":
		all := readCredentials(path)
		if len(all) == 0 {
			fmt.Println("no providers configured")
			return
		}
		for id, credential := range all {
			fmt.Printf("%-24s %s\n", id, credential.Type)
		}
	case "add":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: acornfall provider add <provider> <api-key>")
			os.Exit(2)
		}
		all := readCredentials(path)
		all[args[1]] = credential{Type: "api_key", Key: args[2]}
		if err := writeCredentials(path, all); err != nil {
			log.Fatalf("acornfall: %v", err)
		}
		fmt.Printf("stored api key for %s\n", args[1])
	case "remove":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: acornfall provider remove <provider>")
			os.Exit(2)
		}
		all := readCredentials(path)
		delete(all, args[1])
		if err := writeCredentials(path, all); err != nil {
			log.Fatalf("acornfall: %v", err)
		}
		fmt.Printf("removed %s\n", args[1])
	default:
		fmt.Fprintf(os.Stderr, "unknown provider command %q\n", args[0])
		os.Exit(2)
	}
}

// credential is the pi-ai api-key credential shape. Go never interprets other
// credential types; it only stores and returns them.
type credential struct {
	Type string `json:"type"`
	Key  string `json:"key,omitempty"`
}

func readCredentials(path string) map[string]credential {
	all := map[string]credential{}
	payload, err := os.ReadFile(path)
	if err != nil {
		return all
	}
	if err := json.Unmarshal(payload, &all); err != nil {
		log.Fatalf("acornfall: %s is not valid JSON: %v", path, err)
	}
	return all
}

func writeCredentials(path string, all map[string]credential) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(payload, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// resolveWorker picks the worker process and its directory, and whether
// the runtime is managed. An explicit --worker wins, then --worker-bin,
// then an explicit --worker-dir over the system bun (development), then
// the managed path: ensured bun over ensured sources, both under the
// install folder, nothing on the host.
func resolveWorker(explicit, dirFlag, binFlag string, updates *update.Manager) ([]string, string, bool) {
	if explicit != "" {
		worker, err := resolveWorkerDir(dirFlag)
		if err != nil {
			return strings.Fields(explicit), "", false
		}
		return strings.Fields(explicit), worker, false
	}
	bin := binFlag
	if bin == "" {
		bin = os.Getenv("ACORNFALL_WORKER_BIN")
	}
	if bin != "" {
		return []string{bin}, "", false
	}
	if dirFlag != "" {
		worker, err := resolveWorkerDir(dirFlag)
		if err != nil {
			return []string{"bun", "run", "src/main.ts"}, "", false
		}
		return []string{"bun", "run", "src/main.ts"}, worker, false
	}
	return []string{updates.BunPath(), "run", "src/main.ts"}, updates.WorkerDir(), true
}

// displayAddr turns a wildcard bind into something clickable.
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "0.0.0.0" + addr
	}
	return addr
}

// prettyAddr prefers the machine's own hostname over a wildcard address, so the
// printed URL is the one a remote browser can actually use.
func prettyAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		if name, err := os.Hostname(); err == nil && name != "" {
			return net.JoinHostPort(name, port)
		}
		return net.JoinHostPort("localhost", port)
	}
	return net.JoinHostPort(host, port)
}

/*
workspaceCommand is the operator's way to point a workspace at a folder of their choosing.
The interface cannot do this: it creates workspaces inside the workspaces folder by name,
which is what keeps it inside one directory. This edits the registry directly, and the
gateway picks the change up on its next start.
*/
func workspaceCommand(argv []string) {
	flags := flag.NewFlagSet("workspace", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	image := flags.String("image", "", "sandbox image for this workspace, e.g. golang:1.24 (default: "+sandbox.DefaultImage+")")
	network := flags.String("network", "", `sandbox network: "on" (default) or "off"`)
	engine := flags.String("sandbox", "", "sandbox engine for this workspace: microsandbox, bwrap, or host (default: the install's --sandbox-engine)")
	keepAlive := flags.Bool("keep-alive", false, "keep this workspace's sandbox running when nobody is watching it (for daemons and cron jobs)")
	privateNetworks := flags.Bool("private", false, "let this workspace's sandbox reach private networks as this machine: its LAN, link-local addresses, and its tailnet (default: public internet only)")
	// Flags after the name and the folder are documented in the usage below, and the standard
	// flag package stops at the first positional. Splitting them is what makes
	// `workspace add demo ~/work/demo --sandbox bwrap` mean what it says.
	flagArgs, positional := splitFlags(argv, map[string]bool{"data-dir": true, "image": true, "network": true, "sandbox": true})
	if err := flags.Parse(flagArgs); err != nil {
		return
	}
	args := positional
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: acornfall workspace <list|add|remove> [--sandbox ENGINE] [--image IMAGE] [--network on|off] [--private]")
		os.Exit(2)
	}
	if *network != "" && !control.ValidNetwork(*network) {
		fmt.Fprintln(os.Stderr, `acornfall: --network is "on" or "off"`)
		os.Exit(2)
	}
	if !sandbox.ValidEngine(*engine) {
		fmt.Fprintf(os.Stderr, "acornfall: --sandbox is one of %s\n", strings.Join(sandbox.EngineNames(), ", "))
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

	switch args[0] {
	case "list":
		if len(registry.Workspaces) == 0 {
			fmt.Println("no workspaces registered")
			return
		}
		for _, workspace := range registry.Workspaces {
			marker := " "
			if workspace.General {
				marker = "*"
			}
			fmt.Printf("%s %-16s %-14s %-22s %s\n", marker, workspace.Name, engineLabel(workspace.Engine), sandboxLabel(workspace), workspace.Path)
		}
		fmt.Printf("\nworkspaces folder: %s\n", registry.WorkspacesDir)
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: acornfall workspace add <name> [folder] [--sandbox ENGINE] [--image IMAGE] [--network on|off] [--private]")
			os.Exit(2)
		}
		name := args[1]
		folder := name
		if len(args) > 2 {
			folder = args[2]
		}
		absolute, err := filepath.Abs(folder)
		if err != nil {
			log.Fatalf("acornfall: %v", err)
		}
		if info, err := os.Stat(absolute); err != nil || !info.IsDir() {
			log.Fatalf("acornfall: no folder at %s", absolute)
		}
		for _, workspace := range registry.Workspaces {
			if workspace.Name == name {
				log.Fatalf("acornfall: workspace %q already exists", name)
			}
		}
		// A workspace records its engine: left blank it would mean "whatever this machine prefers",
		// and an unrecorded choice is one that changes if the workspace moves. The question is
		// asked here the same way the gateway asks it — what can this machine actually run.
		chosen := *engine
		if chosen == "" {
			chosen = sandbox.NewTier(dir, "", nil).Default()
		}
		registry.Workspaces = append(registry.Workspaces, control.Workspace{
			Name: name, Path: absolute, Title: name, Image: *image, Network: *network, Engine: chosen,
			KeepAlive: *keepAlive, Private: *privateNetworks,
		})
		if err := control.SaveRegistryFile(registry); err != nil {
			log.Fatalf("acornfall: %v", err)
		}
		fmt.Printf("added %s at %s\n", name, absolute)
		fmt.Printf("sandbox: %s, network %s, engine %s\n", imageLabel(*image), networkLabel(*network), engineLabel(chosen))
		if *privateNetworks {
			fmt.Println("private networks: reachable as this machine (its LAN and tailnet)")
		} else {
			fmt.Println("private networks: refused (public internet only)")
		}
	case "remove":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: acornfall workspace remove <name>")
			os.Exit(2)
		}
		kept := registry.Workspaces[:0]
		removed := false
		for _, workspace := range registry.Workspaces {
			if workspace.Name != args[1] {
				kept = append(kept, workspace)
				continue
			}
			if workspace.General {
				log.Fatalf("acornfall: the general workspace cannot be removed")
			}
			removed = true
		}
		if !removed {
			log.Fatalf("acornfall: no workspace %q", args[1])
		}
		registry.Workspaces = kept
		if err := control.SaveRegistryFile(registry); err != nil {
			log.Fatalf("acornfall: %v", err)
		}
		fmt.Printf("removed %s (its folder and history are untouched)\n", args[1])
	default:
		fmt.Fprintf(os.Stderr, "unknown workspace command %q\n", args[0])
		os.Exit(2)
	}
}

// sandboxLabel is what a workspace's sandbox will be, for the list column: short enough to
// scan, specific enough to answer "which image is this one running".
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

func sandboxLabel(workspace control.Workspace) string {
	image := workspace.Image
	if image == "" {
		image = sandbox.DefaultImage
	}
	network := "net on"
	if workspace.Network == control.NetworkOff {
		network = "net off"
	}
	return image + ", " + network
}

// engineLabel names the engine a workspace runs on. A workspace that named none says so: the
// engine it will run on is this machine's answer, and the gateway records it the first time it
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

func networkLabel(network string) string {
	if network == control.NetworkOff {
		return "off"
	}
	return "on (default)"
}

func resolveDataDir(flagValue string) (string, error) {
	if flagValue != "" {
		return filepath.Abs(flagValue)
	}
	if env := os.Getenv("ACORNFALL_HOME"); env != "" {
		return filepath.Abs(env)
	}
	if env := os.Getenv("ACORNFALL_DATA"); env != "" {
		return filepath.Abs(env)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".acornfall"), nil
}

// resolveWorkerDir finds the worker package without requiring configuration.
func resolveWorkerDir(flagValue string) (string, error) {
	candidates := []string{flagValue, os.Getenv("ACORNFALL_WORKER"), "./worker", "../worker"}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "..", "..", "worker"))
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(candidate, "src", "main.ts")); err == nil {
			return filepath.Abs(candidate)
		}
	}
	return "", fmt.Errorf("worker not found: pass --worker-dir or set ACORNFALL_WORKER")
}
