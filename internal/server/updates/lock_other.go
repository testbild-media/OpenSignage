//go:build !linux

package updates

import "os"

// flockTry is a no-op outside Linux (server updates and the rollback unit are Linux only).
func flockTry(*os.File) (bool, error) { return true, nil }
