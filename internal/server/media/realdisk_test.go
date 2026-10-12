package media

import (
	"math/rand"
	"os"
	"testing"
)

// TestRealDiskFullDuringChunk needs a tiny file system, e.g. on Linux:
//
//	sudo mkdir -p /mnt/small && sudo mount -t tmpfs -o size=4m tmpfs /mnt/small
//	sudo chmod 777 /mnt/small && OS_TEST_SMALLFS=/mnt/small go test -run RealDiskFull ./internal/server/media
//
// It proves that a write failing with ENOSPC becomes a clean 507 disk_full and leaves a
// consistent, resumable upload behind (no 500, no corrupt state).
func TestRealDiskFullDuringChunk(t *testing.T) {
	small := os.Getenv("OS_TEST_SMALLFS")
	if small == "" {
		t.Skip("OS_TEST_SMALLFS not set")
	}
	dataDir, err := os.MkdirTemp(small, "media-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	h := newHarnessAt(t, dataDir, t.TempDir())
	h.m.diskFn = func(string) (uint64, uint64, error) { return 1 << 40, 1 << 39, nil } // the guard believes there is plenty
	c := h.login("operator")
	data := make([]byte, 10<<20)
	rand.New(rand.NewSource(1)).Read(data)
	copy(data, "\x89PNG\r\n\x1a\n")
	code, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "big.png", "size": len(data)})
	if code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	id := out["uploadId"].(string)
	const chunk = 1 << 20
	var failed bool
	for off := 0; off < len(data); off += chunk {
		code, out = c.putChunk(id, data, off, off+chunk-1)
		if code == 507 {
			if errCode(out) != "disk_full" {
				t.Fatalf("507 with wrong code: %v", out)
			}
			failed = true
			break
		}
		if code != 200 {
			t.Fatalf("unexpected status %d: %v", code, out)
		}
	}
	if !failed {
		t.Fatal("the tiny file system should have run full")
	}
	var received int64
	_ = h.app.DB.R.QueryRow(`SELECT received FROM uploads WHERE id = ?`, id).Scan(&received)
	if received <= 0 || received >= int64(len(data)) || received%chunk != 0 {
		t.Fatalf("received must stay at the last complete chunk, got %d", received)
	}
	// aborting frees the space again
	if code, _ := c.do("DELETE", "/api/v1/media/uploads/"+id, nil); code != 204 {
		t.Fatalf("abort: %d", code)
	}
	if entries, _ := os.ReadDir(h.m.tmpDir("uploads")); len(entries) != 0 {
		t.Fatalf("part file must be removed: %d", len(entries))
	}
}
