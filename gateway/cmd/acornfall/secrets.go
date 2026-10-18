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

	"github.com/acornfall/acornfall/gateway/internal/vault"
)

// secretCommand is the headless way to set a tool secret. The value comes from stdin,
// never argv: a value in argv is in the shell history and in every process listing.
func secretCommand(argv []string) {
	if len(argv) == 0 {
		secretUsage()
		os.Exit(2)
	}
	command := argv[0]
	flags := flag.NewFlagSet("secret", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	global := flags.Bool("global", false, "this secret is on every workspace")
	workspace := flags.String("workspace", "", "this secret belongs to this workspace folder")
	description := flags.String("description", "", "what the secret is for, shown in lists")
	hosts := flags.String("hosts", "", "comma-separated hosts the secret may reach")
	query := flags.Bool("query", false, "allow substitution in the URL query")
	body := flags.Bool("body", false, "allow substitution in the request body")
	noHeaders := flags.Bool("no-headers", false, "refuse substitution in request headers")
	allowEnv := flags.Bool("allow-env", true, "let a container engine put the value in the sandbox")
	noEnv := flags.Bool("no-env", false, "keep the value out of a container sandbox")
	if err := flags.Parse(argv[1:]); err != nil {
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		secretFail(err)
	}
	store, err := vault.Open(filepath.Join(dir, "secrets.json"))
	if err != nil {
		secretFail(err)
	}

	switch command {
	case "list":
		secretList(store, *workspace)
	case "add":
		name, scope, scopePath, err := secretTarget(flags, *global, *workspace)
		if err != nil {
			secretFail(err)
		}
		if strings.TrimSpace(*description) == "" {
			secretFail(errors.New("--description is required: what is this secret for?"))
		}
		value, err := readSecretValue()
		if err != nil {
			secretFail(err)
		}
		substitution := vault.Substitution{
			Headers: !*noHeaders,
			Query:   *query,
			Body:    *body,
		}
		if substitution == (vault.Substitution{}) {
			secretFail(errors.New("allow at least one substitution location: headers, --query, or --body"))
		}
		hostList := splitHosts(*hosts)
		envAllowed := *allowEnv && !*noEnv
		meta, err := store.Upsert(vault.Upsert{
			Name:         name,
			Description:  *description,
			Scope:        scope,
			Workspace:    scopePath,
			Value:        value,
			Hosts:        &hostList,
			Substitution: &substitution,
			AllowEnv:     &envAllowed,
		})
		if err != nil {
			secretFail(err)
		}
		fmt.Printf("stored %s (%s)\n", meta.Name, describeScope(meta))
	case "rotate":
		name, scope, scopePath, err := secretTarget(flags, *global, *workspace)
		if err != nil {
			secretFail(err)
		}
		value, err := readSecretValue()
		if err != nil {
			secretFail(err)
		}
		meta, err := store.Rotate(scope, scopePath, name, value)
		if err != nil {
			secretFail(err)
		}
		fmt.Printf("rotated %s (%s)\n", meta.Name, describeScope(meta))
	case "rm":
		name, scope, scopePath, err := secretTarget(flags, *global, *workspace)
		if err != nil {
			secretFail(err)
		}
		if err := store.Delete(scope, scopePath, name); err != nil {
			secretFail(err)
		}
		fmt.Printf("removed %s\n", name)
	default:
		secretUsage()
		os.Exit(2)
	}
}

func secretUsage() {
	fmt.Fprintln(os.Stderr, `usage:
  acornfall secret list [--workspace <folder>]
  acornfall secret add --global|--workspace <folder> --description <text> [--hosts a,b] [--query] [--body] [--no-headers] [--no-env] <NAME> < value
  acornfall secret rotate --global|--workspace <folder> <NAME> < value
  acornfall secret rm --global|--workspace <folder> <NAME>

The value is read from stdin, never argv. It is never printed again.`)
}

func secretFail(err error) {
	fmt.Fprintf(os.Stderr, "acornfall secret: %v\n", err)
	os.Exit(1)
}

// secretTarget reads the name and the one scope the command names. A global secret and a
// workspace secret are different entries, so exactly one scope is required.
func secretTarget(flags *flag.FlagSet, global bool, workspace string) (string, vault.Scope, string, error) {
	if flags.NArg() != 1 {
		return "", "", "", errors.New("name one secret; see acornfall secret")
	}
	name := flags.Arg(0)
	if global == (strings.TrimSpace(workspace) != "") {
		return "", "", "", errors.New("name one scope: --global or --workspace <folder>")
	}
	if global {
		return name, vault.Global, "", nil
	}
	path, err := filepath.Abs(workspace)
	if err != nil {
		return "", "", "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", "", fmt.Errorf("workspace %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", "", "", fmt.Errorf("workspace %s is not a folder", path)
	}
	return name, vault.Workspace, path, nil
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

func secretList(store *vault.Store, workspace string) {
	if strings.TrimSpace(workspace) != "" {
		path, err := filepath.Abs(workspace)
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
			meta.Name, meta.Scope, dash(meta.Workspace), meta.Description,
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
	return "workspace " + meta.Workspace
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
