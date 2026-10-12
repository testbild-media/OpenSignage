package locales

import (
	"slices"
	"testing"
)

// TestCompleteness checks that every language has every key with the same
// placeholders, and no empty translations.
func TestCompleteness(t *testing.T) {
	en, err := Load(Default)
	if err != nil {
		t.Fatal(err)
	}
	if len(en) == 0 {
		t.Fatal("no English messages")
	}
	for _, l := range Languages {
		m, err := Load(l.Code)
		if err != nil {
			t.Fatalf("%s: %v", l.Code, err)
		}
		missing := 0
		for k, v := range en {
			tv, ok := m[k]
			if !ok || tv == "" {
				if missing < 5 {
					t.Errorf("%s: missing %s", l.Code, k)
				}
				missing++
				continue
			}
			if !slices.Equal(Placeholders(v), Placeholders(tv)) {
				t.Errorf("%s: placeholders differ in %s: %q vs %q", l.Code, k, v, tv)
			}
		}
		if missing > 5 {
			t.Errorf("%s: %d keys missing in total", l.Code, missing)
		}
		for k := range m {
			if _, ok := en[k]; !ok {
				t.Errorf("%s: stale key %s (not in English)", l.Code, k)
			}
		}
	}
}

func TestBundle(t *testing.T) {
	b, err := NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	if got := b.T("de", "common.missing.key.for.test"); got != "common.missing.key.for.test" {
		t.Fatalf("missing key should return key, got %q", got)
	}
}
