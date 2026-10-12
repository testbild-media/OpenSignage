//go:build opensignage_selftest_fail

package updates

// Built only for the real rollback test: the self-test of this build always fails.
func init() { forcedSelfTestFailure = "forced self-test failure (build tag opensignage_selftest_fail)" }
