package syncer

import (
	"log/slog"

	"opensignage/internal/agent/state"
	"opensignage/internal/agent/wire"
)

// Prefs influence which variant of an asset is cached.
type Prefs struct {
	Perf        string // low | standard | high (already resolved from "auto")
	ScreenWidth int    // effective output width (after rotation); 0 = unknown
}

// Blob is one file to download.
type Blob struct {
	SHA  string
	Size int64
	URL  string
	MIME string
}

// Choice is the variant served for an asset plus fallbacks (other cached variants).
type Choice struct {
	Asset wire.ManifestAsset
	Alt   []wire.ManifestAsset
}

func preference(kind string, product bool, p Prefs) []string {
	switch kind {
	case wire.KindVideo:
		switch p.Perf {
		case "low":
			return []string{wire.VariantV720, wire.VariantV1080H, wire.VariantV1080, wire.VariantOrig}
		case "standard": // Raspberry Pi 4: 1080p50/60 drops frames, v1080h has 25/30 fps
			return []string{wire.VariantV1080H, wire.VariantV1080, wire.VariantOrig, wire.VariantV720}
		}
		return []string{wire.VariantV1080, wire.VariantOrig, wire.VariantV1080H, wire.VariantV720}
	case wire.KindImage:
		if product {
			return []string{wire.VariantI512, wire.VariantI1920, wire.VariantOrig}
		}
		if p.ScreenWidth > 1920 {
			return []string{wire.VariantOrig, wire.VariantI1920}
		}
		return []string{wire.VariantI1920, wire.VariantOrig}
	case wire.KindAudio:
		return []string{wire.VariantAudio, wire.VariantOrig}
	default:
		return []string{wire.VariantOrig}
	}
}

// Select picks exactly one variant per asset id (ARCHITECTURE §7.2): perf "low" plays
// v720, "standard" v1080h (full HD at half the frame rate, made only above 30 fps) or else
// v1080, "high" v1080 (fallback orig); images use i1920 on screens up to 1920 px wide,
// product images i512. Entries with an invalid SHA-256 are ignored.
func Select(m *wire.Manifest, p Prefs) map[string]Choice {
	product := map[string]bool{}
	for _, id := range m.Products.ImageAssetIDs {
		product[id] = true
	}
	groups := map[string][]wire.ManifestAsset{}
	var order []string
	for _, a := range m.Assets {
		if !state.ValidSHA(a.SHA256) || a.ID == "" {
			slog.Warn("manifest asset ignored (invalid id or sha256)", "asset", a.ID, "variant", a.Variant)
			continue
		}
		if _, ok := groups[a.ID]; !ok {
			order = append(order, a.ID)
		}
		groups[a.ID] = append(groups[a.ID], a)
	}
	out := make(map[string]Choice, len(groups))
	for _, id := range order {
		entries := groups[id]
		chosen := -1
		for _, want := range preference(entries[0].Kind, product[id], p) {
			for i, e := range entries {
				if e.Variant == want {
					chosen = i
					break
				}
			}
			if chosen >= 0 {
				break
			}
		}
		if chosen < 0 {
			chosen = 0 // unknown variant names: first listed entry
		}
		c := Choice{Asset: entries[chosen]}
		for i, e := range entries {
			if i != chosen {
				c.Alt = append(c.Alt, e)
			}
		}
		out[id] = c
	}
	return out
}

// Needed lists the blobs a manifest needs under the given preferences (chosen asset
// variants plus uploaded font files), deduplicated by SHA-256.
func Needed(m *wire.Manifest, p Prefs) []Blob {
	seen := map[string]bool{}
	var out []Blob
	add := func(b Blob) {
		if !state.ValidSHA(b.SHA) || seen[b.SHA] {
			return
		}
		seen[b.SHA] = true
		if b.URL == "" {
			b.URL = "/device/v1/blobs/" + b.SHA
		}
		out = append(out, b)
	}
	choices := Select(m, p)
	for _, a := range m.Assets { // manifest order keeps downloads deterministic
		c, ok := choices[a.ID]
		if !ok || c.Asset.SHA256 != a.SHA256 || c.Asset.Variant != a.Variant {
			continue
		}
		add(Blob{SHA: a.SHA256, Size: a.Size, URL: a.URL, MIME: a.MIME})
	}
	for _, f := range m.Fonts {
		for _, file := range f.Files {
			add(Blob{SHA: file.SHA256, Size: file.Size, URL: file.URL, MIME: fontMIME(file.Format)})
		}
	}
	return out
}

// referenced returns every SHA-256 a manifest mentions (all variants, all fonts). The
// cache GC keeps all of them so switching preferences never deletes a playing file.
func referenced(m *wire.Manifest) map[string]bool {
	out := map[string]bool{}
	for _, a := range m.Assets {
		if state.ValidSHA(a.SHA256) {
			out[a.SHA256] = true
		}
	}
	for _, f := range m.Fonts {
		for _, file := range f.Files {
			if state.ValidSHA(file.SHA256) {
				out[file.SHA256] = true
			}
		}
	}
	return out
}

func fontMIME(format string) string {
	switch format {
	case "woff2":
		return "font/woff2"
	case "woff":
		return "font/woff"
	case "truetype", "ttf":
		return "font/ttf"
	case "opentype", "otf":
		return "font/otf"
	}
	return "application/octet-stream"
}
