// Package licenses holds no Go code; its test checks that every bundled font
// family, graphic and icon set (and every shipped dependency) has a documented, free
// license in fonts/LICENSES.md or THIRD_PARTY_LICENSES.md, and every referenced license
// file exists. (It lives here, not in fonts/, because fonts/ is shipped as it is.)
package licenses

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const root = "../.." // repository root, seen from this directory

type manifest struct {
	Families []struct {
		ID          string `json:"id"`
		Family      string `json:"family"`
		License     string `json:"license"`
		LicenseFile string `json:"licenseFile"`
		Files       []struct {
			File     string `json:"file"`
			Modified bool   `json:"modified"`
		} `json:"files"`
	} `json:"families"`
}

// licenseText is what the shipped license file of each allowed license must contain.
var licenseText = map[string][]string{
	"OFL-1.1":    {"SIL OPEN FONT LICENSE", "Version 1.1"},
	"Apache-2.0": {"Apache License", "Version 2.0"},
}

// licenseLabel is how fonts/LICENSES.md names each license.
var licenseLabel = map[string]string{"OFL-1.1": "SIL OFL 1.1", "Apache-2.0": "Apache 2.0"}

var fontExt = map[string]bool{".woff2": true, ".woff": true, ".ttf": true, ".otf": true}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// tableRows returns the cells of all Markdown table rows (header and separator excluded).
func tableRows(doc string) [][]string {
	var out [][]string
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| ---") {
			continue
		}
		line = strings.ReplaceAll(line, `\|`, "\x00")
		var cells []string
		for _, c := range strings.Split(strings.Trim(line, "|"), "|") {
			cells = append(cells, strings.TrimSpace(strings.ReplaceAll(c, "\x00", "|")))
		}
		out = append(out, cells)
	}
	return out
}

var mdLink = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)

func TestFontLicenses(t *testing.T) {
	var man manifest
	if err := json.Unmarshal([]byte(read(t, "fonts/manifest.json")), &man); err != nil {
		t.Fatal(err)
	}
	doc := read(t, "fonts/LICENSES.md")

	// Rows of the family table by license-file directory.
	type row struct{ family, label, file, modified string }
	rows := map[string]row{}
	for _, cells := range tableRows(doc) {
		if len(cells) < 6 || cells[0] == "Family" {
			continue
		}
		m := mdLink.FindStringSubmatch(cells[2])
		if m == nil {
			t.Errorf("fonts/LICENSES.md: %s has no license file link", cells[0])
			continue
		}
		dir := path.Dir(m[2])
		if _, dup := rows[dir]; dup {
			t.Errorf("fonts/LICENSES.md: %s listed twice", dir)
		}
		rows[dir] = row{family: cells[0], label: m[1], file: m[2], modified: cells[3]}
		if _, err := os.Stat(filepath.Join(root, "fonts", filepath.FromSlash(m[2]))); err != nil {
			t.Errorf("fonts/LICENSES.md: license file of %s missing: %v", cells[0], err)
		}
		if cells[5] == "" || !strings.HasPrefix(cells[4], "[") {
			t.Errorf("fonts/LICENSES.md: %s needs upstream source and copyright", cells[0])
		}
	}

	families := map[string]bool{}
	for _, f := range man.Families {
		families[f.ID] = true
		want, ok := licenseText[f.License]
		if !ok {
			t.Errorf("%s: license %q is not an allowed free font license", f.ID, f.License)
			continue
		}
		if path.Dir(f.LicenseFile) != f.ID {
			t.Errorf("%s: license file %s outside its folder", f.ID, f.LicenseFile)
		}
		text := read(t, "fonts/"+f.LicenseFile)
		for _, w := range want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: %s does not contain %q", f.ID, f.LicenseFile, w)
			}
		}
		r, ok := rows[f.ID]
		switch {
		case !ok:
			t.Errorf("%s (%s) is missing in fonts/LICENSES.md", f.ID, f.Family)
		case r.family != f.Family || r.label != licenseLabel[f.License] || r.file != f.LicenseFile:
			t.Errorf("fonts/LICENSES.md row for %s does not match the manifest: %+v", f.ID, r)
		}
		anyModified := slices.ContainsFunc(f.Files, func(x struct {
			File     string `json:"file"`
			Modified bool   `json:"modified"`
		}) bool {
			return x.Modified
		})
		if want := map[bool]string{true: "yes", false: "no"}[anyModified]; ok && r.modified != want {
			t.Errorf("%s: Modified column %q, files say %q", f.ID, r.modified, want)
		}
		listed := map[string]bool{f.LicenseFile: true}
		for _, x := range f.Files {
			listed[x.File] = true
			if _, err := os.Stat(filepath.Join(root, "fonts", filepath.FromSlash(x.File))); err != nil {
				t.Errorf("%s: font file %s missing", f.ID, x.File)
			}
		}
		// Nothing unlisted ships in the family folder.
		entries, err := os.ReadDir(filepath.Join(root, "fonts", f.ID))
		if err != nil {
			t.Errorf("%s: %v", f.ID, err)
			continue
		}
		fonts := 0
		for _, e := range entries {
			name := f.ID + "/" + e.Name()
			if fontExt[strings.ToLower(path.Ext(e.Name()))] {
				fonts++
			}
			if !listed[name] {
				t.Errorf("%s is not covered by the font manifest and its license", name)
			}
		}
		if fonts == 0 {
			t.Errorf("%s: no font files", f.ID)
		}
	}
	for dir := range rows {
		if !families[dir] {
			t.Errorf("fonts/LICENSES.md lists %s, which is not bundled", dir)
		}
	}
	// Every family folder is a manifest family (so it has a documented license).
	entries, err := os.ReadDir(filepath.Join(root, "fonts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() && !families[e.Name()] {
			t.Errorf("fonts/%s is not in fonts/manifest.json and fonts/LICENSES.md", e.Name())
		}
	}

	// Families with a Reserved Font Name must ship unmodified (OFL condition 3).
	if i := strings.Index(doc, "## Reserved Font Names"); i >= 0 {
		sect := doc[i:]
		if j := strings.Index(sect[3:], "\n## "); j >= 0 {
			sect = sect[:j+3]
		}
		for _, r := range rows {
			if strings.Contains(sect, r.family+",") && r.modified != "no" {
				t.Errorf("%s has a Reserved Font Name but is shipped modified", r.family)
			}
		}
	}
}

// TestThirdPartyLicenses: graphics, icon sets and shipped dependencies are listed with a
// free license, and the links in THIRD_PARTY_LICENSES.md point to existing files.
func TestThirdPartyLicenses(t *testing.T) {
	doc := read(t, "THIRD_PARTY_LICENSES.md")
	for _, m := range mdLink.FindAllStringSubmatch(doc, -1) {
		if strings.Contains(m[2], "://") || strings.HasPrefix(m[2], "#") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(m[2]))); err != nil {
			t.Errorf("THIRD_PARTY_LICENSES.md links to missing %s", m[2])
		}
	}
	if !strings.Contains(doc, "(fonts/LICENSES.md)") {
		t.Error("THIRD_PARTY_LICENSES.md must refer to fonts/LICENSES.md")
	}

	// Graphics shipped with the product: every image file outside fonts, docs and test
	// data must lie under a path named in THIRD_PARTY_LICENSES.md (in backticks).
	var named []string
	for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(doc, -1) {
		named = append(named, m[1])
	}
	covered := func(p string) bool {
		for _, n := range named {
			if n == p || strings.HasSuffix(n, "/") && strings.HasPrefix(p, n) {
				return true
			}
		}
		return false
	}
	images := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".ico": true, ".webp": true, ".avif": true, ".bmp": true}
	skip := map[string]bool{"node_modules": true, "dist": true, "build": true, "coverage": true, "testdata": true, "docs": true, "_kit": true, "_intern": true, "fonts": true}
	found := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || skip[d.Name()] || strings.HasSuffix(rel, "/web/static") || strings.HasSuffix(rel, "/player/static")) {
				return filepath.SkipDir // hidden, build output, third-party trees, non-shipped files
			}
			return nil
		}
		if !images[strings.ToLower(path.Ext(rel))] || strings.Contains(rel, "_test") {
			return nil
		}
		found++
		if !covered(rel) {
			t.Errorf("graphic %s has no license entry in THIRD_PARTY_LICENSES.md", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Error("no graphics found: is the walk broken?")
	}

	// Shipped dependencies: every runtime npm dependency (icon sets included) and every Go
	// module is named with its license.
	free := regexp.MustCompile(`^(MIT|ISC|BSD-2-Clause|BSD-3-Clause|Apache-2\.0|0BSD|OFL-1\.1)( / (MIT|ISC|BSD-2-Clause|BSD-3-Clause|Apache-2\.0))?`)
	licenseOf := map[string]string{} // package or module → license cell
	for _, cells := range tableRows(doc) {
		if len(cells) != 2 || cells[0] == "Package" || cells[0] == "Module" {
			continue
		}
		if !free.MatchString(cells[1]) {
			t.Errorf("THIRD_PARTY_LICENSES.md: %q has no recognised free license (%q)", cells[0], cells[1])
		}
		for _, name := range regexp.MustCompile(`[A-Za-z0-9@][A-Za-z0-9@._/-]*`).FindAllString(cells[0], -1) {
			licenseOf[name] = cells[1]
		}
	}
	var pkgs []string
	pj, _ := filepath.Glob(filepath.Join(root, "web", "package.json"))
	// Only the workspaces (never web/node_modules/*, which holds the dev tooling).
	apps, _ := filepath.Glob(filepath.Join(root, "web", "apps", "*", "package.json"))
	libs, _ := filepath.Glob(filepath.Join(root, "web", "packages", "*", "package.json"))
	for _, f := range append(append(pj, apps...), libs...) {
		var p struct {
			Dependencies map[string]string `json:"dependencies"`
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for name, ver := range p.Dependencies {
			if !strings.HasPrefix(name, "@opensignage/") && !strings.HasPrefix(ver, "file:") && !strings.HasPrefix(ver, "workspace:") {
				pkgs = append(pkgs, name)
			}
		}
	}
	if len(pkgs) == 0 {
		t.Error("no npm dependencies found: is web/package.json readable?")
	}
	for _, name := range pkgs {
		if _, ok := licenseOf[name]; !ok {
			t.Errorf("npm dependency %s is not listed in THIRD_PARTY_LICENSES.md", name)
		}
	}
	// Indirect runtime packages (e.g. dependencies of ProseMirror) end up in the bundles too:
	// every non-dev package of the lock file has a free license and is listed, or the file
	// says why it is not shipped (removed by tree shaking).
	var lock struct {
		Packages map[string]struct {
			License     string `json:"license"`
			Dev         bool   `json:"dev"`
			DevOptional bool   `json:"devOptional"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(read(t, "web/package-lock.json")), &lock); err != nil {
		t.Fatalf("web/package-lock.json: %v", err)
	}
	runtime := 0
	for key, p := range lock.Packages {
		name, ok := strings.CutPrefix(key, "node_modules/")
		if !ok || p.Dev || p.DevOptional || strings.HasPrefix(name, "@opensignage/") || strings.Contains(name, "/node_modules/") {
			continue
		}
		runtime++
		if !free.MatchString(p.License) {
			t.Errorf("runtime npm package %s has no recognised free license (%q)", name, p.License)
		}
		if _, ok := licenseOf[name]; ok {
			continue
		}
		if !regexp.MustCompile(`(^|[^A-Za-z0-9@/-])` + regexp.QuoteMeta(name) + `([^A-Za-z0-9/-]|$)[^\n]*(\n[^\n]+)*not\s+shipped`).MatchString(doc) {
			t.Errorf("runtime npm package %s (indirect) is not listed in THIRD_PARTY_LICENSES.md", name)
		}
	}
	if runtime == 0 {
		t.Error("no runtime packages in web/package-lock.json: is the lock file readable?")
	}
	for _, line := range strings.Split(read(t, "go.mod"), "\n") {
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require "))
		if len(f) < 2 || !strings.Contains(f[0], ".") || !strings.HasPrefix(f[1], "v") {
			continue
		}
		if _, ok := licenseOf[f[0]]; !ok {
			t.Errorf("Go module %s is not listed in THIRD_PARTY_LICENSES.md", f[0])
		}
	}
}
