// Secret delivery: what a sandbox engine can do with a value a command needs. The vault is
// engine-neutral; this is the part that is not. See docs/adr/0021-a-vault-for-tool-secrets.md.
package sandbox

// Delivery is how an engine makes a secret available to a command.
type Delivery int

const (
	// DeliverySubstitute keeps the value on the host and puts a placeholder in the guest. The
	// engine substitutes the real value only for the secret's allowed hosts. The value never
	// enters the sandbox.
	DeliverySubstitute Delivery = iota
	// DeliveryEnvironment puts the value in the sandbox environment. The guest can read it,
	// and the interface must say so.
	DeliveryEnvironment
)

func (d Delivery) String() string {
	switch d {
	case DeliverySubstitute:
		return "substitute"
	case DeliveryEnvironment:
		return "environment"
	default:
		return "unknown"
	}
}

// Secret is one value a sandbox receives. Value is handed only to a Driver; Policy.Render
// writes a source reference, never this field.
type Secret struct {
	Name        string
	Description string
	Value       string
	Hosts       []string
	Headers     bool
	Query       bool
	Body        bool
	AllowEnv    bool
}

// SecretChange says how an update landed. RestartRequired is true when the engine could not
// put the change into the running sandbox: the next start will have it.
type SecretChange struct {
	RestartRequired bool
}
