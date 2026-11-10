package sandbox

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SecretProxyFor assembles what a sandbox needs to trust and reach the workspace's proxy: the port
// pasta forwards, the URL a command puts in HTTPS_PROXY, and a trust bundle built from the image's
// own roots plus this install's CA.
//
// The bundle is the image's roots *and* ours on purpose. Binding only ours would work for a secret
// and break every other TLS connection a command makes, which is a sandbox that cannot install a
// package; binding only the image's is a proxy nobody trusts.
func SecretProxyFor(policy *Policy, workspaceDir, rootfs string) (*SandboxSecretProxy, error) {
	if policy.SecretProxy == nil {
		return nil, nil
	}
	addr := policy.SecretProxy.Addr()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("sandbox: the secrets proxy has no usable address (%q): %w", policy.SecretProxy.URL, err)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number <= 0 || number > 65535 {
		return nil, fmt.Errorf("sandbox: the secrets proxy port %q is not a port", port)
	}
	ca, err := os.ReadFile(policy.SecretProxy.CAFile)
	if err != nil {
		return nil, fmt.Errorf("sandbox: reading the secrets proxy's CA: %w", err)
	}
	bundleFile := filepath.Join(workspaceDir, "ca-bundle.pem")
	if err := WriteTrustBundle(bundleFile, ImageRoots(rootfs), ca); err != nil {
		return nil, err
	}
	return &SandboxSecretProxy{
		URL:        policy.SecretProxy.URL,
		BundleFile: bundleFile,
		Bundle:     GuestCABundle,
		Port:       number,
	}, nil
}

// ImageRoots is the roots a sandbox would have trusted before this install's CA was added: its own
// image's bundle, or the machine's when the workspace binds the host root read-only.
func ImageRoots(rootfs string) []byte {
	paths := []string{GuestCABundle}
	if rootfs != "" {
		paths = append([]string{filepath.Join(rootfs, GuestCABundle)}, paths...)
	}
	for _, path := range paths {
		if data, err := os.ReadFile(path); err == nil {
			return data
		}
	}
	return nil
}

// WriteTrustBundle writes the image's roots followed by the install's CA, each on its own lines so
// a PEM reader sees two blocks rather than one concatenated blob.
func WriteTrustBundle(path string, roots, ca []byte) error {
	bundle := make([]byte, 0, len(roots)+len(ca)+2)
	bundle = appendBlock(bundle, roots)
	bundle = appendBlock(bundle, ca)
	if err := os.WriteFile(path, bundle, 0o644); err != nil {
		return fmt.Errorf("sandbox: writing the trust bundle: %w", err)
	}
	return nil
}

func appendBlock(bundle, block []byte) []byte {
	if len(block) == 0 {
		return bundle
	}
	bundle = append(bundle, block...)
	if !strings.HasSuffix(string(block), "\n") {
		bundle = append(bundle, '\n')
	}
	return bundle
}

// ProxyNeedsRestart is whether the running sandbox and the policy disagree about the one port
// pasta forwards. A rotation keeps the listener and its derived port, so it does not restart
// anything; adding the first proxy, removing the last one, or moving the port does.
func ProxyNeedsRestart(running *SandboxSecretProxy, wanted *SecretProxy) bool {
	if running == nil || wanted == nil {
		return running != nil || wanted != nil
	}
	_, port, err := net.SplitHostPort(wanted.Addr())
	if err != nil {
		return true
	}
	number, err := strconv.Atoi(port)
	return err != nil || running.Port != number
}
