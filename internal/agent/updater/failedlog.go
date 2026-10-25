package updater

import (
	"os"
	"strings"
	"time"

	"opensignage/internal/common/logging"
)

// failedLogMax bounds the log that goes with the report of a rollback.
const failedLogMax = 12 << 10

// FailedRunLog is the log of a version that failed and was rolled back, for the report to
// the server: the crash output written since the update started, and the last log lines
// of the previous run (from the start of the update on when they carry a time; all of them
// when none does). Nothing else shows why a new version failed without access to the
// device. At most 12 KiB, the newest part.
func FailedRunLog(previous []byte, crashPath string, since time.Time) string {
	var parts []string
	lines := logging.CompactLines(previous, since, 80)
	if len(lines) == 0 {
		lines = logging.CompactLines(previous, time.Time{}, 80)
	}
	if len(lines) > 0 {
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if crashPath != "" {
		if st, err := os.Stat(crashPath); err == nil && st.Size() > 0 && (since.IsZero() || !st.ModTime().Before(since)) {
			if b := strings.TrimSpace(string(logging.ReadTail(crashPath, 8<<10))); b != "" {
				parts = append(parts, "--- crash ---\n"+b)
			}
		}
	}
	out := strings.Join(parts, "\n")
	if len(out) > failedLogMax {
		out = out[len(out)-failedLogMax:]
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	return out
}
