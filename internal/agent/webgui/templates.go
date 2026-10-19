package webgui

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"opensignage/locales"
)

func parseTemplates(bundle *locales.Bundle) (*template.Template, error) {
	funcs := template.FuncMap{
		"t": func(lang, key string, params ...any) string { return bundle.T(lang, "client."+key, params...) },
		"bytes": func(v any) string {
			var n float64
			switch x := v.(type) {
			case int64:
				n = float64(x)
			case uint64:
				n = float64(x)
			case int:
				n = float64(x)
			}
			units := []string{"B", "KB", "MB", "GB", "TB"}
			i := 0
			for n >= 1000 && i < len(units)-1 {
				n /= 1000
				i++
			}
			if i == 0 {
				return fmt.Sprintf("%.0f %s", n, units[i])
			}
			return fmt.Sprintf("%.1f %s", n, units[i])
		},
		"pct": func(part, total uint64) string {
			if total == 0 {
				return "–"
			}
			return fmt.Sprintf("%.0f %%", float64(part)/float64(total)*100)
		},
		"dur": func(d time.Duration) string {
			d = d.Round(time.Minute)
			days := d / (24 * time.Hour)
			d -= days * 24 * time.Hour
			h := d / time.Hour
			m := (d - h*time.Hour) / time.Minute
			if days > 0 {
				return fmt.Sprintf("%dd %dh %dm", days, h, m)
			}
			return fmt.Sprintf("%dh %dm", h, m)
		},
		"secs": func(d time.Duration) string { return fmt.Sprintf("%+.1f s", d.Seconds()) },
		"temp": func(t *float64) string {
			if t == nil {
				return "–"
			}
			return fmt.Sprintf("%.1f °C", *t)
		},
		"float": func(f float64) string { return fmt.Sprintf("%.1f", f) },
		"rate":  func(f float64) string { return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0") }, // 780, 866.7
		"hz":    func(f float64) string { return fmt.Sprintf("%.2f", f) },
		"join":  func(l []string) string { return strings.Join(l, ", ") },
		"quality": func(q string) string {
			switch q {
			case "excellent", "good":
				return "ok"
			case "fair":
				return "warn"
			}
			return "err"
		},
		"netctx": func(p *page, i netIface) any {
			return struct {
				P *page
				I netIface
			}{p, i}
		},
		"linkctx": func(l string, s Status) any {
			return struct {
				L    string
				Data Status
			}{l, s}
		},
	}
	t, err := template.New("webgui").Funcs(funcs).Parse(templates)
	if err != nil {
		return nil, err
	}
	return t.Parse(networkTemplates)
}

func serveCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(css))
}

const css = `:root{--bg:#201c18;--surface:#2a251f;--raised:#332d26;--border:#453e35;--border-strong:#5a5147;--text:#f2ede6;--muted:#a99e90;--faint:#746b60;--primary:#ff8a00;--primary-hover:#ffa733;--primary-active:#e67a00;--brand-dark:#a63d00;--ok:#2f9e5c;--warn:#d99a1f;--err:#d64545}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--text);font:15px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
a{color:var(--primary)}a:hover{color:var(--primary-hover)}
header{display:flex;flex-wrap:wrap;align-items:center;gap:12px 24px;padding:12px 24px;background:var(--surface);border-bottom:1px solid var(--border)}
.brand{font-weight:700;font-size:20px;text-decoration:none;white-space:nowrap}.brand .o{color:var(--brand-dark)}.brand .s{color:var(--primary)}.brand small{color:var(--muted);font-weight:400;font-size:13px;margin-left:8px}
nav{display:flex;flex-wrap:wrap;gap:4px;flex:1}
nav a{color:var(--muted);text-decoration:none;padding:6px 10px;border-radius:6px}nav a:hover{color:var(--text);background:var(--raised)}nav a.active{color:var(--text);background:var(--raised);box-shadow:inset 0 -2px 0 var(--primary)}
.tools{display:flex;gap:8px;align-items:center}
main{max-width:980px;margin:0 auto;padding:24px}
h1{font-size:24px;margin:0 0 16px}h2{font-size:17px;margin:0 0 10px}
.card{background:var(--surface);border:1px solid var(--border);border-radius:10px;padding:18px 20px;margin-bottom:16px}
dl.grid{display:grid;grid-template-columns:minmax(150px,240px) 1fr;gap:6px 16px;margin:0}dl.grid dt{color:var(--muted)}dl.grid dd{margin:0;word-break:break-word}
.badge{display:inline-block;padding:1px 8px;border-radius:999px;font-size:13px;background:var(--raised);border:1px solid var(--border-strong)}
.badge.ok{border-color:var(--ok);color:#7ad6a0}.badge.warn{border-color:var(--warn);color:#f0c46a}.badge.err{border-color:var(--err);color:#f08a8a}
.flash{padding:10px 14px;border-radius:6px;margin-bottom:16px;border:1px solid var(--ok);background:rgba(47,158,92,.12)}
.error{padding:10px 14px;border-radius:6px;margin-bottom:16px;border:1px solid var(--err);background:rgba(214,69,69,.12)}
.note{color:var(--muted);font-size:14px}
label{display:block;margin:10px 0 4px;color:var(--muted)}
input[type=text],input[type=password],input[type=number],select{width:100%;max-width:420px;padding:8px 10px;border-radius:6px;border:1px solid var(--border-strong);background:var(--bg);color:var(--text);font:inherit}
input:focus,select:focus,button:focus{outline:2px solid rgba(255,138,0,.5);outline-offset:1px}
.inline{display:flex;gap:8px;align-items:center;flex-wrap:wrap}.inline label{margin:0;color:var(--text)}
.row{display:flex;gap:12px;flex-wrap:wrap}.row>div{flex:1;min-width:120px}
button{padding:8px 16px;border-radius:6px;border:1px solid var(--primary);background:var(--primary);color:#201c18;font:inherit;font-weight:600;cursor:pointer}
button:hover{background:var(--primary-hover)}button:active{background:var(--primary-active)}
button.secondary{background:transparent;color:var(--text);border-color:var(--border-strong)}button.secondary:hover{background:var(--raised)}
button.danger{background:var(--err);border-color:var(--err);color:#fff}
.tools select{width:auto;padding:4px 6px}.tools button{padding:4px 10px}
pre{background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:12px;overflow:auto;max-height:70vh;font-size:12px;line-height:1.45;white-space:pre-wrap;word-break:break-all}
.pin{font-size:48px;font-weight:700;letter-spacing:.12em;color:var(--primary);font-variant-numeric:tabular-nums}
.login{max-width:420px;margin:8vh auto}
code{background:var(--raised);padding:1px 6px;border-radius:4px}
table.aps{width:100%;border-collapse:collapse;margin-top:8px}table.aps th,table.aps td{text-align:left;padding:6px 8px;border-top:1px solid var(--border)}table.aps th{color:var(--muted);font-weight:500}
.unsupported{color:var(--faint)}
fieldset{border:1px solid var(--border);border-radius:8px;padding:6px 14px 12px;margin:12px 0}legend{color:var(--muted);padding:0 6px}
.net-head{display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin-bottom:8px}.net-head h2{margin:0}.net-head code{font-size:13px;font-weight:400}
dl.facts{display:grid;grid-template-columns:repeat(auto-fill,minmax(170px,1fr));gap:8px 18px;margin:0}dl.facts dt{color:var(--muted);font-size:12px}dl.facts dd{margin:0;word-break:break-word}
.net-hw{color:var(--faint);font-size:12px;margin:10px 0 0;word-break:break-word}
details.net-edit{margin-top:12px;border-top:1px solid var(--border);padding-top:10px}details.net-edit>summary{cursor:pointer;color:var(--primary);font-weight:600}
.net-scan{margin:10px 0 4px}.net-form .fields{display:grid;grid-template-columns:repeat(auto-fill,minmax(170px,1fr));gap:0 12px}.net-form .fields input{max-width:none}
.net-form label{margin:8px 0 3px;font-size:13px}.choice{display:flex;flex-wrap:wrap;gap:4px 16px;align-items:center;margin-top:10px}.choice span{color:var(--muted)}.choice label{display:inline-flex;align-items:center;gap:6px;margin:0;color:var(--text);font-size:15px}
.net-form .static-fields{grid-template-columns:repeat(auto-fill,minmax(220px,1fr))}
.static-fields{display:none!important}.net-form:has(input[name=method][value=static]:checked) .static-fields{display:grid!important}.net-form:has(input[name=open]:checked) .pass-field{display:none}
@supports not selector(:has(*)){.static-fields{display:grid!important}}
.net-actions{display:flex;flex-wrap:wrap;gap:8px 12px;align-items:center;margin-top:14px}.net-actions .note{flex:1;min-width:220px;font-size:13px}
table.aps td,table.aps th{padding:4px 8px}.net-foot{margin-top:-4px}
input[type=radio],input[type=checkbox]{accent-color:var(--primary)}
@media (max-width:640px){dl.grid{grid-template-columns:1fr}dl.facts{grid-template-columns:1fr 1fr}main{padding:16px}header{padding:12px 16px}}
`

const templates = `
{{define "layout"}}<!doctype html>
<html lang="{{.L}}">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · OpenSignage</title><link rel="stylesheet" href="/style.css"></head>
<body>
<header>
  <a class="brand" href="/"><span class="o">Open</span><span class="s">Signage</span><small>{{t .L "app.subtitle"}}</small></a>
  {{if .LoggedIn}}<nav>
    <a href="/" {{if eq .Nav "status"}}class="active"{{end}}>{{t .L "nav.status"}}</a>
    <a href="/server" {{if eq .Nav "server"}}class="active"{{end}}>{{t .L "nav.server"}}</a>
    <a href="/pairing" {{if eq .Nav "pairing"}}class="active"{{end}}>{{t .L "nav.pairing"}}</a>
    <a href="/display" {{if eq .Nav "display"}}class="active"{{end}}>{{t .L "nav.display"}}</a>
    <a href="/network" {{if eq .Nav "network"}}class="active"{{end}}>{{t .L "nav.network"}}</a>
    <a href="/actions" {{if eq .Nav "actions"}}class="active"{{end}}>{{t .L "nav.actions"}}</a>
    <a href="/logs" {{if eq .Nav "logs"}}class="active"{{end}}>{{t .L "nav.logs"}}</a>
    <a href="/update" {{if eq .Nav "update"}}class="active"{{end}}>{{t .L "nav.update"}}</a>
    <a href="/password" {{if eq .Nav "password"}}class="active"{{end}}>{{t .L "nav.password"}}</a>
  </nav>{{end}}
  <div class="tools">
    <form method="post" action="/lang" class="inline">
      <input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="back" value="{{.Back}}">
      <select name="lang" aria-label="{{t .L "lang.label"}}">{{range .Langs}}<option value="{{.Code}}" {{if .Selected}}selected{{end}}>{{.Name}}</option>{{end}}</select>
      <button class="secondary" type="submit">{{t .L "lang.apply"}}</button>
    </form>
    {{if .LoggedIn}}<form method="post" action="/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="secondary" type="submit">{{t .L "nav.logout"}}</button></form>{{end}}
  </div>
</header>
<main>
  <h1>{{.Title}}</h1>
  {{if .Flash}}<div class="flash">{{t .L (printf "flash.%s" .Flash)}}</div>{{end}}
  {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
  {{.Body}}
</main>
</body></html>{{end}}

{{define "login"}}<div class="card login">
  <form method="post" action="/login">
    <input type="hidden" name="csrf" value="{{.CSRF}}">
    <label for="pw">{{t .L "login.password"}}</label>
    <input id="pw" type="password" name="password" autocomplete="current-password" autofocus required>
    <p><button type="submit">{{t .L "login.submit"}}</button></p>
  </form>
  {{if .Data.Default}}<p class="note">{{t .L "login.defaultHint" "password" "opensignage"}}</p>{{end}}
  {{if .Data.NetworkPending}}<p class="note">{{t .L "login.networkPending"}}</p>{{end}}
</div>{{end}}

{{define "password"}}<div class="card">
  {{if .Data.Central}}<p>{{t .L "password.central"}}</p>
  {{else}}
  {{if .Data.Forced}}<p>{{t .L "password.forced"}}</p>{{end}}
  <form method="post" action="/password">
    <input type="hidden" name="csrf" value="{{.CSRF}}">
    {{if not .Data.Forced}}<label for="cur">{{t .L "password.current"}}</label>
    <input id="cur" type="password" name="current" autocomplete="current-password" required>{{end}}
    <label for="new">{{t .L "password.new"}}</label>
    <input id="new" type="password" name="new" autocomplete="new-password" minlength="8" required>
    <label for="rep">{{t .L "password.repeat"}}</label>
    <input id="rep" type="password" name="repeat" autocomplete="new-password" minlength="8" required>
    <p class="note">{{t .L "password.rules"}}</p>
    <p><button type="submit">{{t .L "password.submit"}}</button></p>
  </form>{{end}}
</div>{{end}}

{{define "linkstate"}}{{$s := .Data}}{{if eq $s.LinkPhase "connected"}}<span class="badge ok">{{t .L "link.connected"}}</span>
{{else if eq $s.LinkPhase "error"}}<span class="badge err">{{t .L (printf "notice.%s" $s.LinkError)}}</span>
{{else if $s.LinkPhase}}<span class="badge warn">{{t .L (printf "link.%s" $s.LinkPhase)}}</span>{{else}}<span class="badge">{{t .L "link.starting"}}</span>{{end}}{{end}}

{{define "status"}}{{$s := .Data}}
<div class="card"><h2>{{t .L "status.device"}}</h2><dl class="grid">
  <dt>{{t .L "status.deviceName"}}</dt><dd>{{if $s.DeviceName}}{{$s.DeviceName}}{{else}}–{{end}}</dd>
  <dt>{{t .L "status.zone"}}</dt><dd>{{if $s.ZoneName}}{{$s.ZoneName}}{{else if $s.ZoneID}}{{$s.ZoneID}}{{else}}{{t .L "status.noZone"}}{{end}}</dd>
  <dt>{{t .L "status.version"}}</dt><dd>{{$s.Version}}</dd>
  <dt>{{t .L "status.hostname"}}</dt><dd>{{$s.Hostname}}</dd>
  <dt>{{t .L "status.ips"}}</dt><dd>{{range $i, $ip := $s.IPs}}{{if $i}}, {{end}}{{$ip}}{{else}}–{{end}}</dd>
  <dt>{{t .L "status.uptime"}}</dt><dd>{{dur $s.Uptime}}</dd>
</dl></div>
<div class="card"><h2>{{t .L "status.connection"}}</h2><dl class="grid">
  <dt>{{t .L "status.state"}}</dt><dd>{{template "linkstate" .}}</dd>
  <dt>{{t .L "status.server"}}</dt><dd>{{if $s.ServerAddress}}{{$s.ServerAddress}}{{else}}{{t .L "server.automatic"}}{{end}}</dd>
  {{if $s.Fingerprint}}<dt>{{t .L "status.fingerprint"}}</dt><dd><code>{{$s.Fingerprint}}</code></dd>{{end}}
  {{if $s.Presented}}<dt>{{t .L "status.presented"}}</dt><dd><code>{{$s.Presented}}</code> – {{t .L "status.certChangedHint"}}</dd>{{end}}
  {{if $s.PairingPIN}}<dt>{{t .L "pairing.pin"}}</dt><dd><span class="pin">{{$s.PairingPIN}}</span></dd>{{end}}
  {{if $s.HaveWelcome}}<dt>{{t .L "update.server"}}</dt><dd>{{$s.ServerVersion}} {{if not $s.VersionMatch}}<span class="badge err">{{t .L "update.mismatch"}}</span>{{end}}</dd>{{end}}
  {{if $s.LinkDetail}}<dt>{{t .L "status.detail"}}</dt><dd class="note">{{$s.LinkDetail}}</dd>{{end}}
  <dt>{{t .L "status.clock"}}</dt><dd>{{secs $s.ClockOffset}}</dd>
</dl></div>
<div class="card"><h2>{{t .L "status.content"}}</h2><dl class="grid">
  <dt>{{t .L "status.mode"}}</dt><dd>{{t .L (printf "mode.%s" $s.Mode)}}</dd>
  <dt>{{t .L "status.source"}}</dt><dd>{{t .L (printf "source.%s" $s.Source.Kind)}}{{if $s.PlaylistName}}: {{$s.PlaylistName}}{{end}}</dd>
  <dt>{{t .L "status.deployment"}}</dt><dd>{{if $s.ActiveDeployment}}<code>{{$s.ActiveDeployment}}</code>{{else}}–{{end}}</dd>
  <dt>{{t .L "status.sync"}}</dt><dd>{{t .L (printf "sync.%s" $s.Sync.State)}}{{if eq $s.Sync.State "preparing"}} ({{bytes $s.Sync.Done}} / {{bytes $s.Sync.Total}}){{end}}{{if $s.Sync.Error}} – <span class="badge err">{{t .L (printf "syncError.%s" $s.Sync.Error)}}</span>{{end}}</dd>
  <dt>{{t .L "status.player"}}</dt><dd>{{t .L (printf "player.%s" $s.Player)}}{{if $s.PlayerRestarts}} · {{t .L "status.restarts" "count" $s.PlayerRestarts}}{{end}}{{if $s.PlayerError}} <span class="note">({{$s.PlayerError}})</span>{{end}}</dd>
</dl></div>
<div class="card"><h2>{{t .L "status.hardware"}}</h2><dl class="grid">
  <dt>{{t .L "status.resolution"}}</dt><dd>{{if $s.HaveDisplay}}{{$s.Display.Width}}×{{$s.Display.Height}} @ {{hz $s.Display.Refresh}} Hz{{if $s.Display.Rotation}} · {{$s.Display.Rotation}}°{{end}} ({{$s.Display.Output}}){{else}}–{{end}}</dd>
  <dt>{{t .L "status.temperature"}}</dt><dd>{{temp $s.Temperature}}</dd>
  <dt>{{t .L "status.cpu"}}</dt><dd>{{float $s.CPU}} %</dd>
  <dt>{{t .L "status.memory"}}</dt><dd>{{bytes $s.RAMUsed}} / {{bytes $s.RAMTotal}} ({{pct $s.RAMUsed $s.RAMTotal}})</dd>
  <dt>{{t .L "status.storage"}}</dt><dd>{{t .L "status.storageFree" "free" (bytes $s.DiskFree) "total" (bytes $s.DiskTotal)}}</dd>
  <dt>{{t .L "status.cache"}}</dt><dd>{{bytes $s.CacheBytes}}</dd>
</dl></div>{{end}}

{{define "server"}}{{$d := .Data}}
<div class="card"><dl class="grid">
  <dt>{{t .L "status.state"}}</dt><dd>{{template "linkstate" (linkctx .L $d.Status)}}</dd>
  <dt>{{t .L "status.server"}}</dt><dd>{{if $d.Status.ServerAddress}}{{$d.Status.ServerAddress}}{{else}}–{{end}}</dd>
  <dt>{{t .L "server.mode"}}</dt><dd>{{if $d.Automatic}}{{t .L "server.automatic"}}{{else}}{{t .L "server.manual"}}: {{$d.Manual}}{{end}}</dd>
  {{if and $d.FromConfig (not $d.SetByUser)}}<dt></dt><dd class="note">{{t .L "server.fromConfig" "address" $d.FromConfig}}</dd>{{end}}
</dl></div>
<div class="card"><form method="post" action="/server">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <div class="inline"><input type="radio" id="m-auto" name="mode" value="auto" {{if $d.Automatic}}checked{{end}}><label for="m-auto">{{t .L "server.automatic"}}</label></div>
  <div class="inline"><input type="radio" id="m-man" name="mode" value="manual" {{if not $d.Automatic}}checked{{end}}><label for="m-man">{{t .L "server.manual"}}</label></div>
  <label for="addr">{{t .L "server.address"}}</label>
  <input id="addr" type="text" name="address" value="{{$d.Manual}}" placeholder="192.168.1.10 / 100.64.0.5:7443">
  <p class="note">{{t .L "server.addressHint"}}</p>
  <p class="note">{{t .L "server.warning"}}</p>
  <p><button type="submit">{{t .L "server.save"}}</button></p>
</form></div>{{end}}

{{define "pairing"}}{{$s := .Data}}
<div class="card"><dl class="grid">
  <dt>{{t .L "status.state"}}</dt><dd>{{template "linkstate" .}}</dd>
  <dt>{{t .L "status.server"}}</dt><dd>{{if $s.ServerAddress}}{{$s.ServerAddress}}{{else}}–{{end}}</dd>
  {{if $s.Fingerprint}}<dt>{{t .L "status.fingerprint"}}</dt><dd><code>{{$s.Fingerprint}}</code></dd>{{end}}
</dl>
{{if $s.PairingPIN}}<p class="pin">{{$s.PairingPIN}}</p><p class="note">{{t .L "pairing.pinHint"}}</p>{{end}}
</div>
<div class="card"><h2>{{t .L "pairing.repairTitle"}}</h2><p>{{t .L "pairing.repairText"}}</p>
<form method="post" action="/pairing"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="do" value="repair">
  <div class="inline"><input type="checkbox" id="c1" name="confirm" value="yes"><label for="c1">{{t .L "pairing.confirm"}}</label></div>
  <p><button type="submit">{{t .L "pairing.repairButton"}}</button></p></form></div>
<div class="card"><h2>{{t .L "pairing.resetTitle"}}</h2><p>{{t .L "pairing.resetText"}}</p>
<form method="post" action="/pairing"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="do" value="reset">
  <div class="inline"><input type="checkbox" id="c2" name="confirm" value="yes"><label for="c2">{{t .L "pairing.confirm"}}</label></div>
  <p><button class="danger" type="submit">{{t .L "pairing.resetButton"}}</button></p></form></div>{{end}}

{{define "display"}}{{$d := .Data}}
{{if not $d.Available}}<div class="card"><p>{{t .L "display.notAvailable"}}</p>{{if $d.Error}}<p class="note">{{$d.Error}}</p>{{end}}</div>
{{else}}
<div class="card"><dl class="grid">
  <dt>{{t .L "display.output"}}</dt><dd>{{$d.Output.Name}}</dd>
  <dt>{{t .L "display.current"}}</dt><dd>{{$d.Output.Width}}×{{$d.Output.Height}} @ {{hz $d.Output.Refresh}} Hz · {{$d.Output.Rotation}}°</dd>
  {{if $d.Error}}<dt>{{t .L "display.lastError"}}</dt><dd class="note">{{$d.Error}}</dd>{{end}}
</dl></div>
<div class="card"><h2>{{t .L "display.mode"}}</h2><form method="post" action="/display">
  <input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="kind" value="list">
  <label for="mode">{{t .L "display.mode"}}</label>
  <select id="mode" name="mode">{{range $d.Modes}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{if eq .Value "auto"}}{{t $.L "display.auto"}}{{else}}{{.Label}}{{end}}</option>{{end}}</select>
  {{template "rotation" .}}
  <p><button type="submit">{{t .L "display.apply"}}</button></p>
</form></div>
<div class="card"><h2>{{t .L "display.custom"}}</h2><p class="note">{{t .L "display.customHint"}}</p><form method="post" action="/display">
  <input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="kind" value="custom">
  <div class="row">
    <div><label for="w">{{t .L "display.width"}}</label><input id="w" type="number" name="width" min="320" max="7680" value="{{if $d.Custom}}{{$d.Saved.Width}}{{end}}" required></div>
    <div><label for="h">{{t .L "display.height"}}</label><input id="h" type="number" name="height" min="200" max="4320" value="{{if $d.Custom}}{{$d.Saved.Height}}{{end}}" required></div>
    <div><label for="r">{{t .L "display.refresh"}}</label><input id="r" type="number" name="refresh" min="1" max="240" step="0.01" value="{{if $d.Custom}}{{hz $d.Saved.Refresh}}{{else}}60{{end}}" required></div>
  </div>
  {{template "rotation" .}}
  <p><button type="submit">{{t .L "display.apply"}}</button></p>
</form></div>{{end}}{{end}}

{{define "rotation"}}<label>{{t .L "display.rotation"}}</label>
<select name="rotation">{{range .Data.Rotations}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{t $.L (printf "display.rot%d" .Value)}}</option>{{end}}</select>{{end}}

{{define "actions"}}{{$d := .Data}}
<div class="card"><h2>{{t .L "actions.restartTitle"}}</h2><p>{{t .L "actions.restartText"}}</p>
<p class="note">{{t .L "status.player"}}: {{t .L (printf "player.%s" $d.Player)}}</p>
<form method="post" action="/actions"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="do" value="restart">
<button type="submit">{{t .L "actions.restartButton"}}</button></form></div>
<div class="card"><h2>{{t .L "actions.regenTitle"}}</h2><p>{{t .L "actions.regenText"}}</p>
{{if $d.Regeneration}}<p><span class="badge warn">{{t .L "actions.regenRunning"}}</span></p>{{end}}
<form method="post" action="/actions"><input type="hidden" name="csrf" value="{{.CSRF}}">
{{if $d.Regeneration}}<input type="hidden" name="do" value="regeneration_off"><button type="submit">{{t .L "actions.regenStop"}}</button>
{{else}}<input type="hidden" name="do" value="regeneration_on"><button type="submit">{{t .L "actions.regenStart"}}</button>{{end}}</form>
<p class="note">{{t .L "actions.regenHint"}}</p></div>
<div class="card"><h2>{{t .L "actions.rebootTitle"}}</h2><p>{{t .L "actions.rebootText"}}</p>
{{if $d.RebootError}}<p class="error">{{t .L "actions.rebootUnavailable" "error" $d.RebootError}}</p>{{else}}
<form method="post" action="/actions"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="do" value="reboot">
<div class="inline"><input type="checkbox" id="c3" name="confirm" value="yes"><label for="c3">{{t .L "pairing.confirm"}}</label></div>
<p><button class="danger" type="submit">{{t .L "actions.rebootButton"}}</button></p></form>{{end}}</div>{{end}}

{{define "logs"}}{{$d := .Data}}
<div class="card"><h2>{{t .L "logs.download"}}</h2>{{range $d.Files}}<p><a href="/logs/download?f={{.Index}}">{{.Name}}</a> <span class="note">({{bytes .Size}})</span></p>{{end}}</div>
<div class="card"><h2>{{t .L "logs.recent"}}</h2>{{if $d.Lines}}<pre>{{$d.Lines}}</pre>{{else}}<p class="note">{{t .L "logs.empty"}}</p>{{end}}</div>{{end}}

{{define "update"}}{{$u := .Data}}
<div class="card"><dl class="grid">
  <dt>{{t .L "update.own"}}</dt><dd>{{$u.Own}}</dd>
  <dt>{{t .L "update.server"}}</dt><dd>{{if $u.HaveServer}}{{$u.Server}}{{else}}{{t .L "update.unknown"}}{{end}}</dd>
  {{if $u.HaveServer}}<dt>{{t .L "status.state"}}</dt><dd>{{if $u.Match}}<span class="badge ok">{{t .L "update.match"}}</span>{{else}}<span class="badge err">{{t .L "update.mismatch"}}</span>{{end}}</dd>{{end}}
  <dt>{{t .L "update.state"}}</dt><dd>{{t .L (printf "update.phase.%s" $u.Phase)}}{{if $u.Version}} · {{$u.Version}}{{end}}{{if $u.Total}} · {{t .L "update.progress" "done" (bytes $u.Done) "total" (bytes $u.Total)}}{{end}}</dd>
  {{if $u.Error}}<dt>{{t .L "update.lastError"}}</dt><dd><code>{{$u.Error}}</code></dd>{{end}}
</dl><p class="note">{{t .L "update.hint"}}</p></div>
{{if and $u.HaveServer (not $u.Match)}}<div class="card"><h2>{{t .L "update.fallbackTitle"}}</h2>
  <p>{{t .L "update.fallbackText" "version" $u.Server "repo" $u.Repo}}</p>
  {{if $u.Fallback}}<form method="post" action="/update"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="do" value="github">
  <button type="submit">{{t .L "update.fallbackButton" "version" $u.Server}}</button></form>
  {{else}}<p class="note">{{t .L "update.fallbackUnavailable" "reason" $u.FallbackNote}}</p>{{end}}
</div>{{end}}{{end}}
`
