package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/vault"
)

// secretCommand is the headless way to set a tool secret. The value comes from stdin,
// never argv: a value in argv is in the shell history and in every process listing. What a
// secret *is* (its scope, hosts, and substitution) is printed by `show`, without the value,
// which is the one thing that never comes back out.
func secretCommand(argv []string) {
	if len(argv) > 0 {
		switch argv[0] {
		case "list":
			secretList(argv[1:])
			return
		case "add":
			secretAdd(argv[1:])
			return
		case "show":
			secretShow(argv[1:])
			return
		case "rotate":
			secretRotate(argv[1:])
			return
		case "remove":
			secretRemove(argv[1:])
			return
		case "help", "-h", "--help":
			secretUsage(os.Stdout)
			return
		}
	}
	secretUsage(os.Stderr)
	os.Exit(2)
}

func secretUsage(w *os.File) {
	fmt.Fprint(w, `usage: acornfall secret <command>

  list                  every secret; --workspace NAME for what one workspace receives
  add <NAME>            store one, from stdin: --global or --workspace NAME, --description,
                        --hosts a,b, --query, --body, --no-headers, --no-env
  show <NAME>           everything about it except the value
  rotate <NAME>         replace the value, from stdin
  remove <NAME>         delete it

acornfall secret <command> -h    explains that command's flags

The value is read from stdin, never argv. It is never printed again.`)
}

// secretScopeFlag is the one scope every verb takes, and the error that says so.
type secretScope struct {
	global    *bool
	workspace *string
}

func (s *secretScope) register(flags *flag.FlagSet) {
	s.global = flags.Bool("global", false, "this secret is on every workspace")
	s.workspace = flags.String("workspace", "", "this secret belongs to this workspace (by name)")
}

// resolve names the folder the secret belongs to. A workspace is named, not pathed: the
// registry is the one place that decides where a workspace lives.
func (s secretScope) resolve(dir string) (vault.Scope, string, error) {
	name := strings.TrimSpace(*s.workspace)
	switch {
	case *s.global && name != "":
		return "", "", errors.New("name one scope: --global or --workspace NAME, not both")
	case !*s.global && name == "":
		return "", "", errors.New("name one scope: --global or --workspace NAME")
	case *s.global:
		return vault.Global, "", nil
	}
	path, err := workspacePath(dir, name)
	if err != nil {
		return "", "", err
	}
	return vault.Workspace, path, nil
}

// workspacePath finds a workspace's folder by its name.
func workspacePath(dir, name string) (string, error) {
	registry, err := control.LoadRegistryFile(filepath.Join(dir, "workspaces.json"))
	if err != nil {
		return "", err
	}
	for _, workspace := range registry.Workspaces {
		if workspace.Name == name {
			return workspace.Path, nil
		}
	}
	names := make([]string, 0, len(registry.Workspaces))
	for _, workspace := range registry.Workspaces {
		names = append(names, workspace.Name)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no workspace %q: none are registered yet", name)
	}
	return "", fmt.Errorf("no workspace %q; there is %s", name, strings.Join(names, ", "))
}

// secretAdd stores a value. The description is required because a list of names with no
// sentence beside them is a list nobody can choose from.
// addFlags is what `secret add` accepts, after parsing: the flags a person typed, still as
// pointers because "not mentioned" is a value the defaults depend on.
type addFlags struct {
	dataDir     string
	description string
	hosts       string
	query       bool
	body        bool
	noHeaders   bool
	noEnv       bool
}

func secretAdd(argv []string) {
	flags := flag.NewFlagSet("secret add", flag.ContinueOnError)
	scope := secretScope{}
	scope.register(flags)
	values := addFlags{}
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	flags.StringVar(&values.description, "description", "", "what the secret is for, shown in lists")
	flags.StringVar(&values.hosts, "hosts", "", "comma-separated hosts the secret may reach")
	flags.BoolVar(&values.query, "query", false, "allow substitution in the URL query")
	flags.BoolVar(&values.body, "body", false, "allow substitution in the request body")
	flags.BoolVar(&values.noHeaders, "no-headers", false, "refuse substitution in request headers")
	flags.BoolVar(&values.noEnv, "no-env", false, "keep the value out of a container sandbox")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: acornfall secret add <NAME> < value [flags]

  --global | --workspace NAME   which workspaces receive it (exactly one)
  --description TEXT            what it is for, shown in lists
  --hosts a,b                   the hosts it may reach; anything else gets no value
  --query --body --no-headers   where in the request it may be substituted
  --no-env                      keep the value out of a container sandbox
  --data-dir DIR                the acornfall folder (default: ~/.acornfall)
`)
	}
	flagArgs, positional := splitFlags(argv, map[string]bool{
		"data-dir": true, "workspace": true, "description": true, "hosts": true,
	})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	name, ok := oneName(positional)
	if !ok {
		flags.Usage()
		os.Exit(2)
	}
	values.dataDir = *dataDir

	dir, store := openVault(values.dataDir)
	kind, folder, err := scope.resolve(dir)
	if err != nil {
		secretFail(err)
	}
	request, err := values.request(name, kind, folder)
	if err != nil {
		secretFail(err)
	}
	meta, err := store.Upsert(request)
	if err != nil {
		secretFail(err)
	}
	fmt.Printf("stored %s (%s)\n", meta.Name, describeScope(meta))
}

// request turns the flags into the vault's own shape, refusing what the vault would refuse later
// with a worse message: no description, no substitution location, no value.
func (f addFlags) request(name string, kind vault.Scope, folder string) (vault.Upsert, error) {
	if strings.TrimSpace(f.description) == "" {
		return vault.Upsert{}, errors.New("--description is required: what is this secret for?")
	}
	substitution := vault.Substitution{Headers: !f.noHeaders, Query: f.query, Body: f.body}
	if substitution == (vault.Substitution{}) {
		return vault.Upsert{}, errors.New("allow at least one substitution location: headers, --query, or --body")
	}
	value, err := readSecretValue()
	if err != nil {
		return vault.Upsert{}, err
	}
	hosts := splitHosts(f.hosts)
	envAllowed := !f.noEnv
	return vault.Upsert{
		Name:         name,
		Description:  f.description,
		Scope:        kind,
		Workspace:    folder,
		Value:        value,
		Hosts:        &hosts,
		Substitution: &substitution,
		AllowEnv:     &envAllowed,
	}, nil
}

func secretRotate(argv []string) {
	flags := flag.NewFlagSet("secret rotate", flag.ContinueOnError)
	scope := secretScope{}
	scope.register(flags)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: acornfall secret rotate <NAME> < value [--global|--workspace NAME]\n\nThe value is read from stdin; nothing else changes.\n")
	}
	flagArgs, positional := splitFlags(argv, map[string]bool{"data-dir": true, "workspace": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	name, ok := oneName(positional)
	if !ok {
		flags.Usage()
		os.Exit(2)
	}
	dir, store := openVault(*dataDir)
	kind, folder, err := scope.resolve(dir)
	if err != nil {
		secretFail(err)
	}
	value, err := readSecretValue()
	if err != nil {
		secretFail(err)
	}
	meta, err := store.Rotate(kind, folder, name, value)
	if err != nil {
		secretFail(err)
	}
	fmt.Printf("rotated %s (%s)\n", meta.Name, describeScope(meta))
}

func secretRemove(argv []string) {
	flags := flag.NewFlagSet("secret remove", flag.ContinueOnError)
	scope := secretScope{}
	scope.register(flags)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: acornfall secret remove <NAME> [--global|--workspace NAME]\n")
	}
	flagArgs, positional := splitFlags(argv, map[string]bool{"data-dir": true, "workspace": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	name, ok := oneName(positional)
	if !ok {
		flags.Usage()
		os.Exit(2)
	}
	dir, store := openVault(*dataDir)
	kind, folder, err := scope.resolve(dir)
	if err != nil {
		secretFail(err)
	}
	if err := store.Delete(kind, folder, name); err != nil {
		secretFail(err)
	}
	fmt.Printf("removed %s\n", name)
}

// secretShow prints what is stored for a name — every scope it exists in, since the same name
// can be global and per workspace — and never the value. It is the answer to "which hosts does
// this hold a value for", which is the question a secret raises.
func secretShow(argv []string) {
	flags := flag.NewFlagSet("secret show", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: acornfall secret show <NAME>\n\nThe value is not printed, or stored anywhere it could be printed.\n")
	}
	flagArgs, positional := splitFlags(argv, map[string]bool{"data-dir": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	name, ok := oneName(positional)
	if !ok {
		flags.Usage()
		os.Exit(2)
	}
	_, store := openVault(*dataDir)
	all, err := store.List()
	if err != nil {
		secretFail(err)
	}
	found := 0
	for _, meta := range all {
		if meta.Name != name {
			continue
		}
		found++
		scope := "global"
		if meta.Scope != vault.Global {
			scope = "workspace " + dash(workspaceNameOf(meta.Workspace))
		}
		fmt.Printf("%s (%s)\n", meta.Name, scope)
		fmt.Printf("  description    %s\n", dash(meta.Description))
		fmt.Printf("  hosts          %s\n", dash(strings.Join(meta.Hosts, ", ")))
		fmt.Printf("  substitution   %s\n", substitutionLabel(meta.Substitution))
		fmt.Printf("  container env  %s\n", yesNo(meta.AllowEnv))
		fmt.Printf("  rotated        %s\n", stamp(meta.RotatedAt))
		if meta.Overridden {
			fmt.Printf("  note           a workspace entry with this name replaces it there\n")
		}
		if meta.Overrides {
			fmt.Printf("  note           this replaces the global entry here\n")
		}
		fmt.Println()
	}
	if found == 0 {
		secretFail(fmt.Errorf("no secret named %q", name))
	}
}

// workspaceNameOf is the folder's name, which is what a person called it. The vault stores the
// path because a workspace can be moved; a name is how it is referred to.
func workspaceNameOf(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}

func substitutionLabel(substitution vault.Substitution) string {
	var parts []string
	if substitution.Headers {
		parts = append(parts, "headers")
	}
	if substitution.Query {
		parts = append(parts, "query")
	}
	if substitution.Body {
		parts = append(parts, "body")
	}
	if len(parts) == 0 {
		return "none (the value is only ever stored)"
	}
	return strings.Join(parts, ", ")
}

func secretList(argv []string) {
	flags := flag.NewFlagSet("secret list", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	workspace := flags.String("workspace", "", "show what this workspace receives, by name")
	flagArgs, _ := splitFlags(argv, map[string]bool{"data-dir": true, "workspace": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	dir, store := openVault(*dataDir)
	if strings.TrimSpace(*workspace) != "" {
		path, err := workspacePath(dir, *workspace)
		if err != nil {
			secretFail(err)
		}
		view, err := store.ForWorkspace(path)
		if err != nil {
			secretFail(err)
		}
		printEffective(view.Effective)
		return
	}
	all, err := store.List()
	if err != nil {
		secretFail(err)
	}
	if len(all) == 0 {
		fmt.Println("no secrets configured")
		return
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tSCOPE\tWORKSPACE\tDESCRIPTION\tHOSTS\tENV\tROTATED")
	for _, meta := range all {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			meta.Name, meta.Scope, dash(workspaceNameOf(meta.Workspace)), meta.Description,
			dash(strings.Join(meta.Hosts, ",")), yesNo(meta.AllowEnv), stamp(meta.RotatedAt))
	}
	_ = writer.Flush()
}

func printEffective(effective []vault.Meta) {
	if len(effective) == 0 {
		fmt.Println("no secrets for this workspace")
		return
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tSOURCE\tDESCRIPTION\tHOSTS\tENV\tROTATED\tNOTE")
	for _, meta := range effective {
		note := ""
		if meta.Overrides {
			note = "overrides global"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			meta.Name, meta.Source, meta.Description,
			dash(strings.Join(meta.Hosts, ",")), yesNo(meta.AllowEnv), stamp(meta.RotatedAt), note)
	}
	_ = writer.Flush()
}

func openVault(dataDirFlag string) (string, *vault.Store) {
	dir, err := resolveDataDir(dataDirFlag)
	if err != nil {
		secretFail(err)
	}
	store, err := vault.Open(filepath.Join(dir, "secrets.json"))
	if err != nil {
		secretFail(err)
	}
	return dir, store
}

// oneName is the single positional every verb but list takes.
func oneName(positional []string) (string, bool) {
	if len(positional) != 1 {
		return "", false
	}
	return positional[0], true
}

func secretFail(err error) {
	fmt.Fprintf(os.Stderr, "acornfall secret: %v\n", err)
	os.Exit(1)
}

// readSecretValue reads one value from stdin and drops the newline the shell or a file
// usually adds. It refuses an empty value rather than storing one nobody can use.
func readSecretValue() (string, error) {
	payload, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return "", err
	}
	value := strings.TrimRight(string(payload), "\r\n")
	if value == "" {
		return "", errors.New("empty value on stdin")
	}
	return value, nil
}

func splitHosts(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	hosts := make([]string, 0, len(parts))
	for _, part := range parts {
		if host := strings.TrimSpace(part); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func describeScope(meta vault.Meta) string {
	if meta.Scope == vault.Global {
		return "global"
	}
	return "workspace " + workspaceNameOf(meta.Workspace)
}

func dash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func stamp(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format("2006-01-02 15:04")
}
