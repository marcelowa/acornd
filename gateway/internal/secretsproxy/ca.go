// Package secretsproxy serves a workspace's secrets to the commands that need them: the guest
// holds a placeholder, and this swaps in the real value for the hosts a secret allows and nowhere
// else. It is the bwrap engine's answer to what a microVM engine does on its own egress path.
// See docs/adr/0031-a-proxy-for-bwrap-secrets.md.
package secretsproxy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// caFile holds the key and the certificate in one file. One file rather than two: a key without
// its certificate is a CA no client can trust and no code can use, and two files are two writes
// that a crash can land between.
const caFile = "ca.pem"

// maxCAFile is the largest a CA file may be before it is not one: a 2048-bit RSA key and a
// certificate are under 4 KiB, and refusing early beats parsing whatever a full disk left behind.
const maxCAFile = 64 << 10

// CA is the install's signing certificate. One per install, not one per workspace: the expensive
// half of interception is the trust store inside the sandbox, and one certificate pays that once.
// Rotating it is deleting the file, which every workspace then has to be recreated to hear about.
type CA struct {
	certificate tls.Certificate
}

// LoadOrCreateCA reads the install's proxy CA, creating it the first time.
//
// A CA is generated exactly once. Two gateways can start at the same moment, and a second CA
// would silently invalidate the trust every running workspace installed, so the write is a link
// (which fails rather than replaces): the loser of that race reads the winner's certificate.
func LoadOrCreateCA(dir string) (*CA, error) {
	path := filepath.Join(dir, caFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secrets proxy: preparing the CA folder: %w", err)
	}
	ca, err := readCA(path)
	switch {
	case err == nil:
		return ca, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}

	combined, err := generateCA()
	if err != nil {
		return nil, err
	}
	if err := createNew(dir, path, combined); err != nil {
		return nil, err
	}
	// A CA that cannot be read back is a refusal, not a second generation: whatever is at that
	// path is what the workspaces will trust, and quietly replacing it would break them.
	return readCA(path)
}

// createNew writes a file that must not already exist, atomically. The temp file is in the same
// folder so the link cannot cross a filesystem, and `link` refuses an existing target rather than
// replacing it, which is the property a trust anchor needs.
func createNew(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".ca-*.pem")
	if err != nil {
		return fmt.Errorf("secrets proxy: writing the CA: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	// CreateTemp makes 0600; the chmod is for a system whose umask or tempdir policy disagrees.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secrets proxy: setting the CA file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secrets proxy: writing the CA: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("secrets proxy: closing the CA file: %w", err)
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Another process created it between our read and our link. Its file is the install's
			// CA now, and the caller reads that one.
			return nil
		}
		return fmt.Errorf("secrets proxy: installing the CA: %w", err)
	}
	return nil
}

// readCA parses the key and the certificate, and refuses anything else loudly: a half-written or
// hand-edited file must not become a CA that signs nothing a client trusts.
func readCA(path string) (*CA, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxCAFile {
		return nil, fmt.Errorf("secrets proxy: %s is %d bytes, which is too large to be a CA: remove it and restart to have one made", path, info.Size())
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secrets proxy: %s is readable by other users (mode %04o): the proxy's key must be 0600, so set it and restart", path, info.Mode().Perm())
	}
	combined, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secrets proxy: reading the CA: %w", err)
	}
	pair, err := tls.X509KeyPair(combined, combined)
	if err != nil {
		return nil, fmt.Errorf("secrets proxy: %s is not a usable CA: remove it and restart to have one made (%w)", path, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("secrets proxy: %s holds a certificate that cannot be read: remove it and restart to have one made (%w)", path, err)
	}
	if !leaf.IsCA || leaf.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, fmt.Errorf("secrets proxy: %s holds a certificate that may not sign others: remove it and restart to have one made", path)
	}
	pair.Leaf = leaf
	return &CA{certificate: pair}, nil
}

// generateCA makes the signing key and its certificate. RSA 2048 rather than ECDSA: the CA is
// made once, but every host certificate is signed by it and presented to clients this project does
// not choose — an old toolchain that rejects an ECDSA issuer would fail every secret, and the
// cost of the conservative choice is one slow signature per host, cached.
func generateCA() ([]byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("secrets proxy: generating the CA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("secrets proxy: generating the CA serial: %w", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		// A name a person can recognise in a trust store, because that is where they will meet it.
		Subject:   pkix.Name{CommonName: "Acornfall Workspace Proxy CA", Organization: []string{"acornfall"}},
		NotBefore: now.Add(-time.Hour),
		// Ten years: an expiry is a rotation nobody scheduled, and rotating this one means every
		// workspace is recreated. Deleting the file is the supported way to rotate it.
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("secrets proxy: signing the CA certificate: %w", err)
	}
	combined := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	combined = append(combined, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})...)
	return combined, nil
}

// CAPath is where an install's CA lives: the engine that builds a sandbox's trust bundle needs the
// file, not the certificate, because the bundle is assembled from whatever the image already has.
func CAPath(dir string) string { return filepath.Join(dir, caFile) }
