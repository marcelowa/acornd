package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// EnvRunner is the test seam for a command that needs extra environment entries. A Runner
// that implements it can assert what a secret's value looked like on its way to the engine
// without the test having to read the host's environment.
type EnvRunner interface {
	RunEnv(ctx context.Context, env []string, args ...string) (string, error)
}

// SecretDelivery is host-side substitution: the value never enters the guest.
func (m *Microsandbox) SecretDelivery() Delivery { return DeliverySubstitute }

// SecretEnvInWorker is false: the worker attaches to the guest, which holds the placeholder,
// and never needs the value.
func (m *Microsandbox) SecretEnvInWorker() bool { return false }

// substitutedSecrets are the ones this engine can deliver: a secret with no allowed host has
// nowhere to be substituted, so it is not put in the policy. The control reports it as
// unavailable rather than booting a sandbox that cannot use it.
func substitutedSecrets(secrets []Secret) []Secret {
	out := make([]Secret, 0, len(secrets))
	for _, secret := range secrets {
		if len(secret.Hosts) == 0 {
			continue
		}
		out = append(out, secret)
	}
	return out
}

// secretEnvVar is the host variable the policy file references. It is deliberately not the
// guest's name: a host that already exports GITHUB_TOKEN cannot accidentally become the
// source of the sandbox's GITHUB_TOKEN.
func secretEnvVar(index int) string { return fmt.Sprintf("ACORNFALL_SECRET_%d", index) }

// secretEnvValues are the create process's extra environment entries. The values exist only
// here and in the engine's memory; the policy file holds source references.
func secretEnvValues(secrets []Secret) []string {
	values := []string{}
	for index, secret := range substitutedSecrets(secrets) {
		values = append(values, secretEnvVar(index)+"="+secret.Value)
	}
	return values
}

// startSecretEnv carries both source spellings: a config created from sandbox.yaml uses the
// ACORNFALL_SECRET_<n> source, while one changed by `msb modify --secret NAME@HOST` stores
// the guest name as its source. The start process has to satisfy whichever is stored.
func startSecretEnv(secrets []Secret) []string {
	values := append([]string(nil), secretEnvValues(secrets)...)
	for _, secret := range substitutedSecrets(secrets) {
		values = append(values, secret.Name+"="+secret.Value)
	}
	return values
}

// mergeEnv combines environment groups, last wins. A secret named GITHUB_TOKEN must not be
// shadowed by the host's own GITHUB_TOKEN, so a duplicate key is replaced rather than passed
// twice to execve.
func mergeEnv(groups ...[]string) []string {
	values := map[string]string{}
	order := []string{}
	for _, group := range groups {
		for _, entry := range group {
			key, value, ok := strings.Cut(entry, "=")
			if !ok || key == "" {
				continue
			}
			if _, seen := values[key]; !seen {
				order = append(order, key)
			}
			values[key] = value
		}
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+values[key])
	}
	return out
}

// engineSecret is the part of the engine's stored secret entry this side reads. Values are
// never in it.
type engineSecret struct {
	EnvVar       string            `json:"env_var"`
	AllowedHosts []json.RawMessage `json:"allowed_hosts"`
	Substitution struct {
		Headers bool `json:"headers"`
		Query   bool `json:"query"`
		Body    bool `json:"body"`
	} `json:"substitution"`
}

type engineConfig struct {
	Network struct {
		Secrets *struct {
			Secrets []engineSecret `json:"secrets"`
		} `json:"secrets"`
	} `json:"network"`
}

func (c engineConfig) secrets() []engineSecret {
	if c.Network.Secrets == nil {
		return nil
	}
	return c.Network.Secrets.Secrets
}

type engineInspect struct {
	ActiveConfig engineConfig `json:"active_config"`
	Config       engineConfig `json:"config"`
}

// inspect reads the engine's own view of one sandbox: what is running and what is stored for
// next start. It is a read, never a mutation.
func (m *Microsandbox) inspect(ctx context.Context, name string) (engineInspect, error) {
	out, err := m.run(ctx, "inspect", name, "--format", "json")
	if err != nil {
		return engineInspect{}, err
	}
	var inspect engineInspect
	if err := json.Unmarshal([]byte(out), &inspect); err != nil {
		return engineInspect{}, fmt.Errorf("cannot read sandbox %s config: %w", name, err)
	}
	return inspect, nil
}

// sameSecretPolicy compares the engine's entries to the desired ones by the fields the
// engine stores. Values are absent on purpose: a rotation cannot be seen here, only assumed
// when the policy matches and the engine accepted the live change.
func sameSecretPolicy(current []engineSecret, desired []Secret) bool {
	wanted := substitutedSecrets(desired)
	if len(current) != len(wanted) {
		return false
	}
	byName := map[string]engineSecret{}
	for _, secret := range current {
		byName[secret.EnvVar] = secret
	}
	for _, secret := range wanted {
		have, ok := byName[secret.Name]
		if !ok {
			return false
		}
		if have.Substitution.Headers != secret.Headers ||
			have.Substitution.Query != secret.Query ||
			have.Substitution.Body != secret.Body {
			return false
		}
		if !sameHosts(have.AllowedHosts, secret.Hosts) {
			return false
		}
	}
	return true
}

func sameHosts(raw []json.RawMessage, hosts []string) bool {
	have := make([]string, 0, len(raw))
	for _, item := range raw {
		var pattern map[string]string
		if err := json.Unmarshal(item, &pattern); err != nil {
			continue
		}
		switch {
		case pattern["exact"] != "":
			have = append(have, "exact:"+strings.ToLower(pattern["exact"]))
		case pattern["wildcard"] != "":
			have = append(have, "wildcard:"+strings.ToLower(pattern["wildcard"]))
		case pattern["any"] != "":
			have = append(have, "any")
		}
	}
	want := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(host)
		if strings.HasPrefix(host, "*.") {
			want = append(want, "wildcard:"+host)
		} else {
			want = append(want, "exact:"+host)
		}
	}
	sort.Strings(have)
	sort.Strings(want)
	if len(have) != len(want) {
		return false
	}
	for i := range have {
		if have[i] != want[i] {
			return false
		}
	}
	return true
}

// SecretsChanged reconciles a running sandbox with the vault. The engine can rotate the
// value of a secret that is already there, live; adding or removing a name needs a restart,
// and so does a metadata change it will not take while running. In those cases the stored
// config is updated for the next start and RestartRequired says so. A sandbox that does not
// exist yet gets the set when it is created.
func (m *Microsandbox) SecretsChanged(ctx context.Context, name string, secrets []Secret) (SecretChange, error) {
	instances, err := m.List(ctx)
	if err != nil {
		return SecretChange{}, err
	}
	found := false
	running := false
	for _, instance := range instances {
		if instance.Name == name {
			found = true
			running = instance.Running()
			break
		}
	}
	if !found {
		return SecretChange{}, nil
	}
	inspect, err := m.inspect(ctx, name)
	if err != nil {
		return SecretChange{}, err
	}
	active := inspect.ActiveConfig.secrets()
	persisted := inspect.Config.secrets()
	desired := substitutedSecrets(secrets)

	if !running {
		if err := m.applySecretSet(ctx, name, persisted, desired, true); err != nil {
			return SecretChange{}, err
		}
		return SecretChange{RestartRequired: true}, nil
	}
	// A stored change waiting for a start, or a set the stored config does not have, is a
	// restart requirement. Keep the stored config current and say so.
	if !sameSecretPolicy(active, desired) || !sameSecretPolicy(persisted, desired) {
		if err := m.applySecretSet(ctx, name, persisted, desired, true); err != nil {
			return SecretChange{}, err
		}
		return SecretChange{RestartRequired: true}, nil
	}

	// The name set and the policy are already in the running sandbox: this is a rotation, or
	// a metadata change the engine can take live. Apply and then read back what it did.
	if err := m.applySecretSet(ctx, name, active, desired, false); err != nil {
		if restartHint(err) {
			if retry := m.applySecretSet(ctx, name, persisted, desired, true); retry != nil {
				return SecretChange{}, retry
			}
			return SecretChange{RestartRequired: true}, nil
		}
		return SecretChange{}, err
	}
	after, err := m.inspect(ctx, name)
	if err != nil {
		return SecretChange{}, err
	}
	if !sameSecretPolicy(after.ActiveConfig.secrets(), desired) {
		if err := m.applySecretSet(ctx, name, persisted, desired, true); err != nil {
			return SecretChange{}, err
		}
		return SecretChange{RestartRequired: true}, nil
	}
	return SecretChange{}, nil
}

// applySecretSet makes the engine's stored or running set match desired. nextStart writes the
// config for the next boot; otherwise it applies to the running sandbox.
func (m *Microsandbox) applySecretSet(ctx context.Context, name string, current []engineSecret, desired []Secret, nextStart bool) error {
	wanted := map[string]bool{}
	for _, secret := range substitutedSecrets(desired) {
		wanted[secret.Name] = true
	}
	for _, secret := range current {
		if wanted[secret.EnvVar] {
			continue
		}
		args := []string{"modify", name, "--secret-rm", secret.EnvVar}
		if nextStart {
			args = append(args, "--next-start")
		}
		if _, err := m.run(ctx, args...); err != nil {
			return err
		}
	}
	for _, secret := range substitutedSecrets(desired) {
		args := []string{"modify", name, "--secret", secretSpec(secret)}
		if nextStart {
			args = append(args, "--next-start")
		}
		// The engine reads the value from the host environment variable named by the secret,
		// and never from argv.
		env := []string{secret.Name + "=" + secret.Value}
		if _, err := m.runEnv(ctx, env, args...); err != nil {
			if restartHint(err) {
				return fmt.Errorf("%w: %w", errRestartRequired, err)
			}
			return err
		}
	}
	return nil
}

// errRestartRequired is an internal signal: a live apply was refused, so the caller retries
// against the stored config. It never reaches a user.
var errRestartRequired = fmt.Errorf("secret change requires a restart")

func restartHint(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "restart") || strings.Contains(message, "no changes were applied")
}

func secretSpec(secret Secret) string {
	options := []string{}
	if !secret.Headers {
		options = append(options, "no-headers")
	}
	if secret.Query {
		options = append(options, "query")
	}
	if secret.Body {
		options = append(options, "body")
	}
	spec := secret.Name
	if len(options) > 0 {
		spec += ":" + strings.Join(options, ",")
	}
	return spec + "@" + strings.Join(secret.Hosts, ",")
}
