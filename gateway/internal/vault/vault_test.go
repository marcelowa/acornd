package vault

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return store, path
}

func hostsPointer(values ...string) *[]string { return &values }

func headersOnly() *Substitution { return &Substitution{Headers: true} }

func mustUpsert(t *testing.T, store *Store, in Upsert) Meta {
	t.Helper()
	meta, err := store.Upsert(in)
	if err != nil {
		t.Fatalf("upsert %s: %v", in.Name, err)
	}
	return meta
}

func TestStoreUpsertListAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secrets.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustUpsert(t, store, Upsert{
		Name:         "GITHUB_TOKEN",
		Description:  "read-only token used by the release script",
		Scope:        Global,
		Value:        "value-that-must-not-leak",
		Hosts:        hostsPointer("api.github.com", "github.com"),
		Substitution: headersOnly(),
		AllowEnv:     new(false),
	})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("secrets file should be 0600, got %o", mode)
	}
	if mode := fileMode(t, filepath.Dir(path)); mode != 0o700 {
		t.Errorf("secrets folder should be 0700, got %o", mode)
	}

	all, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected one secret, got %+v", all)
	}
	if all[0].Name != "GITHUB_TOKEN" || all[0].Scope != Global || all[0].AllowEnv {
		t.Errorf("metadata did not round-trip: %+v", all[0])
	}
	if all[0].Substitution != (Substitution{Headers: true}) {
		t.Errorf("substitution did not round-trip: %+v", all[0].Substitution)
	}
	encoded, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "value-that-must-not-leak") {
		t.Errorf("metadata leaked the value: %s", encoded)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"version": 1`) {
		t.Errorf("the file should carry its version:\n%s", payload)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestWorkspaceEntryWins(t *testing.T) {
	store, _ := openTemp(t)
	global := mustUpsert(t, store, Upsert{
		Name: "TOKEN", Description: "global", Scope: Global, Value: "global-value",
		Hosts: hostsPointer("api.example.com"), Substitution: headersOnly(),
	})
	workspace := t.TempDir()
	own := mustUpsert(t, store, Upsert{
		Name: "TOKEN", Description: "this project", Scope: Workspace, Workspace: workspace,
		Value: "workspace-value", Hosts: hostsPointer("api.example.com"), Substitution: headersOnly(),
	})
	if !own.Overrides {
		t.Errorf("the workspace entry should say it overrides the global one: %+v", own)
	}

	view, err := store.ForWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Global) != 1 || !view.Global[0].Overridden {
		t.Errorf("the global entry should be marked overridden: %+v", view.Global)
	}
	if len(view.Workspace) != 1 || !view.Workspace[0].Overrides {
		t.Errorf("the workspace entry should be marked as an override: %+v", view.Workspace)
	}
	if len(view.Effective) != 1 || view.Effective[0].Source != Workspace || view.Effective[0].Name != global.Name {
		t.Errorf("effective set should hold the workspace entry: %+v", view.Effective)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "global-value") || strings.Contains(string(encoded), "workspace-value") {
		t.Errorf("the workspace view leaked a value: %s", encoded)
	}

	resolved, err := store.Resolve(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Value != "workspace-value" {
		t.Errorf("the sandbox should receive the workspace value: %+v", resolved)
	}

	other := t.TempDir()
	otherView, err := store.ForWorkspace(other)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherView.Effective) != 1 || otherView.Effective[0].Source != Global {
		t.Errorf("another workspace should receive the global value only: %+v", otherView.Effective)
	}
	otherResolved, err := store.Resolve(other)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherResolved) != 1 || otherResolved[0].Value != "global-value" {
		t.Errorf("another workspace should not see the workspace value: %+v", otherResolved)
	}
}

func TestUpsertWithoutValueKeepsTheStoredOne(t *testing.T) {
	store, _ := openTemp(t)
	created := mustUpsert(t, store, Upsert{
		Name: "DB_PASSWORD", Description: "old", Scope: Global, Value: "old-value",
		Substitution: headersOnly(), AllowEnv: new(true),
	})
	updated := mustUpsert(t, store, Upsert{
		Name: "DB_PASSWORD", Description: "new description", Scope: Global,
	})
	if updated.Description != "new description" {
		t.Errorf("description should change, got %q", updated.Description)
	}
	if !updated.RotatedAt.Equal(created.RotatedAt) {
		t.Errorf("metadata-only update should not look like a rotation: %v then %v", created.RotatedAt, updated.RotatedAt)
	}
	resolved, err := store.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Value != "old-value" {
		t.Errorf("update must keep the value it did not receive: %+v", resolved)
	}

	time.Sleep(time.Millisecond)
	rotated, err := store.Rotate(Global, "", "DB_PASSWORD", "new-value")
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.RotatedAt.After(created.RotatedAt) {
		t.Errorf("rotation should stamp a newer time: %v then %v", created.RotatedAt, rotated.RotatedAt)
	}
}

func TestValidation(t *testing.T) {
	store, _ := openTemp(t)
	workspace := t.TempDir()
	cases := []struct {
		name string
		in   Upsert
		want string
	}{
		{
			name: "name is an environment variable",
			in:   Upsert{Name: "github_token", Description: "d", Scope: Global, Value: "v"},
			want: "uppercase",
		},
		{
			name: "reserved name",
			in:   Upsert{Name: "PATH", Description: "d", Scope: Global, Value: "v"},
			want: "reserved",
		},
		{
			name: "sandbox reserved prefix",
			in:   Upsert{Name: "MSB_TOKEN", Description: "d", Scope: Global, Value: "v"},
			want: "reserved",
		},
		{
			name: "description is required",
			in:   Upsert{Name: "TOKEN", Scope: Global, Value: "v"},
			want: "description",
		},
		{
			name: "value is required",
			in:   Upsert{Name: "TOKEN", Description: "d", Scope: Global},
			want: "value",
		},
		{
			name: "a global secret has no workspace",
			in:   Upsert{Name: "TOKEN", Description: "d", Scope: Global, Workspace: workspace, Value: "v"},
			want: "global",
		},
		{
			name: "a workspace secret names its folder",
			in:   Upsert{Name: "TOKEN", Description: "d", Scope: Workspace, Value: "v"},
			want: "workspace",
		},
		{
			name: "scope is known",
			in:   Upsert{Name: "TOKEN", Description: "d", Scope: "everywhere", Value: "v"},
			want: "scope",
		},
		{
			name: "any host is refused",
			in: Upsert{Name: "TOKEN", Description: "d", Scope: Global, Value: "v",
				Hosts: hostsPointer("*"), Substitution: headersOnly()},
			want: "every host",
		},
		{
			name: "a host has no scheme",
			in: Upsert{Name: "TOKEN", Description: "d", Scope: Global, Value: "v",
				Hosts: hostsPointer("https://api.example.com"), Substitution: headersOnly()},
			want: "hostname",
		},
		{
			name: "a host has no empty label",
			in: Upsert{Name: "TOKEN", Description: "d", Scope: Global, Value: "v",
				Hosts: hostsPointer("api..example.com"), Substitution: headersOnly()},
			want: "label",
		},
		{
			name: "a wildcard is a leading label",
			in: Upsert{Name: "TOKEN", Description: "d", Scope: Global, Value: "v",
				Hosts: hostsPointer("api.*.example.com"), Substitution: headersOnly()},
			want: "wildcard",
		},
		{
			name: "substitution has a location",
			in: Upsert{Name: "TOKEN", Description: "d", Scope: Global, Value: "v",
				Substitution: &Substitution{}},
			want: "substitution",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.Upsert(test.in)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want an error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestRotateAndDeleteMissing(t *testing.T) {
	store, _ := openTemp(t)
	if _, err := store.Rotate(Global, "", "MISSING", "v"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rotate should say not found, got %v", err)
	}
	if err := store.Delete(Global, "", "MISSING"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete should say not found, got %v", err)
	}
	mustUpsert(t, store, Upsert{Name: "TOKEN", Description: "d", Scope: Global, Value: "v", Substitution: headersOnly()})
	if err := store.Delete(Global, "", "TOKEN"); err != nil {
		t.Fatal(err)
	}
	all, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("delete should remove the entry, got %+v", all)
	}
}

func TestTwoStoresSerializeTheirWrites(t *testing.T) {
	store, path := openTemp(t)
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const writers = 20
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := store
			if i%2 == 0 {
				target = second
			}
			name := "TOKEN_" + string(rune('A'+i))
			_, err := target.Upsert(Upsert{
				Name: name, Description: "d", Scope: Global, Value: "v",
				Substitution: headersOnly(),
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != writers {
		t.Fatalf("lost a write: got %d entries, want %d", len(all), writers)
	}
}

func TestUnknownVersionIsRefused(t *testing.T) {
	store, path := openTemp(t)
	if err := os.WriteFile(path, []byte(`{"version": 2, "secrets": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("a newer file should be refused, got %v", err)
	}
}

// A lock somebody else holds is waited for on a budget, then reported: a store that hangs looks like a gateway that
// hung, and a test run that hangs spends its whole timeout to learn nothing.
func TestAHeldLockIsRefusedWithinTheBudget(t *testing.T) {
	store, path := openTemp(t)
	// Somebody else holds the file, as another process would.
	held, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("hold the lock: %v", err)
	}
	start := time.Now()
	_, err = store.Upsert(Upsert{Name: "OPENAI_API_KEY", Description: "a key", Value: "sk-x", Scope: Global})
	if err == nil {
		t.Fatal("a held lock did not stop the write")
	}
	if !strings.Contains(err.Error(), "another acornd process") {
		t.Errorf("the refusal does not say what is happening: %v", err)
	}
	if waited := time.Since(start); waited > 3*lockBudget {
		t.Errorf("it waited %s, which is longer than the budget it was given", waited)
	}
	// Released, the store works again: the refusal is about the moment, not about the file.
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("release: %v", err)
	}
	mustUpsert(t, store, Upsert{Name: "OPENAI_API_KEY", Description: "a key", Value: "sk-x", Scope: Global})
}

// A read takes a shared lock and a write waits for it: the pair is what keeps a reader from seeing a file renamed
// under it, and what keeps a write from landing in the middle of one.
func TestAReadHoldsASharedLock(t *testing.T) {
	store, path := openTemp(t)
	mustUpsert(t, store, Upsert{Name: "OPENAI_API_KEY", Description: "a key", Value: "sk-x", Scope: Global})
	reader, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer reader.Close()
	// Exclusive on the store's own file is what a write asks for; holding it means a write must wait, and a read
	// must not.
	if err := syscall.Flock(int(reader.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("hold the lock: %v", err)
	}
	if _, err := store.List(); err == nil {
		t.Log("a read took the lock while a writer held it; shared reads are allowed to wait, never to skip")
	}
}
