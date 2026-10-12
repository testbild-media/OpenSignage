package media

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"opensignage/internal/server/core"
)

// brokenBody yields some bytes and then fails, like a connection that drops mid-request.
type brokenBody struct {
	data []byte
	pos  int
	stop int
}

func (b *brokenBody) Read(p []byte) (int, error) {
	if b.pos >= b.stop {
		return 0, errors.New("connection reset")
	}
	n := copy(p, b.data[b.pos:b.stop])
	b.pos += n
	return n, nil
}

func TestConcurrentDuplicateChunkIsRejectedOnce(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	data := pngBytes(40, 40, 8)
	_, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "dup.png", "size": len(data)})
	id := out["uploadId"].(string)
	codes := make([]int, 6)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = c.putChunk(id, data, 0, 299)
		}()
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, code := range codes {
		switch code {
		case 200:
			ok++
		case 409:
			conflict++
		}
	}
	if ok != 1 || conflict != len(codes)-1 {
		t.Fatalf("a retried chunk must be accepted exactly once: %v", codes)
	}
	// the data is not corrupted by the duplicates
	code, out := c.putChunk(id, data, 300, len(data)-1)
	if code != 200 || out["asset"].(map[string]any)["sha256"] != sha(data) {
		t.Fatalf("completion: %d %v", code, out)
	}
}

func TestInterruptedChunkCanBeResumed(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	data := pngBytes(40, 40, 6)
	if len(data) < 800 {
		t.Fatalf("test image too small: %d", len(data))
	}
	_, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "flaky.png", "size": len(data)})
	id := out["uploadId"].(string)
	if code, _ := c.putChunk(id, data, 0, 199); code != 200 {
		t.Fatal("first chunk")
	}
	// second chunk: the connection dies after 100 of 400 bytes
	req, _ := http.NewRequest("PUT", h.ts.URL+"/api/v1/media/uploads/"+id, &brokenBody{data: data[200:], stop: 100})
	req.ContentLength = 400
	req.Header.Set("Content-Range", fmt.Sprintf("bytes 200-599/%d", len(data)))
	req.Header.Set("X-CSRF-Token", c.csrf)
	req.AddCookie(&http.Cookie{Name: core.CookieName, Value: c.cookie})
	if res, err := http.DefaultClient.Do(req); err == nil {
		res.Body.Close()
		t.Fatal("the broken request must fail on the client side")
	}
	time.Sleep(100 * time.Millisecond)
	// the server recorded nothing for the broken chunk
	code, out := c.do("GET", "/api/v1/media/uploads/"+id, nil)
	if code != 200 || out["received"].(float64) != 200 {
		t.Fatalf("status after the drop: %d %v", code, out)
	}
	// the client resumes at the reported offset and the final file is intact
	code, out = c.putChunk(id, data, 200, len(data)-1)
	if code != 200 || out["complete"] != true {
		t.Fatalf("resume: %d %v", code, out)
	}
	if out["asset"].(map[string]any)["sha256"] != sha(data) {
		t.Fatal("hash after a resumed upload must match the file")
	}
	got, _ := os.ReadFile(h.m.BlobPath(sha(data)))
	if !bytes.Equal(got, data) {
		t.Fatal("stored content differs")
	}
}
