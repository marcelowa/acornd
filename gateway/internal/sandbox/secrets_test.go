package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPolicyRenderKeepsSecretValuesOut(t *testing.T) {
	policy := Policy{
		Workspace: "/home/me/work",
		Egress:    true,
		Secrets: []Secret{{
			Name: "GITHUB_TOKEN", Description: "release token", Value: "value-must-not-be-here",
			Hosts:   []string{"api.github.com", "*.githubusercontent.com"},
			Headers: true, Query: false, Body: false,
		}},
	}
	rendered := string(policy.Render())
	if strings.Contains(rendered, "value-must-not-be-here") {
		t.Fatalf("the policy file must not hold a value:\n%s", rendered)
	}
	for _, want := range []string{
		"secrets:",
		"GITHUB_TOKEN:",
		"value: { $msb_env: ACORNFALL_SECRET_0 }",
		"allow: [api.github.com, \"*.githubusercontent.com\"]",
		"substitution: { headers: true, query: false, body: false }",
		"require_tls_identity: true",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("policy should contain %q:\n%s", want, rendered)
		}
	}
}

// envRecorder is both a Runner and an EnvRunner, so a create's secret environment is
// visible to the test without reaching the real engine.
type envRecorder struct {
	calls [][]string
	envs  [][]string
}

func (r *envRecorder) Run(_ context.Context, args ...string) (string, error) {
	return r.RunEnv(context.Background(), nil, args...)
}

func (r *envRecorder) RunEnv(_ context.Context, env []string, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	r.envs = append(r.envs, env)
	return "", nil
}

func TestCreatePassesSecretValuesInTheProcessEnvironment(t *testing.T) {
	recorder := &envRecorder{}
	driver := NewMicrosandbox(shortTempDir(t)).WithRunner(recorder)
	driver.installed = true
	dir := t.TempDir()
	policy := Policy{
		Workspace: "/home/me/work",
		Egress:    true,
		Secrets: []Secret{{
			Name: "GITHUB_TOKEN", Value: "ghp-value", Hosts: []string{"api.github.com"},
			Headers: true,
		}},
	}
	if err := driver.Create(context.Background(), policy, dir); err != nil {
		t.Fatal(err)
	}
	var createEnv []string
	for index, call := range recorder.calls {
		if len(call) > 0 && call[0] == "create" && (len(call) < 2 || call[1] != "--help") {
			createEnv = recorder.envs[index]
		}
	}
	if !contains(createEnv, "ACORNFALL_SECRET_0=ghp-value") {
		t.Fatalf("the value should be in the create process environment, got %v", createEnv)
	}
	written, err := os.ReadFile(filepath.Join(dir, "sandbox.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "ghp-value") {
		t.Fatalf("the policy file must not hold the value:\n%s", written)
	}
}

func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}

func TestSecretSpecUsesTheEngineSpelling(t *testing.T) {
	cases := []struct {
		secret Secret
		want   string
	}{
		{secret: Secret{Name: "TOKEN", Hosts: []string{"api.example.com"}, Headers: true}, want: "TOKEN@api.example.com"},
		{secret: Secret{Name: "TOKEN", Hosts: []string{"api.example.com", "*.example.org"}, Headers: true, Query: true}, want: "TOKEN:query@api.example.com,*.example.org"},
		{secret: Secret{Name: "TOKEN", Hosts: []string{"api.example.com"}, Query: true, Body: true}, want: "TOKEN:no-headers,query,body@api.example.com"},
	}
	for _, test := range cases {
		if got := secretSpec(test.secret); got != test.want {
			t.Errorf("secretSpec(%+v) = %q, want %q", test.secret, got, test.want)
		}
	}
}

func TestMergeEnvLastWins(t *testing.T) {
	merged := mergeEnv([]string{"PATH=/bin", "TOKEN=host"}, []string{"MSB_HOME=/state"}, []string{"TOKEN=secret"})
	if !contains(merged, "TOKEN=secret") || !contains(merged, "PATH=/bin") || !contains(merged, "MSB_HOME=/state") {
		t.Fatalf("unexpected merge: %v", merged)
	}
	for _, entry := range merged {
		if strings.HasPrefix(entry, "TOKEN=") && entry != "TOKEN=secret" {
			t.Fatalf("the host variable won: %v", merged)
		}
	}
}

func TestSameSecretPolicyComparesHostsAndSubstitution(t *testing.T) {
	desired := []Secret{{
		Name: "TOKEN", Hosts: []string{"api.example.com", "*.example.org"},
		Headers: true, Query: true,
	}}
	current := []engineSecret{{
		EnvVar:       "TOKEN",
		AllowedHosts: []json.RawMessage{json.RawMessage(`{"exact":"api.example.com"}`), json.RawMessage(`{"wildcard":"*.example.org"}`)},
	}}
	current[0].Substitution.Headers = true
	current[0].Substitution.Query = true
	if !sameSecretPolicy(current, desired) {
		t.Fatal("equal policies should compare equal")
	}
	current[0].Substitution.Query = false
	if sameSecretPolicy(current, desired) {
		t.Fatal("a different substitution should not compare equal")
	}
}

// fakeEngine is a running sandbox whose stored and active secret sets the test controls. It
// applies a live modify to both, a next-start modify to the stored copy, and can refuse a
// live apply the way the real engine does when a name changes.
type fakeEngine struct {
	name       string
	status     string
	active     []engineSecret
	persisted  []engineSecret
	refuseLive bool
	calls      [][]string
	envs       [][]string
}

func (f *fakeEngine) Run(ctx context.Context, args ...string) (string, error) {
	return f.RunEnv(ctx, nil, args...)
}

func (f *fakeEngine) RunEnv(_ context.Context, env []string, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	f.envs = append(f.envs, env)
	switch {
	case len(args) == 0:
		return "", nil
	case args[0] == "list":
		if f.status == "" {
			return "[]", nil
		}
		payload, _ := json.Marshal([]map[string]string{{"name": f.name, "status": f.status}})
		return string(payload), nil
	case args[0] == "inspect":
		payload, _ := json.Marshal(map[string]any{
			"active_config": map[string]any{"network": map[string]any{"secrets": map[string]any{"secrets": f.active}}},
			"config":        map[string]any{"network": map[string]any{"secrets": map[string]any{"secrets": f.persisted}}},
		})
		return string(payload), nil
	case args[0] == "modify":
		f.modify(args)
		return "", nil
	case args[0] == "create" && len(args) > 1 && args[1] == "--help":
		return "", nil
	default:
		return "", nil
	}
}

func (f *fakeEngine) modify(args []string) {
	nextStart := false
	remove := ""
	spec := ""
	for index := 2; index < len(args); index++ {
		switch args[index] {
		case "--next-start":
			nextStart = true
		case "--secret-rm":
			index++
			remove = args[index]
		case "--secret":
			index++
			spec = args[index]
		}
	}
	if remove != "" {
		f.persisted = withoutSecret(f.persisted, remove)
		if !nextStart {
			f.active = withoutSecret(f.active, remove)
		}
		return
	}
	if spec == "" {
		return
	}
	entry := secretFromSpec(spec)
	if nextStart {
		f.persisted = withSecret(f.persisted, entry)
		return
	}
	if f.refuseLive {
		// The engine answered "this needs a restart": the stored copy changed, the running
		// one did not.
		f.persisted = withSecret(f.persisted, entry)
		f.active = nil
		return
	}
	f.active = withSecret(f.active, entry)
	f.persisted = withSecret(f.persisted, entry)
}

func secretFromSpec(spec string) engineSecret {
	at := strings.LastIndex(spec, "@")
	policy := spec[:at]
	hosts := strings.Split(spec[at+1:], ",")
	name := policy
	options := ""
	if before, after, ok := strings.Cut(policy, ":"); ok {
		name, options = before, after
	}
	entry := engineSecret{EnvVar: name}
	entry.Substitution.Headers = true
	for option := range strings.SplitSeq(options, ",") {
		switch option {
		case "no-headers":
			entry.Substitution.Headers = false
		case "query":
			entry.Substitution.Query = true
		case "body":
			entry.Substitution.Body = true
		}
	}
	for _, host := range hosts {
		kind := "exact"
		if strings.HasPrefix(host, "*.") {
			kind = "wildcard"
		}
		pattern, _ := json.Marshal(map[string]string{kind: host})
		entry.AllowedHosts = append(entry.AllowedHosts, pattern)
	}
	return entry
}

func withSecret(entries []engineSecret, entry engineSecret) []engineSecret {
	for index := range entries {
		if entries[index].EnvVar == entry.EnvVar {
			entries[index] = entry
			return entries
		}
	}
	return append(entries, entry)
}

func withoutSecret(entries []engineSecret, name string) []engineSecret {
	out := entries[:0]
	for _, entry := range entries {
		if entry.EnvVar != name {
			out = append(out, entry)
		}
	}
	return out
}

func callsStarting(calls [][]string, command string) [][]string {
	out := [][]string{}
	for _, call := range calls {
		if len(call) > 0 && call[0] == command {
			out = append(out, call)
		}
	}
	return out
}

func callIndex(calls [][]string, command string) int {
	for index, call := range calls {
		if len(call) > 0 && call[0] == command {
			return index
		}
	}
	return -1
}

func secretDriverFor(t *testing.T, engine *fakeEngine) *Microsandbox {
	driver := NewMicrosandbox(shortTempDir(t)).WithRunner(engine)
	driver.installed = true
	return driver
}

func engineToken() engineSecret {
	entry := engineSecret{EnvVar: "TOKEN", AllowedHosts: []json.RawMessage{json.RawMessage(`{"exact":"api.example.com"}`)}}
	entry.Substitution.Headers = true
	return entry
}

func TestSecretsChangedRotatesLive(t *testing.T) {
	workspace := "/home/me/work"
	engine := &fakeEngine{name: Name(workspace), status: "Running", active: []engineSecret{engineToken()}, persisted: []engineSecret{engineToken()}}
	driver := secretDriverFor(t, engine)
	change, err := driver.SecretsChanged(context.Background(), engine.name, []Secret{{
		Name: "TOKEN", Value: "new-value", Hosts: []string{"api.example.com"}, Headers: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if change.RestartRequired {
		t.Error("rotating an existing secret should be live")
	}
	modifies := callsStarting(engine.calls, "modify")
	if len(modifies) != 1 || contains(modifies[0], "--next-start") {
		t.Fatalf("expected one live modify, got %v", engine.calls)
	}
	index := callIndex(engine.calls, "modify")
	if index < 0 || !contains(engine.envs[index], "TOKEN=new-value") {
		t.Errorf("the value should reach the modify process environment, got %v", engine.envs)
	}
}

func TestSecretsChangedAddsAtNextStart(t *testing.T) {
	workspace := "/home/me/work"
	engine := &fakeEngine{name: Name(workspace), status: "Running"}
	driver := secretDriverFor(t, engine)
	change, err := driver.SecretsChanged(context.Background(), engine.name, []Secret{{
		Name: "TOKEN", Value: "v", Hosts: []string{"api.example.com"}, Headers: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !change.RestartRequired {
		t.Error("adding a secret to a running sandbox needs a restart")
	}
	modifies := callsStarting(engine.calls, "modify")
	if len(modifies) != 1 || !contains(modifies[0], "--next-start") {
		t.Fatalf("expected one next-start modify, got %v", engine.calls)
	}
	if len(engine.active) != 0 || len(engine.persisted) != 1 {
		t.Errorf("only the stored set should change: active=%v persisted=%v", engine.active, engine.persisted)
	}
}

func TestSecretsChangedRemovesAtNextStart(t *testing.T) {
	engine := &fakeEngine{name: Name("/home/me/work"), status: "Running", active: []engineSecret{engineToken()}, persisted: []engineSecret{engineToken()}}
	driver := secretDriverFor(t, engine)
	change, err := driver.SecretsChanged(context.Background(), engine.name, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !change.RestartRequired {
		t.Error("removing a secret from a running sandbox needs a restart")
	}
	modifies := callsStarting(engine.calls, "modify")
	if len(modifies) != 1 || !contains(modifies[0], "--secret-rm") || !contains(modifies[0], "--next-start") {
		t.Fatalf("expected a next-start removal, got %v", engine.calls)
	}
}

func TestSecretsChangedStoppedStoresIt(t *testing.T) {
	engine := &fakeEngine{name: Name("/home/me/work"), status: "Stopped", persisted: []engineSecret{engineToken()}}
	driver := secretDriverFor(t, engine)
	change, err := driver.SecretsChanged(context.Background(), engine.name, []Secret{{
		Name: "TOKEN", Value: "new", Hosts: []string{"api.example.com"}, Headers: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !change.RestartRequired {
		t.Error("a stopped sandbox applies the change at its next start")
	}
	modifies := callsStarting(engine.calls, "modify")
	if len(modifies) != 1 || !contains(modifies[0], "--next-start") {
		t.Fatalf("expected a next-start change, got %v", engine.calls)
	}
}

func TestSecretsChangedAbsentDoesNothing(t *testing.T) {
	engine := &fakeEngine{}
	driver := secretDriverFor(t, engine)
	change, err := driver.SecretsChanged(context.Background(), Name("/home/me/work"), []Secret{{
		Name: "TOKEN", Value: "v", Hosts: []string{"api.example.com"}, Headers: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if change.RestartRequired || len(callsStarting(engine.calls, "modify")) != 0 {
		t.Fatalf("an absent sandbox is nothing to change: %v %v", change, engine.calls)
	}
}

func TestSecretsChangedFallsBackWhenLiveIsRefused(t *testing.T) {
	engine := &fakeEngine{
		name: Name("/home/me/work"), status: "Running",
		active: []engineSecret{engineToken()}, persisted: []engineSecret{engineToken()},
		refuseLive: true,
	}
	driver := secretDriverFor(t, engine)
	change, err := driver.SecretsChanged(context.Background(), engine.name, []Secret{{
		Name: "TOKEN", Value: "v", Hosts: []string{"api.example.com"}, Headers: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !change.RestartRequired {
		t.Error("a refused live change should report a restart")
	}
	modifies := callsStarting(engine.calls, "modify")
	if len(modifies) != 2 {
		t.Fatalf("expected a live try then a next-start retry, got %v", engine.calls)
	}
	if contains(modifies[0], "--next-start") {
		t.Errorf("the first try should be live: %v", modifies)
	}
	if !contains(modifies[1], "--next-start") {
		t.Errorf("the retry should store it: %v", modifies)
	}
}
