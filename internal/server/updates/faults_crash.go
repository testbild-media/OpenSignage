//go:build opensignage_crash_on_start

package updates

// Built only for the real rollback test: this build crashes on every start, before it
// can check itself (exercises systemd OnFailure → previous binary `rollback`).
func init() { forcedStartupCrash = "forced crash on start (build tag opensignage_crash_on_start)" }
