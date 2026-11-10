package e2e

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/sandbox"
	"github.com/marcelowa/acornd/gateway/internal/sandbox/youki"
)

// The requirement ADR 0022 could not meet: a second acornd starts inside a youki workspace, its
// console answers, and the engine it picks for its own sandbox (bwrap) makes a sandbox that runs a
// command. The inner gateway is a real binary with a real worker: the copy is what a person who
// checks the project out into a workspace would have, and the command proves the inner sandbox is
// not the outer one.
//
// The inner acornd is not told it is nested. A youki container holds CAP_SETFCAP and not
// CAP_SYS_ADMIN, so bubblewrap there asks for a user namespace of its own; a nested bwrap without
// that capability dies at "Creating new namespace failed", which is the failure this row exists to
// catch.
func TestANestedAcornRunsInYoukiAndPicksItsEngine(t *testing.T) {
	// Not parallel: the copy and the inner sandbox are heavy, and the cgroup parent is shared with
	// every other youki sandbox on the machine.
	nested := bootNestedAcord(t)

	created := nested.api(t, "POST", "/api/workspaces", `{"name":"inner","engine":"bwrap","network":"off"}`)
	if !strings.Contains(created, `"name":"inner"`) || !strings.Contains(created, `"engine":"bwrap"`) {
		t.Fatalf("the nested install did not record the bwrap workspace: %s", created)
	}
	id := nested.conversation(t)
	if got := nested.api(t, "POST", "/api/workspaces/inner/conversations/"+id+"/messages", `{"text":"inner"}`); !strings.Contains(got, "accepted") {
		t.Fatalf("the nested turn was not accepted: %s", got)
	}
	nested.waitForCommand(t, id)
	nested.assertSandbox(t)
}

// nestedAcord is one acornd running inside a youki workspace, and the socket and paths this test
// needs to ask it questions from outside.
type nestedAcord struct {
	socket    string
	workspace string
	data      string
	log       string
	token     string
	port      int
}

// bootNestedAcord builds the outer youki workspace and the inner installation in it.
func bootNestedAcord(t *testing.T) *nestedAcord {
	t.Helper()
	if os.Getenv("ACORND_YOUKI_ONLY") == "" {
		t.Skip("the youki tier runs this: it boots a nested acornd")
	}
	youkiAvailable(t)
	workspace, socket := youkiWorkspace(t)
	copyInnerInstall(t, workspace)
	installInnerTools(t, socket)
	data, port, log := startInnerAcord(t, socket, workspace)
	token, err := os.ReadFile(filepath.Join(data, "token"))
	if err != nil {
		t.Fatalf("the nested install has no token: %v", err)
	}
	if strings.TrimSpace(string(token)) == "" {
		t.Fatal("the nested install's token is empty")
	}
	return &nestedAcord{socket: socket, workspace: workspace, data: data, log: log, token: strings.TrimSpace(string(token)), port: port}
}

// youkiWorkspace creates one sandbox and answers with its workspace path and supervisor socket.
func youkiWorkspace(t *testing.T) (workspace, socket string) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	// The image the suite already pulled is shared, as every other e2e test shares it: a fresh
	// directory would fetch debian from a registry for this one test, and a machine whose resolver
	// is a tailnet name would fail before the sandbox existed.
	shared := sandboxStateDir()
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(dataDir, "sandboxes")); err != nil {
		t.Fatal(err)
	}
	driver := youki.NewYouki(dataDir).WithSupervisor(gatewayBin)
	if err := driver.Install(ctx, t.Logf); err != nil {
		t.Fatalf("install the pinned runtime: %v", err)
	}
	workspace = filepath.Join(t.TempDir(), "nested")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	name := sandbox.Name(workspace)
	t.Cleanup(func() { _ = driver.Remove(context.Background(), name) })
	if err := driver.Create(ctx, sandbox.Policy{Workspace: workspace, Image: "debian:stable-slim", Egress: true}, t.TempDir()); err != nil {
		t.Fatalf("create the youki workspace: %v", err)
	}
	return workspace, readYoukiRecord(t, driver.StateDir(), name).Socket
}

// copyInnerInstall puts the checked-out project into the workspace: the binary this suite built,
// the worker beside it, and bun. The workspace is bound at the same path inside the container, so
// a file written here is a file the inner acornd can run.
func copyInnerInstall(t *testing.T, workspace string) {
	t.Helper()
	copyIntoWorkspace(t, gatewayBin, filepath.Join(workspace, "acornd"))
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatalf("bun is not on PATH: %v", err)
	}
	copyIntoWorkspace(t, bun, filepath.Join(workspace, "bun"))
	workerCopy(t, workerDir, filepath.Join(workspace, "worker"))
	t.Logf("copied the gateway, bun and the worker into %s", workspace)
}

// installInnerTools gives the container the inner engine and the two tools the inner gateway and
// this test need: bubblewrap, ca-certificates for the inner image pull, and curl for the console.
func installInnerTools(t *testing.T, socket string) {
	t.Helper()
	out, code := youkiRun(t, socket,
		"export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq bubblewrap ca-certificates curl >/dev/null 2>&1",
		10*time.Minute)
	if code != 0 {
		t.Fatalf("install the inner engine's tools: exit %d\n%s", code, out)
	}
}

// startInnerAcord writes the inner install's script and settings, starts the gateway in the
// background, and waits for its console.
func startInnerAcord(t *testing.T, socket, workspace string) (data string, port int, log string) {
	t.Helper()
	script := filepath.Join(workspace, "script.json")
	if err := os.WriteFile(script, []byte(`{"rules":[{"match":"inner","tools":[{"name":"bash","args":{"command":"printf inner-sandbox-ok; echo; cat /proc/self/uid_map"}}],"reply":"done"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	data = filepath.Join(workspace, "data")
	innerWorkspaces := filepath.Join(workspace, "inner-workspaces")
	for _, dir := range []string{data, innerWorkspaces} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	port = 18080
	log = filepath.Join(workspace, "nested.log")
	start := fmt.Sprintf(
		`cd %s && env -i PATH=%s:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin HOME=%s ACORND_DATA_DIR=%s ACORND_WORKER=%s ACORND_SANDBOX_ENGINE=bwrap ACORND_DEV_SCRIPT=%s ACORND_MODEL=faux/faux-1 nohup %s start --data-dir %s --workspaces-dir %s --host 127.0.0.1 --port %d > %s 2>&1 < /dev/null &`,
		workspace, workspace, data, data, filepath.Join(workspace, "worker"), script,
		filepath.Join(workspace, "acornd"), data, innerWorkspaces, port, log)
	if out, code := youkiRun(t, socket, start, time.Minute); code != 0 {
		t.Fatalf("start the nested acornd: exit %d\n%s", code, out)
	}
	waitForConsole(t, socket, port, log)
	// The scripted provider is the suite's, and a fresh install has no allowlist: writing it after
	// the gateway started is what the harness does, because a file written before the first boot is
	// migrated to the personal workspace's defaults.
	if err := os.WriteFile(filepath.Join(data, "settings.json"), []byte(`{"providerModels":{"faux":["faux-1"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return data, port, log
}

// waitForConsole is the "the console answers" half of R7, read from inside the container: the
// gateway is on pasta's loopback, which the machine cannot reach.
func waitForConsole(t *testing.T, socket string, port int, log string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		out, code := youkiRun(t, socket,
			fmt.Sprintf("curl -sS -o /dev/null -w '%%{http_code}' http://127.0.0.1:%d/healthz", port), 10*time.Second)
		if code == 0 && strings.TrimSpace(out) == "200" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the nested console never answered: %q\n%s", out, readOrEmpty(log))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// api is one request to the nested console, made from inside the container. The body and the token
// are files the inner process can read, so no JSON is quoted into a shell.
func (n *nestedAcord) api(t *testing.T, method, path, body string) string {
	t.Helper()
	request := filepath.Join(n.workspace, "request.json")
	if err := os.WriteFile(request, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf(
		`curl -sS -X %s -H "Authorization: Bearer $(cat %s)" -H "Content-Type: application/json" --data-binary @%s http://127.0.0.1:%d%s`,
		method, filepath.Join(n.data, "token"), request, n.port, path)
	out, code := youkiRun(t, n.socket, command, 30*time.Second)
	if code != 0 {
		t.Fatalf("%s %s: exit %d\n%s", method, path, code, out)
	}
	return out
}

// conversation is the one the nested workspace creates when it is first opened.
func (n *nestedAcord) conversation(t *testing.T) string {
	t.Helper()
	var conversations []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(n.api(t, "GET", "/api/workspaces/inner/conversations", "")), &conversations); err != nil {
		t.Fatalf("read the nested conversation list: %v", err)
	}
	if len(conversations) == 0 {
		t.Fatal("the nested workspace has no conversation")
	}
	return conversations[0].ID
}

// waitForCommand waits for the transcript to carry what the scripted tool printed inside the
// nested sandbox. The stream sends a whole view on connect, so a poll is a read of the transcript.
func (n *nestedAcord) waitForCommand(t *testing.T, id string) {
	t.Helper()
	stream := fmt.Sprintf(
		"curl -sS -N --max-time 4 -H \"Authorization: Bearer $(cat %s)\" 'http://127.0.0.1:%d/api/stream?workspace=inner&conversation=%s'",
		filepath.Join(n.data, "token"), n.port, id)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		out, _ := youkiRun(t, n.socket, stream, 15*time.Second)
		if strings.Contains(out, "inner-sandbox-ok") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the command did not run in the nested sandbox\nstream: %s\nnested log:\n%s", out, readOrEmpty(n.log))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// assertSandbox is the "the engine it picked works" half: the nested install says bwrap, isolated,
// and its tools run.
func (n *nestedAcord) assertSandbox(t *testing.T) {
	t.Helper()
	var state struct {
		Sandbox struct {
			Engine   string `json:"engine"`
			Tools    bool   `json:"tools"`
			Isolated bool   `json:"isolated"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal([]byte(n.api(t, "GET", "/api/workspaces/inner/state", "")), &state); err != nil {
		t.Fatalf("read the nested state: %v", err)
	}
	if state.Sandbox.Engine != "bwrap" || !state.Sandbox.Isolated || !state.Sandbox.Tools {
		t.Fatalf("the nested sandbox is not a working bwrap: %+v", state.Sandbox)
	}
}

// readYoukiRecord is the part of a sandbox record this test reads: the workspace the container
// binds, and the socket its first process listens on.
type youkiRecord struct {
	Workspace string `json:"workspace"`
	Socket    string `json:"socket"`
}

func readYoukiRecord(t *testing.T, stateDir, name string) youkiRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, name+".json"))
	if err != nil {
		t.Fatalf("read the youki record: %v", err)
	}
	var record youkiRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("parse the youki record: %v", err)
	}
	if record.Workspace == "" || record.Socket == "" {
		t.Fatalf("the youki record has no workspace or socket: %s", raw)
	}
	return record
}

// copyIntoWorkspace puts one host file where the container sees the workspace: the same path on
// both sides, which is what makes a copied binary runnable inside.
func copyIntoWorkspace(t *testing.T, source, target string) {
	t.Helper()
	if out, err := exec.Command("cp", "-a", source, target).CombinedOutput(); err != nil {
		t.Fatalf("copy %s to %s: %v\n%s", source, target, err, out)
	}
}

// workerCopy copies the worker package, resolving node_modules when the checkout keeps it as a
// symlink: a symlink whose target is outside the workspace is not there inside the container.
func workerCopy(t *testing.T, source, target string) {
	t.Helper()
	if out, err := exec.Command("cp", "-a", source, target).CombinedOutput(); err != nil {
		t.Fatalf("copy the worker: %v\n%s", err, out)
	}
	modules := filepath.Join(target, "node_modules")
	resolved, err := filepath.EvalSymlinks(filepath.Join(source, "node_modules"))
	if err != nil || resolved == filepath.Join(source, "node_modules") {
		return
	}
	if err := os.Remove(modules); err != nil {
		t.Fatalf("remove the worker's node_modules link: %v", err)
	}
	if out, err := exec.Command("cp", "-a", resolved, modules).CombinedOutput(); err != nil {
		t.Fatalf("copy the worker's node_modules: %v\n%s", err, out)
	}
}

func readOrEmpty(path string) string {
	raw, _ := os.ReadFile(path)
	return string(raw)
}

// youkiRun is one command through a youki sandbox's supervisor socket: a JSON line in, events back,
// the chunks until the exit. It is the same protocol the worker speaks, written here because this
// test asks the sandbox directly rather than through a gateway's worker.
func youkiRun(t *testing.T, socket, command string, timeout time.Duration) (string, int) {
	t.Helper()
	connection, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		t.Fatalf("connect to the youki supervisor at %s: %v", socket, err)
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	request, err := json.Marshal(map[string]any{
		"type": "run",
		"id":   "1",
		"argv": []string{"/bin/sh", "-c", command},
		"env":  map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		"dir":  "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(append(request, '\n')); err != nil {
		t.Fatalf("write the supervisor request: %v", err)
	}
	var out strings.Builder
	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Code int    `json:"code"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		switch event.Type {
		case "chunk":
			raw, err := base64.StdEncoding.DecodeString(event.Data)
			if err == nil {
				out.Write(raw)
			}
		case "exit":
			return out.String(), event.Code
		}
	}
	return out.String(), -1
}
