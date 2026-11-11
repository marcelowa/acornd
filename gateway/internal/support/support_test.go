package support

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/sandbox"
)

// fixture builds a workspace with a record, a log, a pid, and a cgroup tree — including the
// split between the cgroup the record names and the one the process actually lives in.
func fixture(t *testing.T) Options {
	t.Helper()
	dataDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "workspaces.json"), []byte(`{"workspaces":[{"name":"youki","path":"`+workspace+`","title":"Youki","engine":"youki","image":"debian:stable-slim"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	name := sandbox.Name(workspace)
	dir := filepath.Join(dataDir, "sandboxes", "youki", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{
		"engine":     "youki",
		"workspace":  workspace,
		"cgroupPath": "/user.slice/test.scope/acornd.slice/" + name,
		"secretProxy": map[string]any{
			"url": "http://acornd:proxy-token@127.0.0.1:1234",
		},
	}
	encoded, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(dataDir, "sandboxes", "youki", name+".json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"root":{"path":"/rootfs"},"cgroupsPath":"/user.slice/test.scope/acornd.slice/`+name+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "youki.log"), []byte("starting\nAuthorization: Bearer abcdefghijkl\nTOKEN=supersecret\nsetup http://127.0.0.1:7777/setup?code=ABCD-EFGH\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "container.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	proc := t.TempDir()
	pidDir := filepath.Join(proc, strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "cgroup"), []byte("0::/user.slice/test.scope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "status"), []byte("Name:\tyouki\nUid:\t0\t0\t0\t0\nGroups:\t0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := os.Getpid() + 1000
	task := filepath.Join(pidDir, "task", strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(task, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task, "children"), []byte(strconv.Itoa(child)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Join(proc, strconv.Itoa(child))
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, "cgroup"), []byte("0::/user.slice/test.scope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cgroups := t.TempDir()
	expected := filepath.Join(cgroups, "user.slice", "test.scope", "acornd.slice", name)
	for _, dir := range []string{expected, filepath.Join(cgroups, "user.slice", "test.scope")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(expected, "memory.max"), []byte("268435456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(expected, "cgroup.controllers"), []byte("cpu memory pids\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cgroups, "user.slice", "test.scope", "cgroup.subtree_control"), []byte("memory cpu\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "acornd.log"), []byte("gateway token=abcdefghijkl\nsandbox for youki: started\nexpanding \"a private prompt\"\nworker: tool output that should not travel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Options{
		DataDir:       dataDir,
		Workspace:     "youki",
		WorkspacePath: workspace,
		Engine:        "youki",
		Version:       "v-test",
		Secrets:       []string{"supersecret"},
		ProcRoot:      proc,
		CgroupRoot:    cgroups,
		LogTail:       4096,
	}
}

func TestBundleCarriesTheCgroupAndRemovesSecrets(t *testing.T) {
	bundle, err := Collect(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, file := range bundle.Files {
		if _, seen := files[file.Name]; seen {
			t.Errorf("bundle has %s twice", file.Name)
		}
		files[file.Name] = string(file.Data)
	}
	for _, want := range []string{
		"workspace/record.json",
		"sandbox/youki/record.json",
		"sandbox/youki/config.json",
		"sandbox/youki/youki.log",
		"proc/" + strconv.Itoa(os.Getpid()) + "/cgroup",
		"cgroup/expected/user.slice/test.scope/acornd.slice/" + sandbox.Name(bundleWorkspace(t, bundle)) + "/listing.txt",
		"manifest.json",
		"README.txt",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("bundle is missing %s; has %v", want, keys(files))
		}
	}
	for name, data := range files {
		for _, secret := range []string{"supersecret", "abcdefghijkl", "proxy-token", "ABCD-EFGH"} {
			if strings.Contains(data, secret) {
				t.Errorf("%s contains %q", name, secret)
			}
		}
	}
	if !strings.Contains(files["proc/"+strconv.Itoa(os.Getpid()+1000)+"/cgroup"], "test.scope") {
		t.Fatalf("the process tree should carry the child cgroup: %v", keys(files))
	}
	if strings.Contains(files["logs/acornd.log"], "private prompt") || strings.Contains(files["logs/acornd.log"], "tool output") {
		t.Fatalf("the gateway log leaked a conversation or a tool output: %q", files["logs/acornd.log"])
	}
	if !strings.Contains(files["logs/acornd.log"], "sandbox for youki") {
		t.Fatalf("the gateway log should keep the sandbox line: %q", files["logs/acornd.log"])
	}
	if !strings.Contains(files["sandbox/youki/youki.log"], "[redacted]") {
		t.Errorf("the log should show a redaction: %q", files["sandbox/youki/youki.log"])
	}
	if len(bundle.Preview.Files) == 0 || len(bundle.Redactions) == 0 {
		t.Fatalf("preview = %+v, redactions = %v", bundle.Preview, bundle.Redactions)
	}
}

func TestBundleIsATarGzWithTheManifest(t *testing.T) {
	bundle, err := Collect(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bundle.TarGz()
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[header.Name] = true
	}
	if !names["manifest.json"] || !names["README.txt"] {
		t.Fatalf("tar names = %v", names)
	}
}

func keys(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}

func bundleWorkspace(t *testing.T, bundle *Bundle) string {
	t.Helper()
	var record struct {
		Path string `json:"path"`
	}
	for _, file := range bundle.Files {
		if file.Name == "workspace/record.json" {
			if err := json.Unmarshal(file.Data, &record); err != nil {
				t.Fatal(err)
			}
			return record.Path
		}
	}
	t.Fatal("no workspace record")
	return ""
}
