package main

import (
	"log/slog"
	"os"

	"opensignage/internal/server/config"
	"opensignage/internal/server/updates"
)

// rollbackCmd restores the database backup and the previous release links recorded in
// the update state (see internal/server/updates). It refuses to run as root, accepts only
// a state naming the configured database and the release root of this binary, and exits
// non-zero when nothing was rolled back, so the rollback unit does not restart the same
// broken release again and again (the failure stays visible in systemd instead).
func rollbackCmd(cfg *config.Config) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	root := ""
	if exe, err := os.Executable(); err == nil {
		if r, err := updates.ReleaseRoot(exe); err == nil {
			root = r
		}
	}
	return updates.RollbackCommand(cfg.DataDir, root, updates.Symlinks{}, os.Stdout, log)
}
