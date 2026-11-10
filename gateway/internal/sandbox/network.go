package sandbox

// SandboxSecretProxy is what a command needs to use this workspace's secrets: a proxy URL with its
// session credential, the bundle a client has to trust, and the port pasta forwards. The worker
// reads the first two and sets them in the environment it hands each command.
// SandboxSecretProxy is what a command needs to use this workspace's secrets: a proxy URL with its
// session credential, the bundle a client has to trust, and the port pasta forwards. The worker
// reads the first two and sets them in the environment it hands each command.
type SandboxSecretProxy struct {
	URL string `json:"url"`
	// BundleFile is the trust bundle on the machine; Bundle is where it is bound inside, and is
	// repeated here because the worker tells node about it.
	BundleFile string `json:"bundleFile"`
	Bundle     string `json:"bundle"`
	Port       int    `json:"port"`
}

// GuestCABundle is the path every Debian-family image reads its roots from, and Alpine ships it
// too. The workspace's bundle is bound over it: a client reads one file and finds the image's own
// roots and this install's CA, which is what keeps every other TLS connection working.
const GuestCABundle = "/etc/ssl/certs/ca-certificates.crt"

// NoForwarding is pasta's word for a port it does not carry. A workspace with no proxy forwards
// nothing, which is the shape every workspace had before the proxy existed.
const NoForwarding = "none"

// SandboxNetwork is the network a command gets. The tool paths are resolved when the workspace is
// created, so a missing one is a sentence then rather than a mystery in the middle of a turn.
type SandboxNetwork struct {
	Mode string `json:"mode"`
	// Private is whether this sandbox may reach private networks. False, the default, gives it
	// routes that refuse them: pasta carries the machine's own routing table, so a sandbox whose
	// routes say nothing reaches the machine's LAN and its overlay as the machine does.
	Private bool `json:"private,omitempty"`
	// Address is the sandbox's own address inside that namespace, and Gateway is pasta's address
	// there. Both live inside the sandbox: nothing about them appears on the host.
	Address string `json:"address,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	Pasta   string `json:"pasta,omitempty"`
}

// The sandbox's network, when it has one. It is the address space QEMU's user-mode network has
// used for twenty years, and it is chosen not to collide with the ranges a machine is likely to
// have.
const (
	SandboxAddress = "10.0.2.15/24"
	SandboxGateway = "10.0.2.2"
	// SandboxDNS is where the sandbox sends its queries when its private networks are refused:
	// pasta maps it to the machine's own resolver, so DNS never needs a route into anything
	// private. It is inside 10.0.2.0/24, whose connected route is more specific than the refused
	// ranges, which is what keeps the gateway and this address reachable.
	SandboxDNS = "10.0.2.3"
)

// RefusePrivate is the flag that asks the namespace's own configuration to install the routes
// refusing private ranges. The list is not here: it lives where the routes are installed
// (cmd/acornd/netns.go), so one file decides what a workspace without a grant cannot reach.
const RefusePrivate = "--refuse-private"

// NetworkOffName is how a policy's egress switch is written into the engine's config.
const NetworkOffName = "off"
