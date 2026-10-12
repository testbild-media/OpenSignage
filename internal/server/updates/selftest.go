package updates

import (
	"context"
	"errors"
	"fmt"

	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/web"
)

// Fault injection for the real update tests. Both are empty in every normal build; the
// build tags opensignage_selftest_fail and opensignage_crash_on_start set them (see
// faults_*.go).
var (
	forcedSelfTestFailure string
	forcedStartupCrash    string
)

// SelfTest checks a freshly installed version before it serves: the database opens and
// migrates, the schema version is current, the database passes a quick integrity check,
// the settings load and validate, and the web interface is embedded. Listening on the
// ports is checked by the normal start (the update is finalized only after that).
func SelfTest(ctx context.Context, dbPath string) error {
	if forcedSelfTestFailure != "" {
		return errors.New(forcedSelfTestFailure)
	}
	d, err := db.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer d.Close()
	want, err := db.LatestVersion()
	if err != nil {
		return err
	}
	got, err := d.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("schema version: %w", err)
	}
	if got < want {
		return fmt.Errorf("schema version %d, expected %d", got, want)
	}
	var check string
	if err := d.R.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&check); err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	if check != "ok" {
		return fmt.Errorf("integrity check: %s", check)
	}
	st, err := core.LoadSettings(ctx, d)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	s := st.Get()
	if err := s.Validate(); err != nil {
		return fmt.Errorf("settings invalid: %w", err)
	}
	return web.Embedded()
}
