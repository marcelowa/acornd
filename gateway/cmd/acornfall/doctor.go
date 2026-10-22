package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/ready"
	"github.com/acornfall/acornfall/gateway/internal/sandbox"
	"github.com/acornfall/acornfall/gateway/internal/update"
)

// doctorCommand reports what this machine can do, and changes nothing: the difference
// between a diagnosis and a repair is the whole point of running it. Every engine is asked,
// because "which one can run here" is the question, and answering it for one engine at a time
// is how a machine gets described by whichever flag was remembered.
func doctorCommand(argv []string) {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder (default: ~/.acornfall)")
	asJSON := flags.Bool("json", false, "print the report as JSON")
	flagArgs, _ := splitFlags(argv, map[string]bool{"data-dir": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	resolved, err := resolveDataDir(*dataDir)
	if err != nil {
		log.Fatalf("acornfall doctor: %v", err)
	}

	engines := sandboxReports(resolved)
	defaultEngine := sandbox.NewTier(resolved, "", nil).Default()
	var chosen sandbox.Report
	for _, engine := range engines {
		if engine.Default {
			chosen = engine.Report
		}
	}
	updater := update.New(resolved, "")
	if err := updater.Open(); err != nil {
		log.Fatalf("acornfall doctor: %v", err)
	}
	// Preflight never blocks on the network: an offline machine still gets
	// every local answer, and the release check degrades to a sentence.
	checkCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	health := updater.Preflight(checkCtx)
	requirements := ready.Check(readyInput(resolved, updater, health, chosen, defaultEngine))
	if *asJSON {
		payload, err := json.MarshalIndent(struct {
			Engines   []engineReport      `json:"engines"`
			Default   string              `json:"default"`
			Update    update.Preflight    `json:"update"`
			Readiness []ready.Requirement `json:"readiness"`
		}{Engines: engines, Default: defaultEngine, Update: health, Readiness: requirements}, "", "  ")
		if err != nil {
			log.Fatalf("acornfall doctor: %v", err)
		}
		fmt.Println(string(payload))
	} else {
		printEngines(engines, defaultEngine)
		fmt.Println("update")
		fmt.Printf("  version   %s\n", health.Version)
		fmt.Printf("  worker    %s\n", health.Worker)
		fmt.Printf("  bun       %s\n", health.Bun)
		if health.Latest != "" {
			fmt.Printf("  releases  %s (latest)\n", health.Latest)
		} else {
			fmt.Printf("  releases  %s\n", health.Releases)
		}
		fmt.Println("readiness")
		for _, requirement := range requirements {
			if requirement.OK {
				fmt.Printf("  %-11s yes %s\n", requirement.Name, requirement.Detail)
				continue
			}
			fmt.Printf("  %-11s NO  %s\n", requirement.Name, requirement.Detail)
			fmt.Printf("  %-11s     fix: %s\n", "", requirement.Fix)
		}
	}
	// The exit code is about this install, not about an engine it will not use: a machine
	// without a hypervisor that runs on bwrap is working, and says so. An engine that is not
	// downloaded yet is not a failure either — the first start fetches it.
	if chosen.Problem != "" && chosen.Installed {
		os.Exit(1)
	}
}

// engineReport is one engine's answer, with the nesting detail that belongs to it.
type engineReport struct {
	sandbox.Report
	Default  bool                   `json:"default,omitempty"`
	Default_ bool                   `json:"-"`
	Nesting  sandbox.NestingSupport `json:"nesting"`
}

// sandboxReports asks every engine this build knows, in the order a workspace would be given
// one. Each answer stands alone: an engine that cannot run here says why, and that is the
// answer, not a failure of the report.
func sandboxReports(dataDir string) []engineReport {
	defaultEngine := sandbox.NewTier(dataDir, "", nil).Default()
	reports := make([]engineReport, 0, len(sandbox.EngineNames()))
	for _, name := range sandbox.EngineNames() {
		manager, err := reportManager(dataDir, name)
		if err != nil {
			reports = append(reports, engineReport{Report: sandbox.Report{Engine: name, Problem: err.Error()}, Default: name == defaultEngine})
			continue
		}
		// Read the store through the pinned engine, so a catalog migrated forward by a
		// newer binary shows as a problem instead of a ready tier that refuses work.
		manager.Verify(context.Background())
		reports = append(reports, engineReport{
			Report:  manager.Report(),
			Default: name == defaultEngine,
			Nesting: manager.Nesting(context.Background()),
		})
	}
	return reports
}

// printEngines puts the engines in one block with the default marked: the table is the answer
// to "what can this machine run", and the mark is the answer to "what will it run".
func printEngines(engines []engineReport, defaultEngine string) {
	fmt.Println("sandbox engines")
	for _, engine := range engines {
		marker := " "
		if engine.Default {
			marker = "*"
		}
		version := engine.Version
		if version != "" {
			version = " " + version
		}
		fmt.Printf("%s %-14s %s%s\n", marker, engine.Engine, stateOf(engine), version)
		if engine.Path != "" && engine.Installed {
			fmt.Printf("  %-14s at %s\n", "", engine.Path)
		}
		if engine.Default {
			if engine.Nesting.Available {
				fmt.Printf("  %-14s nesting yes\n", "")
			} else if engine.Isolated {
				fmt.Printf("  %-14s nesting no (%s)\n", "", engine.Nesting.Reason)
			}
		}
		if engine.Problem != "" && engine.Installed {
			fmt.Printf("  %-14s problem %s\n", "", engine.Problem)
		}
	}
	if defaultEngine == "" {
		fmt.Println("\nno engine can run here: acornfall setup says what this machine needs")
		return
	}
	fmt.Printf("\n* this machine will use %s\n", defaultEngine)
}

// stateOf is one word for what an engine can do here, written so the three read as a column.
func stateOf(engine engineReport) string {
	switch {
	case engine.Problem != "" && engine.Installed:
		return "unavailable"
	case !engine.Installed:
		return "not downloaded yet"
	case !engine.Isolated:
		return "not isolated"
	}
	return "ready"
}

// readyInput gathers what the readiness list reads from the managers
// doctor already owns. Report-only where possible: doctor changes nothing.
func readyInput(dataDir string, updater *update.Manager, health update.Preflight, report sandbox.Report, engineName string) ready.Input {
	_, bunOK := updater.BunInstalled()
	return ready.Input{
		DataDir:         dataDir,
		Current:         health.Version,
		RuntimeTag:      updater.RuntimeTag(),
		Worker:          updater.WorkerVersion(),
		BunPath:         updater.BunPath(),
		BunOK:           bunOK,
		EngineName:      report.Engine,
		EngineVersion:   report.Version,
		EngineInstalled: report.Installed,
		EngineIsolated:  report.Isolated,
		EngineProblem:   report.Problem,
		EngineDisabled:  disabledReason(),
		Latest:          health.Latest,
		ReleasesNote:    health.Releases,
		HasCredential:   ready.HasCredential(dataDir),
	}
}

// disabledReason names an engine refusal from the environment, the way the
// gateway's own startup does: the same switch must mean the same thing.
func disabledReason() string {
	if os.Getenv("ACORNFALL_NO_SANDBOX") != "" {
		return "ACORNFALL_NO_SANDBOX is set"
	}
	return ""
}

// readyInputForBoot gathers the boot-time readiness list. The engine may
// be nil (refused tier): that is a stated answer, not a missing one.
func readyInputForBoot(dataDir string, updates *update.Manager, engine *sandbox.Manager) ready.Input {
	_, bunOK := updates.BunInstalled()
	status := updates.Status()
	input := ready.Input{
		DataDir:       dataDir,
		Current:       status.Current,
		RuntimeTag:    updates.RuntimeTag(),
		Worker:        updates.WorkerVersion(),
		BunPath:       updates.BunPath(),
		BunOK:         bunOK,
		HasCredential: ready.HasCredential(dataDir),
		Latest:        status.Latest,
	}
	if engine == nil {
		input.EngineDisabled = disabledReason()
		if input.EngineDisabled == "" {
			input.EngineDisabled = "no engine configured"
		}
		return input
	}
	report := engine.Report()
	input.EngineName = report.Engine
	input.EngineVersion = report.Version
	input.EngineInstalled = report.Installed
	input.EngineIsolated = report.Isolated
	if problem := engine.Problem(); problem != nil {
		input.EngineProblem = problem.Error()
	}
	return input
}

// reportManager builds the report-only manager doctor describes. It has to name an engine:
// without a driver, Open refuses ("sandbox: Driver is required") and doctor can only report
// that failure, never the tier it exists to diagnose.
func reportManager(dataDir, engineName string) (*sandbox.Manager, error) {
	driver, err := sandbox.Engine(dataDir, "", engineName)
	if err != nil {
		return nil, err
	}
	return sandbox.Open(context.Background(), sandbox.Options{
		DataDir:    dataDir,
		Driver:     driver,
		ReportOnly: true,
	})
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
