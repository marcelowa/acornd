// Package support builds a redacted bundle of the facts needed to diagnose a workspace.
//
// A bundle is made on the machine that runs acornd, by the person who owns it, and stays a
// local file: nothing here uploads anything. The collector is deliberately narrow — records,
// small logs, process groups, and the cgroup files around them — because a diagnostic that
// copies a workspace or a vault is a data export wearing a lab coat.
package support

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/sandbox"
)

// Options is one workspace's diagnosis. Paths are the install's, not the workspace's.
type Options struct {
	DataDir       string
	Workspace     string
	WorkspacePath string
	Engine        string
	Version       string
	// Home is the user's home folder, replaced with ~ in the report. Empty means ask the OS.
	Home string
	// Secrets are the values the vault would deliver here. They are replaced before anything
	// is written. The caller reads them; this package never opens the vault.
	Secrets []string
	// ProcRoot and CgroupRoot are /proc and /sys/fs/cgroup in the product. They are fields so a
	// test can describe a machine without being it.
	ProcRoot   string
	CgroupRoot string
	// LogTail is how many bytes of each log are kept. Zero uses the default.
	LogTail int
	Now     time.Time
	// Warnings are facts the caller already knows are missing, such as a vault that could
	// not be read for redaction. They travel with the bundle instead of failing the collection.
	Warnings []string
	// State is control's live view of the workspace, when the caller has one: the sandbox
	// problem, whether the worker is up, and the error it last said. A CLI has no worker, so it
	// leaves this nil.
	State map[string]any
}

const defaultLogTail = 256 * 1024

// maxFile is the largest file the collector reads whole. Everything larger is listed, not copied.
const maxFile = 1 << 20

// File is one member of a bundle.
type File struct {
	Name string
	Data []byte
	Mode os.FileMode
}

// Bundle is a collected diagnosis, ready to write as a tar.gz.
type Bundle struct {
	Workspace  string
	Created    time.Time
	Files      []File
	Preview    Preview
	Redactions []string
	Warnings   []string
	tailBytes  int
}

// Preview is what the console shows before anything is downloaded.
type Preview struct {
	Workspace  string     `json:"workspace"`
	Created    time.Time  `json:"created"`
	Files      []FileInfo `json:"files"`
	Redactions []string   `json:"redactions"`
	Warnings   []string   `json:"warnings"`
}

// FileInfo is one line of a preview.
type FileInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func normalizeOptions(opts Options) (Options, error) {
	if strings.TrimSpace(opts.DataDir) == "" {
		return opts, errors.New("support: DataDir is required")
	}
	if strings.TrimSpace(opts.Workspace) == "" || strings.TrimSpace(opts.WorkspacePath) == "" {
		return opts, errors.New("support: a workspace name and path are required")
	}
	if opts.ProcRoot == "" {
		opts.ProcRoot = "/proc"
	}
	if opts.CgroupRoot == "" {
		opts.CgroupRoot = "/sys/fs/cgroup"
	}
	if opts.LogTail <= 0 {
		opts.LogTail = defaultLogTail
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	return opts, nil
}

// Collect gathers a workspace's diagnosis. A missing record or cgroup is a warning in the
// bundle, not a failure: the times a diagnosis is wanted are the times the machine is wrong.
func Collect(opts Options) (*Bundle, error) {
	opts, err := normalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	home := opts.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	red := NewRedactor(opts.Secrets, home)
	b := &Bundle{Workspace: opts.Workspace, Created: opts.Now, tailBytes: opts.LogTail, Warnings: append([]string(nil), opts.Warnings...)}
	add := func(name string, data []byte) {
		if len(data) > maxFile && !strings.HasSuffix(name, ".log") {
			b.Warnings = append(b.Warnings, name+" is larger than the collector's limit and was left out")
			return
		}
		b.Files = append(b.Files, File{Name: cleanName(name), Data: []byte(red.Text(string(data))), Mode: 0o600})
	}
	addJSON := func(name string, value any) {
		data, err := json.MarshalIndent(red.Value(value), "", "  ")
		if err != nil {
			b.Warnings = append(b.Warnings, name+": "+err.Error())
			return
		}
		add(name, append(data, '\n'))
	}

	b.system(opts, add)
	if opts.State != nil {
		addJSON("workspace/state.json", opts.State)
	}
	b.workspaceRecord(opts, addJSON, add)
	b.sandboxRecords(opts, addJSON, add)
	b.pidsAndCgroups(opts, add)
	b.gatewayLog(opts, add)
	b.finish(add, addJSON, red)
	return b, nil
}

// PreviewOf is Collect's answer as the console reads it: names and sizes, never bytes.
func PreviewOf(b *Bundle) Preview {
	infos := make([]FileInfo, 0, len(b.Files))
	for _, file := range b.Files {
		infos = append(infos, FileInfo{Name: file.Name, Size: int64(len(file.Data))})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return Preview{
		Workspace:  b.Workspace,
		Created:    b.Created,
		Files:      infos,
		Redactions: listOf(b.Redactions),
		Warnings:   listOf(b.Warnings),
	}
}

// listOf is a JSON array even when there is nothing in it. The console reads `.length` on
// every list the preview carries, so a null there is an uncaught TypeError mid-render instead
// of an empty section — which is how the bundle shipped unreachable.
func listOf(values []string) []string {
	return append([]string{}, values...)
}

// TarGz writes the bundle as one file a person can attach to a report.
func (b *Bundle) TarGz() ([]byte, error) {
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	for _, file := range b.Files {
		header := &tar.Header{
			Name:    file.Name,
			Mode:    int64(file.Mode.Perm()),
			Size:    int64(len(file.Data)),
			ModTime: b.Created,
		}
		if err := archive.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := archive.Write(file.Data); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if err := compressed.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (b *Bundle) finish(add func(string, []byte), addJSON func(string, any), red *Redactor) {
	for index := range b.Warnings {
		b.Warnings[index] = red.Text(b.Warnings[index])
	}
	b.Redactions = red.Notes()
	if len(b.Redactions) == 0 {
		b.Redactions = []string{"no known secret values or token patterns were found in the collected text"}
	}
	b.Preview = PreviewOf(b)
	addJSON("manifest.json", map[string]any{
		"workspace":  b.Workspace,
		"created":    b.Created,
		"files":      b.Preview.Files,
		"redactions": b.Redactions,
		"warnings":   listOf(b.Warnings),
		"acornd":     map[string]string{"version": b.version(), "go": runtime.Version()},
	})
	add("README.txt", []byte(b.readme()))
	// The manifest names the files before it; the preview the console shows includes the
	// manifest and the README as well, because they are what a person is about to download.
	b.Preview = PreviewOf(b)
}

func (b *Bundle) version() string {
	for _, file := range b.Files {
		if file.Name == "system/acornd-version.txt" {
			return strings.TrimSpace(string(file.Data))
		}
	}
	return ""
}

func (b *Bundle) readme() string {
	return `acornd support bundle

This file was made on the machine that runs acornd, for one workspace. It is a local report:
acornd does not upload it anywhere.

It contains:
- acornd and machine facts (version, OS, kernel, cgroups).
- The workspace record, with secrets removed.
- The sandbox record and its small configuration files.
- The last part of the sandbox and gateway logs.
- The process group of the sandbox process, and the cgroup files around it.
- A list of files the collector saw but did not copy (roots, home, workspace).

It does not contain:
- secret values, credentials, provider keys, or token values.
- conversation text or workspace files.
- environment variables.

Redaction is best effort: exact vault values, token-shaped strings, URL credentials, email
addresses, and the home path are replaced. Check the manifest for what was replaced before
sharing this file.
`
}

// system writes the machine facts a cgroup question needs.
func (b *Bundle) system(opts Options, add func(string, []byte)) {
	version := opts.Version
	if version == "" {
		version = "unknown"
	}
	add("system/acornd-version.txt", []byte(version+"\n"))
	add("system/go.txt", []byte(runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH+"\n"))
	for _, item := range []struct{ name, path string }{
		{"os-release.txt", "/etc/os-release"},
		{"kernel.txt", "/proc/version"},
		{"uid-map.txt", "/proc/self/uid_map"},
		{"self-cgroup.txt", "/proc/self/cgroup"},
	} {
		if data, err := os.ReadFile(item.path); err == nil {
			add("system/"+item.name, data)
		} else {
			b.Warnings = append(b.Warnings, item.path+": "+err.Error())
		}
	}
	add("system/id.txt", []byte(fmt.Sprintf("uid=%d gid=%d euid=%d egid=%d\n", os.Getuid(), os.Getgid(), os.Geteuid(), os.Getegid())))
	if data, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		var lines []string
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "cgroup") {
				lines = append(lines, line)
			}
		}
		add("system/mountinfo-cgroup.txt", []byte(strings.Join(lines, "\n")+"\n"))
	}
	if out, err := systemctlVersion(); err == nil {
		add("system/systemd.txt", out)
	}
	if out := systemdSession(); len(out) > 0 {
		add("system/systemd-session.txt", out)
	}
}

func systemctlVersion() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "--version").Output()
}

type registryFile struct {
	Workspaces []struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Title  string `json:"title"`
		Engine string `json:"engine"`
		Image  string `json:"image"`
	} `json:"workspaces"`
}

// workspaceRecord writes the workspace's record without the fields a support bundle never
// needs: members' addresses and the rest of the room's social graph.
func (b *Bundle) workspaceRecord(opts Options, addJSON func(string, any), add func(string, []byte)) {
	path := filepath.Join(opts.DataDir, "workspaces.json")
	data, err := os.ReadFile(path)
	if err != nil {
		b.Warnings = append(b.Warnings, path+": "+err.Error())
		return
	}
	var registry registryFile
	if err := json.Unmarshal(data, &registry); err != nil {
		b.Warnings = append(b.Warnings, path+": "+err.Error())
		return
	}
	for _, workspace := range registry.Workspaces {
		if workspace.Name != opts.Workspace {
			continue
		}
		addJSON("workspace/record.json", map[string]any{
			"name":   workspace.Name,
			"path":   workspace.Path,
			"title":  workspace.Title,
			"engine": workspace.Engine,
			"image":  workspace.Image,
		})
		return
	}
	add("workspace/record.txt", []byte("the workspace is not in "+path+"\n"))
}

// sandboxRecords finds every engine's record for this workspace. The name is derived from the
// path, so a record is found without knowing which engine wrote it.
func (b *Bundle) sandboxRecords(opts Options, addJSON func(string, any), add func(string, []byte)) {
	name := sandbox.Name(opts.WorkspacePath)
	pattern := filepath.Join(opts.DataDir, "sandboxes", "*", name+".json")
	records, err := filepath.Glob(pattern)
	if err != nil {
		b.Warnings = append(b.Warnings, pattern+": "+err.Error())
		return
	}
	if len(records) == 0 {
		b.Warnings = append(b.Warnings, "no sandbox record for "+name+" under "+filepath.Join(opts.DataDir, "sandboxes"))
	}
	for _, recordPath := range records {
		engine := filepath.Base(filepath.Dir(recordPath))
		raw, err := os.ReadFile(recordPath)
		if err != nil {
			b.Warnings = append(b.Warnings, recordPath+": "+err.Error())
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			b.Warnings = append(b.Warnings, recordPath+": "+err.Error())
			continue
		}
		addJSON(filepath.Join("sandbox", engine, "record.json"), record)
		b.recordFiles(engine, strings.TrimSuffix(recordPath, ".json"), add)
	}
}

// recordFiles copies the small files beside a record and lists the rest. A rootfs, an upper
// layer, and a private home are named in the listing but never read.
func (b *Bundle) recordFiles(engine, dir string, add func(string, []byte)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		b.Warnings = append(b.Warnings, dir+": "+err.Error())
		return
	}
	add(filepath.Join("sandbox", engine, "listing.txt"), []byte(recordListing(entries)))
	for _, entry := range entries {
		b.recordFile(engine, dir, entry, add)
	}
}

func recordListing(entries []os.DirEntry) string {
	var listing strings.Builder
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(&listing, "%s %10d %s\n", info.Mode().String(), info.Size(), entry.Name())
	}
	return listing.String()
}

func (b *Bundle) recordFile(engine, dir string, entry os.DirEntry, add func(string, []byte)) {
	info, err := entry.Info()
	if err != nil || entry.IsDir() || !info.Mode().IsRegular() {
		return
	}
	switch entry.Name() {
	case "config.json", "container.pid", "supervisor.pid", "runtime.pid", "pasta-resolv.conf":
		if info.Size() > maxFile {
			return
		}
		if data, err := os.ReadFile(filepath.Join(dir, entry.Name())); err == nil {
			add(filepath.Join("sandbox", engine, entry.Name()), data)
		}
	case "youki.log", "supervisor.log":
		add(filepath.Join("sandbox", engine, entry.Name()), tail(filepath.Join(dir, entry.Name()), b.tailBytes))
	}
}

// tail reads the last max bytes of a file, with a marker when it was cut.
func tail(path string, max int) []byte {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	offset := int64(0)
	cut := false
	if info.Size() > int64(max) {
		offset = info.Size() - int64(max)
		cut = true
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil
	}
	if cut {
		data = append([]byte(fmt.Sprintf("[... %d bytes cut ...]\n", offset)), data...)
	}
	return data
}

// pidsAndCgroups is the part the last three outages were about: where the sandbox process
// actually lives, and what the record says it should be.
func (b *Bundle) pidsAndCgroups(opts Options, add func(string, []byte)) {
	dirs := b.sandboxDirs(opts)
	expected := b.expectedCgroups(dirs)
	actual := map[string]bool{}
	for _, dir := range dirs {
		b.readPids(opts, dir, add, actual)
	}
	b.snapshotCgroups(opts, expected, "expected", nil, add)
	b.snapshotCgroups(opts, actual, "actual", expected, add)
}

func (b *Bundle) sandboxDirs(opts Options) []string {
	pattern := filepath.Join(opts.DataDir, "sandboxes", "*", sandbox.Name(opts.WorkspacePath))
	dirs, _ := filepath.Glob(pattern)
	return dirs
}

func (b *Bundle) expectedCgroups(dirs []string) map[string]bool {
	expected := map[string]bool{}
	for _, dir := range dirs {
		raw, err := os.ReadFile(dir + ".json")
		if err != nil {
			continue
		}
		var record map[string]any
		if json.Unmarshal(raw, &record) != nil {
			continue
		}
		if cgroup, ok := record["cgroupPath"].(string); ok && cgroup != "" {
			expected[cgroup] = true
		}
	}
	return expected
}

func (b *Bundle) readPids(opts Options, dir string, add func(string, []byte), actual map[string]bool) {
	for _, name := range []string{"container.pid", "supervisor.pid", "runtime.pid"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 0 {
			continue
		}
		b.pid(opts, dir, name, pid, add, actual)
		b.procTree(opts, pid, add, actual, map[int]bool{pid: true}, 0)
	}
}

func (b *Bundle) pid(opts Options, dir, name string, pid int, add func(string, []byte), actual map[string]bool) {
	b.procCgroup(opts, dir, name, pid, add, actual)
	b.procFiles(opts, pid, add)
}

func (b *Bundle) procCgroup(opts Options, dir, name string, pid int, add func(string, []byte), actual map[string]bool) {
	base := filepath.Join("proc", strconv.Itoa(pid))
	raw, err := os.ReadFile(filepath.Join(opts.ProcRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		b.Warnings = append(b.Warnings, name+" ("+dir+"): "+err.Error())
		return
	}
	add(filepath.Join(base, "cgroup"), raw)
	for _, line := range strings.Split(string(raw), "\n") {
		_, after, ok := strings.Cut(line, "::")
		if !ok {
			continue
		}
		cgroup := strings.TrimSpace(after)
		if cgroup == "" {
			continue
		}
		actual[cgroup] = true
	}
}

func (b *Bundle) procFiles(opts Options, pid int, add func(string, []byte)) {
	base := filepath.Join("proc", strconv.Itoa(pid))
	if raw, err := os.ReadFile(filepath.Join(opts.ProcRoot, strconv.Itoa(pid), "cmdline")); err == nil {
		add(filepath.Join(base, "cmdline"), bytes.ReplaceAll(raw, []byte{0}, []byte{' '}))
	}
	raw, err := os.ReadFile(filepath.Join(opts.ProcRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return
	}
	var kept []string
	for _, line := range strings.Split(string(raw), "\n") {
		for _, prefix := range []string{"Name:", "State:", "Uid:", "Gid:", "Groups:", "NSpid:", "CapEff:"} {
			if strings.HasPrefix(line, prefix) {
				kept = append(kept, line)
				break
			}
		}
	}
	add(filepath.Join(base, "status"), []byte(strings.Join(kept, "\n")+"\n"))
}

func (b *Bundle) snapshotCgroups(opts Options, set map[string]bool, kind string, skip map[string]bool, add func(string, []byte)) {
	for cgroup := range set {
		if skip != nil && skip[cgroup] {
			continue
		}
		b.snapshotCgroup(opts, cgroup, kind, add)
	}
}

func (b *Bundle) snapshotCgroup(opts Options, cgroup, kind string, add func(string, []byte)) {
	if cgroup == "" || cgroup == "/" {
		return
	}
	name := filepath.Join("cgroup", kind, cleanName(cgroup))
	full := filepath.Join(opts.CgroupRoot, strings.TrimPrefix(cgroup, "/"))
	info, err := os.Stat(full)
	if err != nil {
		add(filepath.Join(name, "missing.txt"), []byte(err.Error()+"\n"))
		b.cgroupAncestors(opts, cgroup, name, add)
		return
	}
	b.cgroupListing(full, name, info, add)
	b.cgroupFiles(full, name, add)
	b.cgroupAncestors(opts, cgroup, name, add)
}

func (b *Bundle) cgroupListing(full, name string, info os.FileInfo, add func(string, []byte)) {
	var listing strings.Builder
	fmt.Fprintf(&listing, "%s\n", info.Mode().String())
	entries, err := os.ReadDir(full)
	if err != nil {
		add(filepath.Join(name, "listing.txt"), []byte(listing.String()))
		return
	}
	for _, entry := range entries {
		child, err := entry.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(&listing, "%s %10d %s\n", child.Mode().String(), child.Size(), entry.Name())
	}
	add(filepath.Join(name, "listing.txt"), []byte(listing.String()))
}

func (b *Bundle) cgroupFiles(full, name string, add func(string, []byte)) {
	for _, file := range []string{
		"cgroup.controllers", "cgroup.subtree_control", "cgroup.procs", "cgroup.events",
		"memory.max", "memory.swap.max", "memory.current", "pids.max", "pids.current",
		"cpu.max", "cpu.stat",
	} {
		if raw, err := os.ReadFile(filepath.Join(full, file)); err == nil {
			add(filepath.Join(name, file), raw)
		}
	}
}

// cgroupAncestors says whether the controller was ever distributed down to this leaf. That
// is the difference between a missing file and a missing path.
func (b *Bundle) cgroupAncestors(opts Options, cgroup, name string, add func(string, []byte)) {
	var ancestors strings.Builder
	parts := strings.Split(strings.Trim(cgroup, "/"), "/")
	for i := 1; i <= len(parts); i++ {
		ancestor := "/" + strings.Join(parts[:i], "/")
		ancestorDir := filepath.Join(opts.CgroupRoot, strings.TrimPrefix(ancestor, "/"))
		fmt.Fprintf(&ancestors, "== %s ==\n", ancestor)
		for _, file := range []string{"cgroup.controllers", "cgroup.subtree_control"} {
			raw, err := os.ReadFile(filepath.Join(ancestorDir, file))
			if err != nil {
				fmt.Fprintf(&ancestors, "%s: %v\n", file, err)
				continue
			}
			fmt.Fprintf(&ancestors, "%s: %s\n", file, strings.TrimSpace(string(raw)))
		}
	}
	add(filepath.Join(name, "ancestors.txt"), []byte(ancestors.String()))
}

func (b *Bundle) gatewayLog(opts Options, add func(string, []byte)) {
	path := filepath.Join(opts.DataDir, "acornd.log")
	data := tail(path, opts.LogTail)
	if len(data) == 0 {
		b.Warnings = append(b.Warnings, "no gateway log at "+path)
		return
	}
	add("logs/acornd.log", []byte(lifecycleLines(string(data))))
}

// lifecycleLines keeps the gateway's own start, stop, and sandbox lines. The worker's lines
// can carry a prompt or a tool's output, and a support bundle must not become a transcript.
func lifecycleLines(text string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "worker:") || strings.Contains(lower, "expanding") ||
			strings.Contains(lower, "conversation") || strings.Contains(lower, "prompt") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func cleanName(name string) string {
	clean := path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	return strings.TrimPrefix(clean, "/")
}

// systemdSession is the user manager's own answer about this account: whether it is active and
// what acornd units it knows. An idle stop that takes a cgroup with it is a user-manager fact,
// and the cgroup tree alone cannot say whether the manager was restarting.
func systemdSession() []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out strings.Builder
	for _, command := range [][]string{
		{"loginctl", "show-user", strconv.Itoa(os.Getuid())},
		{"systemctl", "--user", "--no-pager", "list-units", "--all", "--plain", "acornd*"},
	} {
		if data, err := exec.CommandContext(ctx, command[0], command[1:]...).Output(); err == nil {
			fmt.Fprintf(&out, "== %s ==\n%s\n", strings.Join(command, " "), strings.TrimSpace(string(data)))
		}
	}
	return []byte(out.String())
}

// procTree follows the process tree from a pid the record names. The pid in a runtime's own
// pid file can be the container's namespace pid; the host pid of the supervisor is a child of
// the runtime, and its cgroup is what a diagnosis needs.
func (b *Bundle) procTree(opts Options, pid int, add func(string, []byte), actual map[string]bool, seen map[int]bool, depth int) {
	if depth >= 5 {
		return
	}
	children, err := os.ReadFile(filepath.Join(opts.ProcRoot, strconv.Itoa(pid), "task", strconv.Itoa(pid), "children"))
	if err != nil {
		return
	}
	add(filepath.Join("proc", strconv.Itoa(pid), "children"), children)
	for _, field := range strings.Fields(string(children)) {
		child, err := strconv.Atoi(field)
		if err != nil || child <= 0 || seen[child] {
			continue
		}
		seen[child] = true
		b.procCgroup(opts, "process tree", "child", child, add, actual)
		b.procFiles(opts, child, add)
		b.procTree(opts, child, add, actual, seen, depth+1)
	}
}
