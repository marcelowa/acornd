// Package control's support bundle: the one workspace's diagnosis, made where the vault is.
//
// The collector lives in internal/support and knows nothing about control. This file is the
// seam: it resolves the workspace, the vault values that must be redacted, and the permission
// that says a bundle is the owner's act rather than any member's.
package control

import (
	"context"

	"github.com/marcelowa/acornd/gateway/internal/support"
	"github.com/marcelowa/acornd/gateway/internal/update"
	"github.com/marcelowa/acornd/gateway/internal/vault"
)

// SupportBundle collects a workspace's diagnosis. The caller must be the install's admin or
// the room's owner: a guest can read the room, but a bundle carries the machine's logs and
// process groups, which are not the room's to give.
func (c *Control) SupportBundle(ctx context.Context, caller Caller) (*support.Bundle, error) {
	_ = ctx
	if !caller.Admin && !c.May(caller, ActionAdminister) {
		return nil, ErrNotAllowed
	}
	return support.Collect(c.supportOptions())
}

// SupportPreview is the same collection with the bytes dropped: what the console shows before
// a person downloads anything.
func (c *Control) SupportPreview(ctx context.Context, caller Caller) (support.Preview, error) {
	bundle, err := c.SupportBundle(ctx, caller)
	if err != nil {
		return support.Preview{}, err
	}
	return bundle.Preview, nil
}

func (c *Control) supportOptions() support.Options {
	c.mu.RLock()
	workspace := c.opts.Workspace
	dataDir := c.opts.DataDir
	userHome := c.opts.UserHome
	vaultStore := c.opts.Vault
	workerUp := c.workerUp
	workerError := c.workerError
	sandbox := c.sandbox
	c.mu.RUnlock()
	engine := workspace.Engine
	if driver := c.sandboxDriver(); driver != nil {
		engine = driver.Name()
	}
	state := map[string]any{
		"workerUp":       workerUp,
		"workerError":    workerError,
		"sandboxTools":   sandbox.Tools,
		"sandboxProblem": sandbox.Problem,
		"sandboxEngine":  sandbox.Engine,
	}
	options := support.Options{
		DataDir:       dataDir,
		Workspace:     workspace.Name,
		WorkspacePath: workspace.Path,
		Engine:        engine,
		Home:          userHome,
		Version:       update.BuildVersion,
		State:         state,
	}
	if vaultStore != nil {
		entries, err := vaultStore.Resolve(workspace.Path)
		if err != nil {
			options.Warnings = append(options.Warnings, "the vault could not be read for redaction: "+err.Error())
		} else {
			options.Secrets = secretValues(entries)
		}
	}
	return options
}

func secretValues(entries []vault.Entry) []string {
	values := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Value != "" {
			values = append(values, entry.Value)
		}
	}
	return values
}
