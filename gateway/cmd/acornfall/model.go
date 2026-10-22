package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// modelCommand is the headless half of Settings > Models: which model every workspace falls back
// to, and which provider keys this install has. The catalog of what each provider offers comes
// from the provider itself, through the worker, so it lives in the interface; what is decided
// here is the choice, which is one line in settings.json.
func modelCommand(argv []string) {
	if len(argv) > 0 {
		switch argv[0] {
		case "list":
			modelList(argv[1:])
			return
		case "set":
			modelSet(argv[1:])
			return
		case "clear":
			modelClear(argv[1:])
			return
		case "help", "-h", "--help":
			modelUsage(os.Stdout)
			return
		}
	}
	modelUsage(os.Stderr)
	os.Exit(2)
}

func modelUsage(w *os.File) {
	fmt.Fprint(w, `usage: acornfall model <command>

  list                 the default model and the provider keys this install has
  set <provider>/<id>  make it the default for every workspace
  clear                fall back to the provider's own choice

The default is below a workspace's own choice, and above the last resort. The models each
provider offers are listed in the interface, where the provider is asked.
`)
}

func modelList(argv []string) {
	flags := flag.NewFlagSet("model list", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	flagArgs, _ := splitFlags(argv, map[string]bool{"data-dir": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		logFatal(err)
	}
	defaults, err := control.LoadDefaults(modelSettingsPath(dir))
	if err != nil {
		logFatal(err)
	}
	if binding := defaults.Model; binding.Provider != "" {
		fmt.Printf("default model   %s/%s\n", binding.Provider, binding.ModelID)
	} else {
		fmt.Println("default model   none (each workspace, then the provider's own choice)")
	}
	if env := strings.TrimSpace(os.Getenv("ACORNFALL_MODEL")); env != "" {
		fmt.Printf("last resort     %s (ACORNFALL_MODEL)\n", env)
	}
	credentials := readCredentials(filepath.Join(dir, "credentials.json"))
	if len(credentials) == 0 {
		fmt.Println("provider keys   none")
		return
	}
	names := make([]string, 0, len(credentials))
	for name := range credentials {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("provider key    %s (%s)\n", name, credentials[name].Type)
	}
}

func modelSet(argv []string) {
	flags := flag.NewFlagSet("model set", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: acornfall model set <provider>/<modelId> [--data-dir DIR]\n")
	}
	flagArgs, positional := splitFlags(argv, map[string]bool{"data-dir": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	if len(positional) != 1 {
		flags.Usage()
		os.Exit(2)
	}
	provider, modelID, ok := strings.Cut(positional[0], "/")
	if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(modelID) == "" {
		fmt.Fprintf(os.Stderr, "acornfall model: %q is provider/modelId, e.g. anthropic/claude-sonnet-4-5\n", positional[0])
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		logFatal(err)
	}
	// The file is the install's own layer, and the gateway reads it on every turn: this is the
	// same write the interface makes, not a second place a default can live.
	path := modelSettingsPath(dir)
	defaults, err := control.LoadDefaults(path)
	if err != nil {
		logFatal(err)
	}
	defaults.Model = protocol.ModelBinding{Provider: provider, ModelID: modelID}
	if err := control.SaveDefaults(path, defaults); err != nil {
		logFatal(err)
	}
	fmt.Printf("default model is now %s/%s\n", provider, modelID)
}

func modelClear(argv []string) {
	flags := flag.NewFlagSet("model clear", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	if err := flags.Parse(argv); err != nil {
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		logFatal(err)
	}
	path := modelSettingsPath(dir)
	defaults, err := control.LoadDefaults(path)
	if err != nil {
		logFatal(err)
	}
	defaults.Model = protocol.ModelBinding{}
	if err := control.SaveDefaults(path, defaults); err != nil {
		logFatal(err)
	}
	fmt.Println("default model cleared: each workspace decides, then the provider's own choice")
}

func modelSettingsPath(dir string) string {
	return filepath.Join(dir, "settings.json")
}

func logFatal(err error) {
	fmt.Fprintf(os.Stderr, "acornfall: %v\n", err)
	os.Exit(1)
}
