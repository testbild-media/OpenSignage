package contracts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestManifestCanonicalHash(t *testing.T) {
	m := Manifest{ID: "a", Hash: "h", BuiltAt: "t1", ZoneID: "z", MinAgentVersion: "1.0.0", Timezone: "UTC",
		Rules: []Rule{}, Playlists: []json.RawMessage{json.RawMessage(`{"id":"p","items":[]}`)},
		Presentations: []Presentation{}, Products: ManifestProducts{ImageAssetIDs: []string{}},
		Assets: []ManifestAsset{{ID: "x", Kind: "image", SHA256: "s1", Variant: "orig"}}, Fonts: []ManifestFont{}}
	h1, err := m.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	m.ID, m.Hash, m.BuiltAt = "b", "other", "t2"
	if h2, _ := m.CanonicalHash(); h2 != h1 {
		t.Fatal("id, hash and builtAt must not influence the hash")
	}
	b, _ := m.CanonicalJSON()
	for _, k := range []string{`"id":"b"`, `"hash"`, `"builtAt"`} {
		if strings.Contains(string(b), k) {
			t.Fatalf("canonical JSON contains %s: %s", k, b)
		}
	}
	m.Timezone = "Europe/Berlin"
	if h3, _ := m.CanonicalHash(); h3 == h1 {
		t.Fatal("content change must change the hash")
	}
	if got := m.BlobHashes(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("blobs: %v", got)
	}
}

func TestCanonicalizeJSON(t *testing.T) {
	a, err := CanonicalizeJSON([]byte(`{"b":1, "a":{"y":12345678901234567890,"x":[3,1]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != `{"a":{"x":[3,1],"y":12345678901234567890},"b":1}` {
		t.Fatalf("canonical: %s", a)
	}
	if _, err := CanonicalizeJSON([]byte(`{} {}`)); err == nil {
		t.Fatal("trailing data must fail")
	}
}

// TestLiveStateJSONNames pins the JSON names shared with web/packages/contracts.
func TestLiveStateJSONNames(t *testing.T) {
	d := DefaultTickerTemplate()
	if err := d.Validate(nil); err != nil {
		t.Fatal(err)
	}
	s := LiveState{Zone: LiveZone{ID: "z", State: "OPEN"}, Ticker: LiveTicker{Template: d},
		Categories: []LiveCategory{{ID: "c"}}, Products: []LiveProduct{{ID: "p", Status: "SOLD_OUT",
			Quantity: &Quantity{Value: "0.5", Unit: "l"}, ShowImageInsteadOfName: true}}}
	b, _ := json.Marshal(s)
	for _, k := range []string{`"zone"`, `"ticker"`, `"template"`, `"background"`, `"position":"bottom"`, `"categories"`,
		`"products"`, `"showImageInsteadOfName":true`, `"quantity":{"value":"0.5","unit":"l"}`, `"serverTime"`, `"status":"SOLD_OUT"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("missing %s in %s", k, b)
		}
	}
	bad := d
	bad.Direction = "up"
	if bad.Validate(nil) == nil {
		t.Fatal("invalid direction accepted")
	}
}
