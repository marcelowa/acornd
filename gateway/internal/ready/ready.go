// Package ready is the install's readiness list: every thing acornfall
// needs from a machine, each with a plain fix. Doctor prints it, boot
// logs what is missing. A missing piece is a sentence with a fix, never
// a crash and never a silent fallback.
//
// New dependencies land here: one Requirement, not a new special case in
// three places. Anything auto-installable (bun, worker sources, the
// engine) is ensured where it is used; this list only tells the truth.
package ready

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/acornfall/acornfall/gateway/internal/update"
)

// Requirement is one thing the install needs: whether it holds, what it
// is in one line, and how to fix it when it does not.
type Requirement struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// Input is everything the checks read. The caller gathers it from the
// managers it already owns; this package decides nothing it cannot see.
type Input struct {
	DataDir string
	// Current is the running tag ("dev" for source builds).
	Current string
	// RuntimeTag is the binary's own stamp when it has one: execution
	// must match the binary, so sources matching the stamp satisfy even
	// when the version file lags behind it.
	RuntimeTag string
	// Worker is the ensured sources' tag, "" when none.
	Worker string
	// BunPath and BunOK say whether the pinned runtime answers.
	BunPath string
	BunOK   bool
	// Engine describes the sandbox tier; Disabled says it was refused.
	EngineName      string
	EngineVersion   string
	EngineInstalled bool
	EngineIsolated  bool
	EngineProblem   string
	EngineDisabled  string
	// Latest names the release check's answer, "" with ReleasesNote.
	Latest        string
	ReleasesNote  string
	HasCredential bool
	// HypervisorOK is the machine fact the list reports, gathered by the caller so a test
	// can state it instead of depending on the machine running the test.
	HypervisorOK     bool
	HypervisorDetail string
}

// Check builds the list. Filesystem reads stay under DataDir so tests run
// anywhere; the caller gathers the one machine fact (/dev/kvm), and disk
// space reads the data folder.
func Check(input Input) []Requirement {
	return []Requirement{
		dataDirWritable(input.DataDir),
		bun(input),
		worker(input),
		engine(input),
		hypervisor(input),
		credentials(input),
		disk(input.DataDir),
		releases(input),
	}
}

// Hypervisor is the machine fact the readiness list reports. It is here, not inside Check, so
// the check is a function of its input and a test can state both answers without a device.
func Hypervisor() (ok bool, detail string) {
	if runtime.GOOS != "linux" {
		return true, "not applicable on " + runtime.GOOS
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return false, "no /dev/kvm"
	}
	return true, "/dev/kvm present"
}

func dataDirWritable(dataDir string) Requirement {
	dir := dataDir
	created := "is writable"
	// Doctor must not create anything: a folder that does not exist yet is
	// judged by its parent, which is where creation would happen.
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		dir = filepath.Dir(dataDir)
		created = "will be created inside writable " + dir
	}
	probe := dir + "/.acornfall-writable"
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return Requirement{"data-dir", false, "cannot write " + dir + ": " + err.Error(), "run with a writable --data-dir"}
	}
	_ = os.Remove(probe)
	return Requirement{"data-dir", true, dataDir + " " + created, ""}
}

func bun(input Input) Requirement {
	if input.BunOK {
		return Requirement{"bun", true, input.BunPath + " answers " + update.BunVersion, ""}
	}
	if input.Current == "dev" {
		return Requirement{"bun", true, "dev build: system bun over repo sources", ""}
	}
	return Requirement{"bun", false, "pinned runtime missing", "first boot downloads it (needs network once)"}
}

func worker(input Input) Requirement {
	if input.Worker != "" {
		if input.Worker == input.Current || input.Current == "" {
			return Requirement{"worker", true, "sources " + input.Worker, ""}
		}
		if input.RuntimeTag != "" && input.Worker == input.RuntimeTag {
			return Requirement{"worker", true, "sources " + input.Worker + " match the running binary", ""}
		}
		return Requirement{"worker", false, "sources " + input.Worker + " do not match " + input.Current, "restart: boot activates the staged sources"}
	}
	if input.Current == "dev" {
		return Requirement{"worker", true, "dev build: repo sources", ""}
	}
	return Requirement{"worker", false, "no sources yet", "first boot fetches them (needs network once)"}
}

func engine(input Input) Requirement {
	if input.EngineDisabled != "" {
		return Requirement{"engine", true, "disabled: " + input.EngineDisabled, ""}
	}
	if input.EngineProblem != "" {
		return Requirement{"engine", false, input.EngineProblem, "see the sandbox section above; tools refuse until it is fixed"}
	}
	if !input.EngineInstalled {
		return Requirement{"engine", false, input.EngineName + " not installed", "open a workspace: the engine installs on first use (needs network once)"}
	}
	name := input.EngineName
	if input.EngineVersion != "" {
		name += " " + input.EngineVersion
	}
	if !input.EngineIsolated {
		return Requirement{"engine", true, name + " runs on the host, unconfined", ""}
	}
	return Requirement{"engine", true, name + " installed", ""}
}

func hypervisor(input Input) Requirement {
	if runtime.GOOS != "linux" {
		return Requirement{"hypervisor", true, "not applicable on " + runtime.GOOS, ""}
	}
	if !input.HypervisorOK {
		detail := input.HypervisorDetail
		if detail == "" {
			detail = "no /dev/kvm"
		}
		return Requirement{"hypervisor", false, detail, "run where KVM exists, or choose another engine: bwrap (user namespaces) or host (unconfined)"}
	}
	detail := input.HypervisorDetail
	if detail == "" {
		detail = "/dev/kvm present"
	}
	return Requirement{"hypervisor", true, detail, ""}
}

func credentials(input Input) Requirement {
	if input.HasCredential {
		return Requirement{"credentials", true, "at least one provider stored", ""}
	}
	return Requirement{"credentials", false, "no provider stored", "acornfall provider add <provider> <key> (the dev provider needs none)"}
}

func disk(dataDir string) Requirement {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dataDir, &stat); err != nil {
		if err := syscall.Statfs(filepath.Dir(dataDir), &stat); err != nil {
			return Requirement{"disk", true, "free space unreadable, assuming room", ""}
		}
	}
	free := stat.Bavail * uint64(stat.Bsize)
	detail := fmt.Sprintf("%dM free", free>>20)
	const want = 100 << 20
	if free < want {
		return Requirement{"disk", false, detail, "free disk: backups, images, and dependencies live here"}
	}
	return Requirement{"disk", true, detail, ""}
}

func releases(input Input) Requirement {
	if input.Latest != "" {
		return Requirement{"releases", true, "latest is " + input.Latest, ""}
	}
	if input.ReleasesNote != "" {
		return Requirement{"releases", false, input.ReleasesNote, "checks retry hourly; offline is fine, updates wait"}
	}
	return Requirement{"releases", true, "not checked yet", ""}
}

// HasCredential reports whether the credentials file names a provider.
func HasCredential(dataDir string) bool {
	return credentialCount(dataDir) > 0
}

func credentialCount(dataDir string) int {
	payload, err := os.ReadFile(dataDir + "/credentials.json")
	if err != nil {
		return 0
	}
	var all map[string]any
	if err := json.Unmarshal(payload, &all); err != nil {
		return 0
	}
	return len(all)
}
