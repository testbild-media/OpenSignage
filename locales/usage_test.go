package locales

// No missing translation keys: every translation key the code uses
// must exist in English (the source and fallback language; TestCompleteness then checks
// the other languages against English).
//
// Sources scanned:
//   - web/apps/*/src and web/packages/*/src (*.ts, *.tsx, tests excluded): every string
//     literal that has the shape of a key of a known namespace ('media.status.ready',
//     "admin.nav.media", `editor.tab.start`) – this covers t('…') as well as keys kept in
//     tables and passed around; template literals with ${…} are dynamic families.
//   - Go code under internal/ and cmd/: Bundle.T(lang, "ns.key"), the client WebGUI and
//     agent helpers that prefix "client." (s.t(lang, "key"), a.T("key"), flash(…, "key"),
//     fail("key"), {{t .L "key"}} in templates) and their dynamic forms
//     ("nav."+name, (printf "mode.%s" …)).
//   - Server error codes: the UI shows errors.<code> for every API error code, and
//     errors.field.<reason> / fields.<field> for validation errors (web/apps/admin/src/lib/
//     api.ts). Codes are collected from httpx.E, httpx.Invalid, contracts.FieldError,
//     the updates module's stored errors, and every helper that forwards its parameter
//     to one of them.
//
// Dynamic keys cannot be resolved by reading the code, so every family must be listed in
// dynamicFamilies below with the complete set of values its ${…} part can take.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const repoRoot = ".." // seen from locales/

// family describes one dynamic key family "prefix*suffix".
type family struct {
	values []string             // the complete value set
	from   []string             // "file#Name": string literals of a TS union type or const array
	fn     func(*scan) []string // values computed from the scanned code
	note   string               // where the values come from
}

// Values of the TS unions live in these files.
const (
	tsContracts = "web/packages/contracts/src/index.ts"
	tsMedia     = "web/apps/admin/src/features/media/model.ts"
	tsProducts  = "web/apps/admin/src/features/products/types.ts"
)

// dynamicFamilies documents every run-time built key. Families marked "has()" are guarded
// by a has() check with a fallback text in the UI; their known values must still exist.
var dynamicFamilies = map[string]family{
	// ---- admin web UI
	"admin.fit.*":                   {from: []string{tsContracts + "#Fit"}},
	"admin.products.status.*":       {from: []string{tsContracts + "#ProductStatus"}},
	"admin.products.type.*":         {from: []string{tsProducts + "#ProductType"}},
	"admin.products.unit.*":         {from: []string{tsProducts + "#QUANTITY_UNITS"}},
	"admin.role.*":                  {from: []string{tsContracts + "#Role"}},
	"admin.role.*.hint":             {from: []string{tsContracts + "#Role"}},
	"admin.scale.*":                 {from: []string{tsContracts + "#ScaleMode"}},
	"admin.schedules.midnight.*":    {from: []string{"web/apps/admin/src/features/schedules/windows.ts#MidnightInfo"}},
	"admin.schedules.scope.*":       {values: []string{"zone", "global", "default"}, note: "Rule['scope'], contracts.Scope*"},
	"admin.schedules.scopeHint.*":   {values: []string{"zone", "global", "default"}, note: "Rule['scope'], contracts.Scope*"},
	"admin.schedules.ticker.mode.*": {values: []string{"keep", "show", "hide"}, note: "TickerMode in RuleDialog"},
	"control.status.*":              {from: []string{tsContracts + "#ProductStatus"}},
	"devices.action.*": {values: []string{"sync_now", "reload_player", "restart_client", "reboot_device", "clean_cache", "screenshot", "plasma_regeneration"},
		note: "server/devices/actions.go actionNames"},
	"devices.actionError.*": {values: []string{"invalid_params", "not_supported", "player_not_running", "reboot_not_permitted", "send_failed", "unknown_action"},
		note: "has(); error codes of action.result (agent/handler.go runAction) and of server/devices/actions.go fail()"},
	"devices.alert.*": {values: []string{"device_offline", "player_crashed", "sync_failed", "version_mismatch", "disk_low", "disk_critical", "power_low", "video_fallback", "gpu_lost"},
		note: "has(); server/devices/monitor.go Alert* kinds"},
	"devices.clock.*": {values: []string{"server", "peer", "local"}, note: "agent/clocksync Source*"},
	"devices.hw.*":    {values: []string{"auto", "on", "off"}, note: "server/devices/store.go validHWVideo"},
	"updates.serverReason.*": {values: []string{"not_linux", "not_installed", "not_writable"},
		note: "server/updates/layout.go LayoutError codes"},
	"client.update.phase.*": {values: []string{"idle", "downloading", "verifying", "installing", "restarting", "health_check", "done", "failed", "rolled_back"},
		note: "agent/updater/updater.go Phase*"},
	"devices.perfLevel.*": {values: []string{"auto", "low", "standard", "high"}, note: "server/devices/store.go validPerf"},
	"devices.source.*":    {values: []string{"playlist", "closed", "cleared"}, note: "contracts.ActiveSource kinds"},
	"devices.syncState.*": {values: []string{"unpaired", "no_zone", "version_mismatch", "cleared", "active", "pending", "ready", "preparing", "failed"},
		note: "server/devices/admin.go sync state, deploy progress states"},
	"editor.align.*":      {from: []string{tsContracts + "#HAlign"}},
	"editor.bg.*":         {values: []string{"color", "gradient", "image", "video"}, note: "Background['type']"},
	"editor.dash.*":       {values: []string{"solid", "dashed", "dotted"}, note: "Stroke.dash"},
	"editor.date.*":       {values: []string{"none", "short", "long"}, note: "ClockElement.date"},
	"editor.direction.*":  {from: []string{tsContracts + "#Direction"}},
	"editor.el.*":         {values: []string{"text", "image", "video", "audio", "shape", "productList", "product", "clock", "ticker"}, note: "SlideElement['type'], contracts El*"},
	"editor.field.*":      {values: []string{"name", "quantity", "description", "price"}, note: "ProductFields"},
	"editor.shape.*":      {values: []string{"rect", "roundrect", "ellipse", "line", "arrow"}, note: "InsertKind shapes (Toolbar.tsx)"},
	"editor.source.*":     {values: []string{"all", "categories", "products"}, note: "ProductListElement source.kind"},
	"editor.transition.*": {from: []string{tsContracts + "#TransitionType"}},
	"editor.typo.*":       {values: []string{"heading", "subheading", "item", "description", "price"}, note: "Typography keys"},
	"editor.valign.*":     {from: []string{tsContracts + "#VAlign"}},
	"errors.*": {fn: func(s *scan) []string { return shown(s.codes, deviceOnlyCodes) },
		note: "every API error code of the server, client-side ApiError codes and the update error codes devices report"},
	"errors.field.*": {fn: func(s *scan) []string { return shown(s.reasons, deviceOnlyReasons) }, note: "validation reasons (httpx.Invalid, contracts.FieldError)"},
	"fields.*":       {fn: func(s *scan) []string { return shown(s.fields, deviceOnlyFields) }, note: "literal field names of validation errors"},
	"media.failure.*": {values: []string{"interrupted", "disk_full", "file_missing", "unsupported_type", "svg_unsafe", "probe_failed", "ffmpeg_failed", "unknown"},
		note: "has(); server/media/jobs.go jobError codes"},
	"media.fonts.cat.*":   {values: []string{"all"}, fn: fontCategories, note: "fonts/manifest.json categories plus the 'all' filter"},
	"media.kind.*":        {from: []string{tsMedia + "#MediaKind"}},
	"media.kind.plural.*": {values: []string{"all"}, from: []string{tsMedia + "#MediaKind"}},
	"media.status.*":      {from: []string{tsMedia + "#MediaStatus"}},
	"media.step.*": {values: []string{"probe", "orig", "i1920", "i512", "thumb", "v1080", "v1080h", "v720", "poster", "a"},
		note: "has(); server/media/ffmpeg.go variant specs and the probe step"},
	"media.storage.cat.*": {values: []string{"images", "videoOriginals", "videoVariants", "audio", "thumbnails", "fonts", "trash", "unreferenced", "database", "logs", "tmp"},
		note: "server/media/storage.go breakdown"},
	"media.usage.*":       {values: []string{"presentation", "playlist", "product", "other"}, note: "owner types (model.ts groupUsage)"},
	"media.usage.state.*": {values: []string{"draft", "published"}, note: "has(); owner type suffixes"},
	"media.variant.*": {values: []string{"orig", "i1920", "i512", "thumb", "v1080", "v1080h", "v720", "poster", "a"},
		note: "has(); server/media/ffmpeg.go variant specs"},
	"updates.duState.*": {values: []string{"waiting", "sent", "downloading", "installing", "installed", "done", "failed", "rolled_back", "offline", "skipped", "cancelled"},
		note: "server/updates/store.go device update states"},
	"updates.kind.*":      {values: []string{"server", "clients"}, note: "server/updates/store.go"},
	"updates.relSource.*": {values: []string{"github", "upload"}, note: "server/updates check.go, upload.go"},
	"updates.relState.*":  {values: []string{"available", "downloading", "ready", "failed"}, note: "server/updates/store.go"},
	"updates.runState.*":  {values: []string{"running", "done", "failed", "rolled_back", "stopped"}, note: "server/updates/store.go"},
	"updates.step.*": {values: []string{"metadata", "packages", "verifying", "receiving", "backup", "extract", "check", "switch", "restarting"},
		note: "server/updates download.go, upload.go, install.go, server.go"},

	// ---- client WebGUI and player notices (Go, internal/agent)
	"client.flash.*": {values: []string{"passwordChanged", "serverSaved", "repaired", "displaySaved", "playerRestarted", "rebooting"},
		note: "agent/webgui/pages.go flash keys"},
	"client.notice.*": {fn: goConsts("internal/agent/link", "Err"), note: "agent/link Err* codes"},
	"client.link.*":   {fn: goConsts("internal/agent/link", "Phase"), note: "agent/link Phase* states"},
	"client.nav.*": {values: []string{"login", "password", "status", "server", "pairing", "display", "actions", "logs", "update"},
		note: "agent/webgui page names (render/authedPage)"},
	"client.mode.*":       {fn: goConsts("internal/agent/wire", "Mode"), note: "agent/wire Mode* player modes"},
	"client.source.*":     {values: []string{"playlist", "closed", "cleared"}, note: "active source kinds"},
	"client.sync.*":       {values: []string{"idle", "cleared", "preparing", "active", "failed"}, note: "agent/syncer states"},
	"client.syncError.*":  {fn: goConsts("internal/agent/syncer", "Err"), note: "agent/syncer Err* codes"},
	"client.player.*":     {values: []string{"starting", "ok", "crashed", "restarting", "disabled"}, note: "agent/kiosk player states"},
	"client.display.rot*": {values: []string{"0", "90", "180", "270"}, note: "rotations"},
}

// extraErrorCodes are error codes that reach errors.<code> without an httpx.E call the
// scan can see.
var extraErrorCodes = []string{
	"update_not_supported", // reported by agents in update.progress (agent/handler.go)
	"update_failed",        // updates/rollout.go fallbacks for agent-reported failures
	"update_rolled_back",
}

// extraReasons are validation reasons produced from error values.
var extraReasons = []string{
	"password_too_short", "password_too_long", // common/passwords errors via httpx.Invalid("password", err.Error())
}

// unresolved lists the code/reason arguments the scan cannot evaluate, with the reason
// that is fine ("<file base name>: <expression>").
var unresolved = map[string]string{
	"users.go: err.Error()":       "password policy errors, see extraReasons",
	"system.go: err.Error()":      "password policy errors, see extraReasons",
	"rollout.go: firstNonEmpty()": "agent-reported update error codes, see extraErrorCodes",
	"errors.go: ae.Code":          "updates encodeErr stores the code of an httpx.Error (collected at its source)",
	"httpx.go: fe.Code":           "WriteError forwards contracts.FieldError codes (collected at their source)",
}

// Codes, reasons and fields that only the device channel (port 7443, used by agents)
// returns: no web interface ever shows them, so they need no text. The reason and the
// fields come from the validation of pairing requests.
var (
	deviceOnlyCodes = map[string]bool{"blob_not_found": true, "deployment_not_found": true, "invalid_image": true,
		"pairing_busy": true, "pairing_secret_invalid": true, "revoked": true, "version_mismatch": true}
	deviceOnlyReasons = map[string]bool{"too_large": true}
	deviceOnlyFields  = map[string]bool{"arch": true, "hardwareId": true, "hostname": true, "ips": true, "os": true, "screen": true}
)

// ------------------------------------------------------------------------------- scan

type scan struct {
	t              *testing.T
	en             map[string]string
	nss            map[string]bool // namespaces
	client         map[string]bool // first key segments of the client namespace
	need           map[string]string
	families       map[string]string
	usedUnresolved map[string]bool
	codes          map[string]string // error code → where
	reasons        map[string]string
	fields         map[string]string
}

func (s *scan) require(key, where string) {
	if _, ok := s.need[key]; !ok {
		s.need[key] = where
	}
}

func (s *scan) family(f, where string) {
	if _, ok := s.families[f]; !ok {
		s.families[f] = where
	}
}

// shown returns the sorted entries of a code set that the web interfaces can show.
func shown(m map[string]string, deviceOnly map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if !deviceOnly[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sortedSet(m map[string]string) []string { return shown(m, nil) }

func rel(p string) string {
	r, err := filepath.Rel(repoRoot, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

func TestUsedKeysExist(t *testing.T) {
	en, err := Load(Default)
	if err != nil {
		t.Fatal(err)
	}
	s := &scan{t: t, en: en, nss: map[string]bool{}, client: map[string]bool{}, need: map[string]string{},
		families: map[string]string{}, codes: map[string]string{}, reasons: map[string]string{}, fields: map[string]string{},
		usedUnresolved: map[string]bool{}}
	for k := range en {
		ns, rest, _ := strings.Cut(k, ".")
		s.nss[ns] = true
		if ns == "client" {
			first, _, _ := strings.Cut(rest, ".")
			s.client[first] = true
		}
	}
	s.scanWeb()
	s.scanGo()
	for _, c := range extraErrorCodes {
		s.codes[c] = "extraErrorCodes"
	}
	for _, r := range extraReasons {
		s.reasons[r] = "extraReasons"
	}

	var missing []string
	for k, where := range s.need {
		if _, ok := en[k]; !ok {
			missing = append(missing, k+"  ("+where+")")
		}
	}
	for f, where := range s.families {
		spec, ok := dynamicFamilies[f]
		if !ok {
			t.Errorf("dynamic key family %q (%s) is not documented: add it to dynamicFamilies with all its values", f, where)
			continue
		}
		pre, suf, _ := strings.Cut(f, "*")
		vals := append([]string(nil), spec.values...)
		for _, src := range spec.from {
			v := tsLiterals(t, src)
			if len(v) == 0 {
				t.Errorf("family %q: no values found in %s", f, src)
			}
			vals = append(vals, v...)
		}
		if spec.fn != nil {
			vals = append(vals, spec.fn(s)...)
		}
		if len(vals) == 0 {
			t.Errorf("family %q has no values", f)
		}
		for _, v := range vals {
			if _, ok := en[pre+v+suf]; !ok {
				missing = append(missing, pre+v+suf+"  (family "+f+", "+where+")")
			}
		}
	}
	for f := range dynamicFamilies {
		if _, ok := s.families[f]; !ok {
			t.Errorf("dynamicFamilies lists %q, which the code no longer uses", f)
		}
	}
	for e := range unresolved {
		if !s.usedUnresolved[e] {
			t.Errorf("unresolved lists %q, which the code no longer contains", e)
		}
	}
	for _, l := range []struct {
		name string
		list map[string]bool
		seen map[string]string
	}{{"deviceOnlyCodes", deviceOnlyCodes, s.codes}, {"deviceOnlyReasons", deviceOnlyReasons, s.reasons}, {"deviceOnlyFields", deviceOnlyFields, s.fields}} {
		for c := range l.list {
			if _, ok := l.seen[c]; !ok {
				t.Errorf("%s lists %q, which the server no longer returns", l.name, c)
			}
		}
	}
	// Guard against a scan that silently finds nothing.
	clientKeys := 0
	for k := range s.need {
		if strings.HasPrefix(k, "client.") {
			clientKeys++
		}
	}
	if len(s.need) < 500 || clientKeys < 50 || len(s.codes) < 50 || len(s.reasons) < 10 || len(s.fields) < 20 {
		t.Errorf("scan found too little: %d keys (%d client), %d codes, %d reasons, %d fields", len(s.need), clientKeys, len(s.codes), len(s.reasons), len(s.fields))
	}
	t.Logf("checked %d literal keys (%d client), %d dynamic families, %d error codes, %d reasons, %d fields",
		len(s.need), clientKeys, len(s.families), len(s.codes), len(s.reasons), len(s.fields))
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d translation keys are used but missing in locales/en:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}

// ------------------------------------------------------------------------------- web

var (
	tsKeyRe = regexp.MustCompile("(['\"`])([A-Za-z]+(?:\\.[A-Za-z0-9_-]+)+)['\"`]")
	tsFamRe = regexp.MustCompile("`([A-Za-z]+\\.[A-Za-z0-9_.]*)\\$\\{[^}`]*\\}([A-Za-z0-9_.]*)`")
	tsCatRe = regexp.MustCompile(`(['"])([A-Za-z]+\.[A-Za-z0-9_.]*\.)['"]\s*\+`)
	tsErrRe = regexp.MustCompile(`new ApiError\([^,()]+,\s*'([a-z0-9_]+)'`)
)

func (s *scan) scanWeb() {
	var files []string
	for _, base := range []string{"web/apps", "web/packages"} {
		root := filepath.Join(repoRoot, base)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "node_modules", "dist", "__tests__", "test", "tests":
					return filepath.SkipDir
				}
				return nil
			}
			r := rel(p)
			if !strings.Contains(r, "/src/") || !(strings.HasSuffix(r, ".ts") || strings.HasSuffix(r, ".tsx")) ||
				strings.HasSuffix(r, ".d.ts") || strings.Contains(r, ".test.") || strings.Contains(r, ".spec.") {
				return nil
			}
			files = append(files, p)
			return nil
		})
		if err != nil {
			s.t.Fatal(err)
		}
	}
	if len(files) < 10 {
		s.t.Fatalf("only %d web sources found: is the walk broken?", len(files))
	}
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			s.t.Fatal(err)
		}
		src, r := string(b), rel(p)
		for _, m := range tsKeyRe.FindAllStringSubmatch(src, -1) {
			if ns, _, _ := strings.Cut(m[2], "."); s.nss[ns] {
				s.require(m[2], r)
			}
		}
		for _, m := range tsFamRe.FindAllStringSubmatch(src, -1) {
			if ns, _, _ := strings.Cut(m[1], "."); s.nss[ns] {
				s.family(m[1]+"*"+m[2], r)
			}
		}
		for _, m := range tsCatRe.FindAllStringSubmatch(src, -1) {
			if ns, _, _ := strings.Cut(m[2], "."); s.nss[ns] {
				s.family(m[2]+"*", r)
			}
		}
		for _, m := range tsErrRe.FindAllStringSubmatch(src, -1) {
			s.codes[m[1]] = r
		}
	}
}

// tsLiterals returns the quoted string literals of `type Name = …;` or `const Name = […]`.
func tsLiterals(t *testing.T, spec string) []string {
	file, name, _ := strings.Cut(spec, "#")
	b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(file)))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)\b(?:type|const)\s+` + regexp.QuoteMeta(name) + `\b[^=]*=\s*(.*?)(?:;|\n\n)`).FindStringSubmatch(string(b))
	if m == nil {
		return nil
	}
	var out []string
	for _, v := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, v[1])
	}
	return out
}

func fontCategories(s *scan) []string {
	b, err := os.ReadFile(filepath.Join(repoRoot, "fonts", "manifest.json"))
	if err != nil {
		s.t.Fatal(err)
	}
	set := map[string]string{}
	for _, m := range regexp.MustCompile(`"category"\s*:\s*"([^"]+)"`).FindAllStringSubmatch(string(b), -1) {
		set[m[1]] = ""
	}
	return sortedSet(set)
}

// ------------------------------------------------------------------------------- Go

type goFile struct {
	path string // repo relative
	dir  string
	f    *ast.File
}

func (s *scan) parseGo() []goFile {
	fset := token.NewFileSet()
	var out []goFile
	for _, base := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, base), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.Contains(rel(p), "/devtest/") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			out = append(out, goFile{path: rel(p), dir: path.Dir(rel(p)), f: f})
			return nil
		})
		if err != nil {
			s.t.Fatal(err)
		}
	}
	return out
}

func unquote(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(bl.Value)
	return v, err == nil
}

var (
	goKeyRe   = regexp.MustCompile(`^[a-z][A-Za-z0-9]*(\.[A-Za-z0-9_]+)+$`)
	tmplKeyRe = regexp.MustCompile(`[({]\s*t\s+(?:\$?\.[A-Za-z0-9_.]*|\$[A-Za-z0-9_]+)\s+"([^"]+)"`)
	tmplFamRe = regexp.MustCompile(`[({]\s*t\s+(?:\$?\.[A-Za-z0-9_.]*|\$[A-Za-z0-9_]+)\s+\(printf\s+"([^"%]*)%[a-z]([^"]*)"`)
	fileExt   = map[string]bool{"json": true, "jpg": true, "png": true, "txt": true, "log": true, "env": true, "crt": true,
		"key": true, "pem": true, "html": true, "js": true, "css": true, "db": true, "tar": true, "gz": true, "sock": true}
)

func (s *scan) scanGo() {
	files := s.parseGo()
	if len(files) < 20 {
		s.t.Fatalf("only %d Go files found: is the walk broken?", len(files))
	}
	for _, gf := range files {
		agent := strings.HasPrefix(gf.path, "internal/agent/") || strings.HasPrefix(gf.path, "cmd/opensignage-agent/")
		ast.Inspect(gf.f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if !agent || x.Kind != token.STRING {
					return true
				}
				v, _ := strconv.Unquote(x.Value)
				if !strings.Contains(v, "{{") {
					return true
				}
				for _, m := range tmplKeyRe.FindAllStringSubmatch(v, -1) {
					s.require("client."+m[1], gf.path)
				}
				for _, m := range tmplFamRe.FindAllStringSubmatch(v, -1) {
					s.family("client."+m[1]+"*"+m[2], gf.path)
				}
			case *ast.CallExpr:
				sel, isT := x.Fun.(*ast.SelectorExpr)
				isT = isT && sel.Sel.Name == "T" // locales.Bundle.T(lang, "ns.key", …)
				for i, a := range x.Args {
					s.goKeyArg(a, agent, isT && i == 1, gf.path)
				}
			}
			return true
		})
	}
	s.scanErrorCodes(files)
}

// goKeyArg handles one call argument: a full key passed to Bundle.T ("ns.key"), a
// client-relative key in agent code ("nav.status", which the agent helpers prefix with
// "client."), or a dynamic family ("client.notice."+x, "nav."+name).
func (s *scan) goKeyArg(a ast.Expr, agent, fullKey bool, where string) {
	if !agent && !fullKey {
		return // server code has no translated texts (audit actions look like keys)
	}
	if be, ok := a.(*ast.BinaryExpr); ok && be.Op == token.ADD {
		lit, ok := unquote(be.X)
		if !ok || !strings.HasSuffix(lit, ".") {
			return
		}
		ns, rest, _ := strings.Cut(lit, ".")
		switch {
		case s.nss[ns] && rest != "" && (fullKey || ns == "client"):
			s.family(lit+"*", where)
		case agent && !fullKey && s.client[ns]:
			s.family("client."+lit+"*", where)
		}
		return
	}
	lit, ok := unquote(a)
	if !ok || !goKeyRe.MatchString(lit) {
		return
	}
	first, _, _ := strings.Cut(lit, ".")
	last := lit[strings.LastIndexByte(lit, '.')+1:]
	switch {
	case fullKey && s.nss[first], first == "client":
		s.require(lit, where)
	case agent && s.client[first] && !fileExt[last]:
		s.require("client."+lit, where)
	}
}

// ---- error codes

type sinkArg struct {
	kind string // code | reason | field
	idx  int
}

// scanErrorCodes collects the codes of all API errors: httpx.E(status, code, msg),
// httpx.Invalid(field, reason), FieldError{Field, Code}, storedError{Code} and every
// function that forwards one of its parameters into such a position.
func (s *scan) scanErrorCodes(files []goFile) {
	consts := map[string]map[string]string{} // dir → const name → value
	byPkg := map[string]map[string]string{}  // package base name → exported consts
	for _, gf := range files {
		for _, d := range gf.f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i < len(vs.Values) {
						if v, ok := unquote(vs.Values[i]); ok {
							if consts[gf.dir] == nil {
								consts[gf.dir] = map[string]string{}
								byPkg[path.Base(gf.dir)] = map[string]string{}
							}
							consts[gf.dir][n.Name] = v
							byPkg[path.Base(gf.dir)][n.Name] = v
						}
					}
				}
			}
		}
	}
	eval := func(gf goFile, e ast.Expr) (string, bool) {
		switch x := e.(type) {
		case *ast.BasicLit:
			return unquote(x)
		case *ast.Ident:
			v, ok := consts[gf.dir][x.Name]
			return v, ok
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok {
				v, ok := byPkg[id.Name][x.Sel.Name]
				return v, ok
			}
		}
		return "", false
	}

	sinks := map[string][]sinkArg{"Invalid": {{"field", 0}, {"reason", 1}}}
	composites := map[string]map[string]string{"FieldError": {"Field": "field", "Code": "reason"}, "storedError": {"Code": "code"}}
	callName := func(gf goFile, c *ast.CallExpr) string {
		switch f := c.Fun.(type) {
		case *ast.Ident:
			if f.Name == "E" && path.Base(gf.dir) != "httpx" {
				return ""
			}
			return f.Name
		case *ast.SelectorExpr:
			if f.Sel.Name == "E" {
				if id, ok := f.X.(*ast.Ident); !ok || id.Name != "httpx" {
					return ""
				}
			}
			return f.Sel.Name
		}
		return ""
	}
	typeName := func(e ast.Expr) string {
		switch x := e.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.SelectorExpr:
			return x.Sel.Name
		}
		return ""
	}
	sinks["E"] = []sinkArg{{"code", 1}}

	// visit calls fn for every argument in a sink position.
	visit := func(gf goFile, body ast.Node, fn func(kind string, arg ast.Expr)) {
		ast.Inspect(body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				for _, sa := range sinks[callName(gf, x)] {
					if sa.idx < len(x.Args) {
						fn(sa.kind, x.Args[sa.idx])
					}
				}
			case *ast.CompositeLit:
				if fields := composites[typeName(x.Type)]; fields != nil {
					for _, el := range x.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							if k, ok := kv.Key.(*ast.Ident); ok && fields[k.Name] != "" {
								fn(fields[k.Name], kv.Value)
							}
						}
					}
				}
			}
			return true
		})
	}
	// Wrappers: functions passing a parameter on into a sink position (to a fixpoint).
	for changed := true; changed; {
		changed = false
		for _, gf := range files {
			for _, d := range gf.f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil || fd.Name.Name == "E" || fd.Name.Name == "Invalid" {
					continue
				}
				params := map[string]int{}
				i := 0
				for _, fl := range fd.Type.Params.List {
					for _, n := range fl.Names {
						params[n.Name] = i
						i++
					}
					if len(fl.Names) == 0 {
						i++
					}
				}
				visit(gf, fd.Body, func(kind string, arg ast.Expr) {
					id, ok := arg.(*ast.Ident)
					if !ok {
						return
					}
					idx, ok := params[id.Name]
					if !ok {
						return
					}
					for _, sa := range sinks[fd.Name.Name] {
						if sa == (sinkArg{kind, idx}) {
							return
						}
					}
					sinks[fd.Name.Name] = append(sinks[fd.Name.Name], sinkArg{kind, idx})
					changed = true
				})
			}
		}
	}
	for _, gf := range files {
		visit(gf, gf.f, func(kind string, arg ast.Expr) {
			v, ok := eval(gf, arg)
			if !ok {
				if kind == "field" {
					return // computed paths (items[3].refId) fall back to the raw name in the UI
				}
				expr := path.Base(gf.path) + ": " + exprString(arg)
				s.usedUnresolved[expr] = true
				if _, listed := unresolved[expr]; !listed && !isParam(gf, arg) {
					s.t.Errorf("%s: cannot resolve error %s %q – make it a literal or constant, or document it in unresolved", gf.path, kind, expr)
				}
				return
			}
			switch kind {
			case "code":
				s.codes[v] = gf.path
			case "reason":
				s.reasons[v] = gf.path
			case "field":
				s.fields[v] = gf.path
			}
		})
	}
}

// isParam reports whether arg is a parameter of the function it appears in (wrappers are
// resolved at their call sites).
func isParam(gf goFile, arg ast.Expr) bool {
	id, ok := arg.(*ast.Ident)
	if !ok {
		return false
	}
	found := false
	for _, d := range gf.f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil || arg.Pos() < fd.Body.Pos() || arg.Pos() > fd.Body.End() {
			continue
		}
		for _, fl := range fd.Type.Params.List {
			for _, n := range fl.Names {
				if n.Name == id.Name {
					found = true
				}
			}
		}
	}
	return found
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		return exprString(x.Fun) + "()"
	case *ast.BasicLit:
		return x.Value
	case *ast.BinaryExpr:
		return exprString(x.X) + " " + x.Op.String() + " " + exprString(x.Y)
	}
	return "?"
}

// goConsts returns the string constants of a package whose names start with prefix.
func goConsts(dir, prefix string) func(*scan) []string {
	return func(s *scan) []string {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, filepath.Join(repoRoot, filepath.FromSlash(dir)), func(fi fs.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		if err != nil {
			s.t.Fatal(err)
		}
		set := map[string]string{}
		for _, p := range pkgs {
			for _, f := range p.Files {
				for _, d := range f.Decls {
					gd, ok := d.(*ast.GenDecl)
					if !ok || gd.Tok != token.CONST {
						continue
					}
					for _, sp := range gd.Specs {
						vs := sp.(*ast.ValueSpec)
						for i, n := range vs.Names {
							if strings.HasPrefix(n.Name, prefix) && i < len(vs.Values) {
								if v, ok := unquote(vs.Values[i]); ok {
									set[v] = ""
								}
							}
						}
					}
				}
			}
		}
		if len(set) == 0 {
			s.t.Errorf("no %s* constants in %s", prefix, dir)
		}
		return sortedSet(set)
	}
}
