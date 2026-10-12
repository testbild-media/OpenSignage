package ids

import "testing"

func TestNewOrderedAndValid(t *testing.T) {
	prev := ""
	seen := map[string]bool{}
	for i := 0; i < 5000; i++ {
		id := New()
		if !Valid(id) {
			t.Fatalf("invalid id %q", id)
		}
		if id[14] != '7' {
			t.Fatalf("not v7: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate %q", id)
		}
		seen[id] = true
		if prev != "" && id <= prev {
			t.Fatalf("not ordered: %q after %q", id, prev)
		}
		prev = id
	}
}
