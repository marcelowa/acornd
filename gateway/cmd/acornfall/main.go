package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	args := os.Args[1:]
	if len(args) == 0 {
		// Nothing is a guess, and starting a server is too big a guess: a command is named.
		usage(os.Stderr)
		os.Exit(2)
	}
	command, known := commands[args[0]]
	if !known {
		fmt.Fprintf(os.Stderr, "acornfall: no such command %q\n\n", args[0])
		usage(os.Stderr)
		os.Exit(2)
	}
	command(args[1:])
}

// commands is the whole surface: a name and what it does. A table rather than a switch because
// the list is the interface, and side by side with `usage` below, a command that exists in one and
// not the other is visible.
var commands = map[string]func([]string){
	"start": func(args []string) {
		if err := serve(args); err != nil {
			log.Fatalf("acornfall: %v", err)
		}
	},
	"setup":     setupCommand,
	"doctor":    doctorCommand,
	"workspace": workspaceCommand,
	"secret":    secretCommand,
	"provider":  providers,
	"model":     modelCommand,
	"people":    peopleCommand,
	"token":     tokenCommand,
	"update":    updateCommand,
	"version":   versionCommand,
	"internal":  internalCommand,
	"help":      func([]string) { usage(os.Stdout) },
	"-h":        func([]string) { usage(os.Stdout) },
	"--help":    func([]string) { usage(os.Stdout) },
}

// internalCommand dispatches the verbs that binaries call. An unknown one is refused like any
// other unknown command, because a typo in a driver should say so.
func internalCommand(argv []string) {
	if len(argv) > 0 {
		switch argv[0] {
		case "netns":
			netnsCommand(argv[1:])
			return
		case "sandbox-supervisor":
			supervisorCommand(argv[1:])
			return
		}
	}
	fmt.Fprintln(os.Stderr, "usage: acornfall internal <netns|sandbox-supervisor> [flags]")
	os.Exit(2)
}

// usage is the whole command surface on one screen: a verb and what it is for. The flags are
// per command, because a flag that belongs to one command should not look like it belongs to
// all of them.
func usage(w io.Writer) {
	fmt.Fprint(w, `usage: acornfall <command> [flags]

  start      run the gateway
  setup      check this machine, and make it able to run sandboxes
  doctor     report what the sandbox engines can do here; changes nothing
  workspace  list, add, and remove workspaces
  secret     list, add, show, rotate, and remove secrets
  provider   list, add, and remove model provider keys
  model      list or choose the default model
  people     who may use this install, and what they may do
  token      print the token that proves you own this install
  update     check for a release, and install it
  version    print the running version

acornfall <command> -h    explains that command's flags
`)
}

func serve(argv []string) error {
	flags := flag.NewFlagSet("acornfall start", flag.ContinueOnError)
	host := flags.String("host", "0.0.0.0", "interface to listen on (default: every interface)")
	port := flags.Int("port", 7777, "port to listen on")
	dataDir := flags.String("data-dir", "", "the acornfall folder: config, credentials, state, workspaces (default: ~/.acornfall)")
	workspacesDir := flags.String("workspaces-dir", "", "where workspaces are created (default: ~/acornfall/workspaces)")
	workerCommand := flags.String("worker", "", `worker command (default: ensured bun over ensured sources)`)
	if err := flags.Parse(argv); err != nil {
		return err
	}

	install, err := openInstall(*dataDir)
	if err != nil {
		return err
	}
	dir, userHome, secrets := install.dir, install.userHome, install.vault
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The self-update tier, owned early: boot readiness (migrate, bun, worker
	// sources) is decided before anything serves.
	updates, err := bootUpdates(ctx, dir)
	if err != nil {
		return err
	}
	command, worker, workerManaged := resolveWorker(*workerCommand, updates)
	ensureWorkerSources(ctx, updates, workerManaged)

	// The sandbox tier. One engine per install, and the worker is told which one, because it
	// attaches with that engine's client and the two must agree. Nothing here chooses for a
	// machine: naming the host engine is what makes a run unconfined.
	// The engine is a property of a workspace, recorded when the workspace is created. This
	// install-wide name exists for tests and for a machine that must be told; the environment is
	// where it lives now, because a flag on the server is a decision nobody meant to make once
	// per boot.
	engineName := os.Getenv("ACORNFALL_SANDBOX_ENGINE")
	engine, tier, err := sandboxTier(ctx, dir, engineName)
	if err != nil {
		return err
	}

	go updates.StartLoop(ctx)

	// The gate is opened before the registry, because the registry needs one answer out of it: whose the
	// workspaces that predate people are. Control is told an address, not a file — it knows what a
	// workspace is and not what a people file is.
	gate, err := openGate(dir, *host)
	if err != nil {
		return err
	}
	workspaces := newWorkspaces(dir, userHome, *workspacesDir, command, worker, updates, secrets, engine, tier, ownerEmail(gate.People))
	if err := workspaces.Open(ctx); err != nil {
		return err
	}
	defer workspaces.Close()

	wireUpdates(updates, workspaces, engine)

	logReadiness(ready.Check(readyInputForBoot(dir, updates, engine)))

	return listen(ctx, *host, *port, httpapi.New(workspaces, web.Files, updates, gate).Handler(), dir)
}

// newWorkspaces is the workspace layer as this boot configured it: where its folders are, what
// it starts workers with, and the two tiers it may reach.
func newWorkspaces(
	dir, userHome, workspacesDir string,
	command []string, worker string,
	updates *update.Manager,
	secrets *vault.Store,
	engine *sandbox.Manager,
	tier *sandbox.Tier,
	owner string,
) *control.Workspaces {
	return control.NewWorkspaces(control.WorkspacesOptions{
		DataDir:       dir,
		UserHome:      userHome,
		WorkspacesDir: workspacesDir,
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
		Model:      os.Getenv("ACORNFALL_MODEL"),
		Sandbox:    engine,
		Sandboxes:  tier,
		Vault:      secrets,
		OwnerEmail: owner,
	})
}

// install is where this install lives, opened: its folder, the user's home the sandbox mounts
// from, and the vault.
type install struct {
	dir      string
	userHome string
	vault    *vault.Store
}

// openInstall resolves the install once, and creates the folders a sandbox will mount from. The
// user's home is resolved here rather than inside control: the sandbox mounts `~/.agents` at the
// same path the worker reads it from, and both have to be answering about the same folder.
func openInstall(dataDir string) (install, error) {
	dir, err := resolveDataDir(dataDir)
	if err != nil {
		return install{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return install{}, err
	}
	// The vault is install-wide: one file beside credentials.json, never inside a workspace and
	// never mounted into a sandbox.
	store, err := vault.Open(filepath.Join(dir, "secrets.json"))
	if err != nil {
		return install{}, err
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return install{}, fmt.Errorf("cannot tell where the user's home is: %w", err)
	}
	if err := sandbox.EnsureGlobal(dir, userHome); err != nil {
		return install{}, err
	}
	return install{dir: dir, userHome: userHome, vault: store}, nil
}

// bootUpdates opens the self-update tier and runs whatever migrate is pending. A failing migrate
// stops boot and rolls back: the supervisor restarts the last good build instead of a dead UI.
func bootUpdates(ctx context.Context, dir string) (*update.Manager, error) {
	updates := update.New(dir, "")
	if err := updates.Open(); err != nil {
		return nil, err
	}
	if err := updates.RunPending(ctx); err != nil {
		return nil, err
	}
	return updates, nil
}

// ensureWorkerSources fetches bun and the worker's sources when this install manages them. Best
// effort, never fatal: a missing toolchain with no network is a loud refusal at workspace open,
// not a bricked boot.
func ensureWorkerSources(ctx context.Context, updates *update.Manager, managed bool) {
	if !managed || updates.Status().Current == "dev" {
		return
	}
	if _, err := updates.EnsureBun(ctx); err != nil {
		log.Printf("worker: %v", err)
	}
	if err := updates.EnsureWorker(ctx, updates.RuntimeTag()); err != nil {
		log.Printf("worker: %v", err)
	}
}

// logReadiness is readiness, plainly: every unsatisfied requirement is one log line with its fix.
// Auto-installable pieces were ensured above; what is still missing refuses loudly at use, never
// silently.
func logReadiness(requirements []ready.Requirement) {
	for _, requirement := range requirements {
		if requirement.OK {
			continue
		}
		log.Printf("ready: %s not satisfied: %s (fix: %s)", requirement.Name, requirement.Detail, requirement.Fix)
	}
}

// wireUpdates is what the update tier may do to this install: it may ask what is running, stop
// it, and clean the sandboxes a rollback orphaned. The shapes cross the layer boundary as this
// tier's own, so neither side learns the other's types.
func wireUpdates(updates *update.Manager, workspaces *control.Workspaces, engine *sandbox.Manager) {
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
		names := make([]string, 0, len(workspaces.List()))
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
}

// listen serves until the owner stops, shutting down within five seconds and no later.
func listen(ctx context.Context, host string, port int, handler http.Handler, dir string) error {
	server := &http.Server{
		Addr:              net.JoinHostPort(host, strconv.Itoa(port)),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		// The cancellation that got here is the one this budget must outlive, so it is taken from a
		// copy of the context that keeps its values and not its deadline: a plain Background would
		// drop the request's own logging and tracing on the way out.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	// Listen explicitly so the line below reports the address that was actually bound: with
	// a port of 0 the configured address says nothing about where to reach it.
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	bound := listener.Addr().String()
	log.Printf("acornfall listening on http://%s  home=%s", displayAddr(bound), dir)
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
func sandboxTier(ctx context.Context, dataDir, engineName string) (*sandbox.Manager, *sandbox.Tier, error) {
	if os.Getenv("ACORNFALL_NO_SANDBOX") != "" {
		log.Printf("sandbox: disabled by ACORNFALL_NO_SANDBOX; commands and file tools will refuse")
		return nil, nil, nil
	}
	_ = ctx
	// Every engine this build has, one manager each, so a workspace can choose its own. The
	// default is what this machine can run; a workspace that recorded an engine wins over it.
	tier := sandbox.NewTier(dataDir, "", log.Printf)
	engine, err := tier.For(engineName)
	if err != nil {
		return nil, nil, err
	}
	if engineName == "" {
		// Not a silent choice: which engine a new workspace gets is a security decision, and the
		// reason is this machine's ability, so it is said rather than discovered in a record.
		// %q, because the driver's name comes from the configuration and a newline in it would end
		// the line early and start a record nobody wrote. gosec's taint analysis follows the value
		// and not the verb, so the escaping it cannot see is named here.
		// #nosec G706 -- %q escapes the control characters a log injection needs.
		log.Printf("sandbox: no engine named, and this machine can run %q, so that is the default (each workspace keeps the engine it recorded)", engine.Driver().Name())
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
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	flagArgs, _ := splitFlags(argv, map[string]bool{"data-dir": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
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

func versionString(dataDir string) (string, error) {
	manager := update.New(dataDir, "")
	if err := manager.Open(); err != nil {
		return "", err
	}
	return manager.Status().Current, nil
}

func providers(argv []string) {
	flags := flag.NewFlagSet("provider", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	flagArgs, positional := splitFlags(argv, map[string]bool{"data-dir": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	args := positional
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
		providerList(path)
	case "add":
		providerAdd(path, args[1:])
	case "remove":
		providerRemove(path, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown provider command %q\n", args[0])
		os.Exit(2)
	}
}

// providerList is one line per provider: the id, and how this install holds it. Never the key.
func providerList(path string) {
	all := readCredentials(path)
	if len(all) == 0 {
		fmt.Println("no providers configured")
		return
	}
	for id, credential := range all {
		fmt.Printf("%-24s %s\n", id, credential.Type)
	}
}

func providerAdd(path string, args []string) {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, `usage: acornfall provider add <provider> [<api-key>]

With the key in argv the value is in the shell history and in every process listing; with the
key left out it is read from stdin, which is what a script should do.`)
		os.Exit(2)
	}
	key, err := apiKey(args)
	if err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	if key == "" {
		log.Fatalf("acornfall: no api key for %s", args[0])
	}
	all := readCredentials(path)
	all[args[0]] = credential{Type: "api_key", Key: key}
	if err := writeCredentials(path, all); err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	fmt.Printf("stored api key for %s\n", args[0])
}

// apiKey is argv when it was given there, and stdin when it was not: a key on a command line is
// in the shell history and in every process listing on the machine.
func apiKey(args []string) (string, error) {
	if len(args) == 2 {
		return args[1], nil
	}
	payload, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(payload)), nil
}

func providerRemove(path string, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: acornfall provider remove <provider>")
		os.Exit(2)
	}
	all := readCredentials(path)
	delete(all, args[0])
	if err := writeCredentials(path, all); err != nil {
		log.Fatalf("acornfall: %v", err)
	}
	fmt.Printf("removed %s\n", args[0])
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
func resolveWorker(explicit string, updates *update.Manager) ([]string, string, bool) {
	if explicit != "" {
		worker, err := resolveWorkerDir("")
		if err != nil {
			return strings.Fields(explicit), "", false
		}
		return strings.Fields(explicit), worker, false
	}
	if dir := os.Getenv("ACORNFALL_WORKER"); dir != "" {
		worker, err := resolveWorkerDir(dir)
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

func resolveDataDir(flagValue string) (string, error) {
	if flagValue != "" {
		return filepath.Abs(flagValue)
	}
	if env := os.Getenv("ACORNFALL_DATA_DIR"); env != "" {
		return filepath.Abs(env)
	}
	// The two older names, read only so an update can happen: the updater that stages a new
	// binary runs it to ask its version, and the updater doing that is the one already on disk.
	// A v0.14 install therefore points a v0.15 binary at an empty folder with ACORNFALL_HOME,
	// and without this the binary would answer about the live folder and be refused as the
	// wrong version. Nothing but an updater should set them; they go when v0.14 is out of
	// support (ADR 0027).
	for _, legacy := range []string{"ACORNFALL_HOME", "ACORNFALL_DATA"} {
		if env := os.Getenv(legacy); env != "" {
			return filepath.Abs(env)
		}
	}
	// The user's own home, not this install's folder: the two are different answers and the
	// same word, which is why the install folder is called dataDir everywhere now.
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".acornfall"), nil
}

// resolveWorkerDir finds the worker package without requiring configuration.
func resolveWorkerDir(flagValue string) (string, error) {
	candidates := []string{flagValue, "./worker", "../worker"}
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
