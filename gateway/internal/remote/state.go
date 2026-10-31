// What the install recorded about its remote access: the desire, and the ids the last apply wrote down.
package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File is where an install keeps its remote access, inside the install folder — which ADR 0014 keeps out of
// every sandbox mount, so an agent cannot read the tunnel's token.
const File = "remote.json"

// Setup is the file: what the admin asked for, and what exists because of it.
type Setup struct {
	Desired Desired `json:"desired"`
	State   State   `json:"state"`
	// APIToken is the Cloudflare token the admin pasted in the console, kept 0600 beside the tunnel's. It is what
	// makes the door follow the book: a person added, revoked or restored is in the group at the next reconcile.
	// It is never returned by the status API and never logged (ADR 0033 §4 as amended).
	APIToken string `json:"api_token,omitempty"`
	// Updated is when the last apply wrote this, so a status screen can say how old the answer is.
	Updated string `json:"updated,omitempty"`
}

// Configured reports whether an apply has ever run.
func (s Setup) Configured() bool { return s.Desired.Hostname != "" }

// Load reads the setup, or an empty one when nothing has been configured. A file that cannot be read is an
// error rather than an empty setup: an install whose record vanished would make a second tunnel.
func Load(dir string) (Setup, error) {
	path := filepath.Join(dir, File)
	payload, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Setup{}, nil
		}
		return Setup{}, err
	}
	var setup Setup
	if err := json.Unmarshal(payload, &setup); err != nil {
		return Setup{}, fmt.Errorf("%s is not a remote access file: %w", path, err)
	}
	return setup, nil
}

// Save writes the setup, 0600 and whole. It holds the tunnel's token, which is a credential that can run one
// tunnel — the narrowest one this install keeps, and the one the supervised process reads (ADR 0033 §4).
func Save(dir string, setup Setup, now time.Time) error {
	if dir == "" {
		return errors.New("remote: a setup needs an install folder")
	}
	setup.Updated = now.UTC().Format(time.RFC3339)
	// #nosec G117 -- writing the token is what this file is for (ADR 0033 §4): it lands in a
	// 0600 file inside a 0700 install folder, which ADR 0014 keeps out of every sandbox mount,
	// and GET /api/remote says only whether one is stored.
	payload, err := json.MarshalIndent(setup, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".remote-*")
	if err != nil {
		return err
	}
	path := temp.Name()
	defer func() { _ = os.Remove(path) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(dir, File))
}

// Env is the identity configuration a run needs once this setup is applied: the three values the assertion
// door checks an assertion against (ADR 0032 §2). The issuer is the team's own domain and the keys live under
// it, so the admin does not have to remember either.
func (s Setup) Env(teamDomain string) map[string]string {
	domain := strings.TrimSuffix(strings.TrimSpace(teamDomain), "/")
	return map[string]string{
		"ACORNFALL_IDENTITY":          "access",
		"ACORNFALL_IDENTITY_AUDIENCE": s.State.Audience,
		"ACORNFALL_IDENTITY_ISSUER":   domain,
		"ACORNFALL_IDENTITY_KEYS":     domain + "/cdn-cgi/access/certs",
	}
}
