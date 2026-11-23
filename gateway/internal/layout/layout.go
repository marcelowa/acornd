// Package layout is the install folder's file layout: which file holds what, how a file is replaced so a reader
// never sees half of it, and the lock two processes take before they change one.
//
// Every file name lives here, so a rename is one edit and a reader can see the whole folder in one place. The
// packages that own a file's contents (control, vault, people, remote, update) ask for its path and never
// spell it.
package layout

import "path/filepath"

// Registry is the workspaces registry: which workspaces exist and where their folders are.
func Registry(install string) string { return filepath.Join(install, "workspaces.json") }

// Settings is a defaults file. The install folder has one, the layer every workspace falls back to; each
// workspace's state folder has its own.
func Settings(folder string) string { return filepath.Join(folder, "settings.json") }

// Secrets is the vault (ADR 0021).
func Secrets(install string) string { return filepath.Join(install, "secrets.json") }

// People is the book of who may use the install (ADR 0032 §3). ADR 0014 keeps the install folder out of every
// sandbox mount, which is what makes it a list an agent cannot read.
func People(install string) string { return filepath.Join(install, "people.json") }

// Remote is the install's remote access: what the admin asked for, and the tunnel's token (ADR 0033 §4). It is
// in the install folder for the same reason the book is: no sandbox mounts it.
func Remote(install string) string { return filepath.Join(install, "remote.json") }

// Update is the self-update's own settings (ADR 0019).
func Update(install string) string { return filepath.Join(install, "update.json") }

// Config is the install-wide configuration every sandbox mounts read-only (ADR 0014).
func Config(install string) string { return filepath.Join(install, "config.json") }

// State is where a workspace's storage, its settings and its drafts live: `<install>/state/<name>`, beside the
// registry rather than inside the workspace's folder. It is the folder that makes a name mean a history, so
// forgetting a room leaves it and purging one deletes it.
func State(install, workspace string) string { return filepath.Join(install, "state", workspace) }

// Session is a workspace's conversation storage, in its state folder. An install from before workspaces kept
// it in the install folder itself, which is where the move into a state folder finds it.
func Session(folder string) string { return filepath.Join(folder, "session.sqlite") }

// Drafts is what a workspace's composers hold unsent, in its state folder.
func Drafts(state string) string { return filepath.Join(state, "drafts.json") }
