package webgui

// networkTemplates are the pages of the network settings (no scripts: the WebGUI allows
// none, so the scan result links back with the chosen network in the query).
const networkTemplates = `
{{define "network"}}{{$d := .Data}}
{{if $d.HelperNote}}<div class="card"><p>{{t .L "network.helperMissing"}}</p><p class="note">{{t .L "network.helperMissingHint"}}</p></div>{{end}}
{{if $d.View.Pending}}<div class="card"><h2>{{t .L "network.pending.title"}}</h2><p>{{t .L "network.pending.text" "since" $d.Since}}</p></div>{{end}}
{{if $d.View.LastError}}<div class="error">{{t .L "network.error.generic" "error" $d.View.LastError}} {{t .L "network.reverted"}}</div>{{end}}
{{range $d.View.Firmware}}<div class="card"><h2>{{t $.L "network.firmware.title"}}</h2><p>{{t $.L "network.firmware.text" "file" .File "package" .Package}}</p>
{{if $d.View.Available}}<form method="post" action="/network"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="do" value="firmware"><input type="hidden" name="package" value="{{.Package}}">
<p><button type="submit">{{t $.L "network.firmware.install"}}</button></p></form><p class="note">{{t $.L "network.firmware.hint"}}</p>{{end}}</div>{{end}}
{{range $d.Ethernet}}{{template "netiface" (netctx $ .)}}{{end}}
{{range $d.Wifi}}{{template "netiface" (netctx $ .)}}{{end}}
{{if not $d.Wifi}}<div class="card"><h2>{{t .L "network.wifiTitle"}}</h2><p><span class="badge">{{t .L "network.noWifi"}}</span></p><p class="note">{{t .L "network.noWifiHint"}}</p></div>{{end}}
<div class="card"><dl class="grid">
  <dt>{{t .L "network.dns"}}</dt><dd>{{if $d.View.Status.DNS}}{{join $d.View.Status.DNS}}{{else}}–{{end}}</dd>
  {{if $d.View.Status.Country}}<dt>{{t .L "network.countryInEffect"}}</dt><dd>{{$d.View.Status.Country}}</dd>{{end}}
</dl></div>{{end}}

{{define "netiface"}}{{$p := .P}}{{$L := .P.L}}{{$i := .I}}
<div class="card" id="if-{{$i.Name}}">
<h2>{{if eq $i.Kind "wifi"}}{{t $L "network.wifiTitle"}}{{else}}{{t $L "network.ethernetTitle"}}{{end}} · <code>{{$i.Name}}</code>{{if $i.Pending}} <span class="badge warn">{{t $L "network.pendingBadge"}}</span>{{end}}</h2>
<dl class="grid">
  <dt>{{t $L "network.state"}}</dt><dd><span class="badge {{if eq $i.State "connected"}}ok{{else}}warn{{end}}">{{t $L (printf "network.state.%s" $i.State)}}</span></dd>
  {{if $i.SSID}}<dt>{{t $L "network.ssid"}}</dt><dd>{{$i.SSID}}</dd>
  <dt>{{t $L "network.signal"}}</dt><dd>{{$i.SignalDBM}} dBm · {{$i.SignalPct}} % <span class="badge {{quality $i.Quality}}">{{t $L (printf "network.quality.%s" $i.Quality)}}</span></dd>
  {{if $i.Band}}<dt>{{t $L "network.band"}}</dt><dd>{{t $L "network.bandValue" "band" $i.Band "channel" $i.Channel}}</dd>{{end}}
  {{if $i.BitrateMbps}}<dt>{{t $L "network.bitrate"}}</dt><dd>{{float $i.BitrateMbps}} Mbit/s</dd>{{end}}{{end}}
  {{if $i.SpeedMbps}}<dt>{{t $L "network.speed"}}</dt><dd>{{$i.SpeedMbps}} Mbit/s</dd>{{end}}
  <dt>{{t $L "network.ipv4"}}</dt><dd>{{if $i.IPv4Text}}{{$i.IPv4Text}}{{else}}–{{end}}</dd>
  {{if $i.Gateway}}<dt>{{t $L "network.gateway"}}</dt><dd>{{$i.Gateway}}</dd>{{end}}
  {{if $i.IPv6}}<dt>IPv6</dt><dd>{{join $i.IPv6}}</dd>{{end}}
  <dt>{{t $L "network.method"}}</dt><dd>{{if $i.Method}}{{t $L (printf "network.method.%s" $i.Method)}}{{else}}{{t $L "network.method.none"}}{{end}} · {{if $i.Managed}}{{t $L "network.source.webgui"}}{{else}}{{t $L "network.source.system"}}{{end}}</dd>
  <dt>MAC</dt><dd><code>{{$i.MAC}}</code>{{if $i.Driver}} · {{$i.Driver}}{{end}}</dd>
</dl></div>
{{if $p.Data.View.Available}}
{{if eq $i.Kind "wifi"}}<div class="card"><h2>{{t $L "network.scanTitle"}}</h2>
<form method="post" action="/network"><input type="hidden" name="csrf" value="{{$p.CSRF}}"><input type="hidden" name="do" value="scan"><input type="hidden" name="iface" value="{{$i.Name}}">
<button class="secondary" type="submit">{{t $L "network.scan"}}</button></form>
{{if $i.Scanned}}{{if $i.ScanError}}<p class="error">{{t $L "network.scanFailed" "error" $i.ScanError}}</p>{{else if not $i.Scan}}<p class="note">{{t $L "network.scanEmpty"}}</p>{{else}}
<table class="aps"><tr><th>{{t $L "network.ssid"}}</th><th>{{t $L "network.signal"}}</th><th>{{t $L "network.security"}}</th><th>{{t $L "network.bandShort"}}</th><th></th></tr>
{{range $i.Scan}}<tr{{if not .Supported}} class="unsupported"{{end}}><td>{{.SSID}}</td><td>{{.SignalPct}} %{{if .SignalDBM}} ({{.SignalDBM}} dBm){{end}}</td>
<td>{{t $L (printf "network.security.%s" .SecKey)}}</td><td>{{if .Band}}{{.Band}} GHz{{end}}</td>
<td>{{if .Supported}}<a href="/network?iface={{$i.Name}}&amp;ssid={{.SSID}}&amp;sec={{.Security}}#form-{{$i.Name}}">{{t $L "network.select"}}</a>{{else}}{{t $L "network.unsupported"}}{{end}}</td></tr>{{end}}
</table>{{end}}{{end}}</div>{{end}}
<div class="card" id="form-{{$i.Name}}"><h2>{{t $L "network.settingsTitle"}}</h2>
<form method="post" action="/network">
<input type="hidden" name="csrf" value="{{$p.CSRF}}"><input type="hidden" name="do" value="apply"><input type="hidden" name="iface" value="{{$i.Name}}"><input type="hidden" name="kind" value="{{$i.Kind}}">
{{if eq $i.Kind "wifi"}}<fieldset><legend>{{t $L "network.wifiNetwork"}}</legend>
  <label for="ssid-{{$i.Name}}">{{t $L "network.ssid"}}</label><input id="ssid-{{$i.Name}}" type="text" name="ssid" maxlength="32" value="{{$i.Form.SSID}}" required>
  <div class="inline"><input type="checkbox" id="hidden-{{$i.Name}}" name="hidden" value="yes" {{if $i.Form.Hidden}}checked{{end}}><label for="hidden-{{$i.Name}}">{{t $L "network.hidden"}}</label></div>
  <label for="pass-{{$i.Name}}">{{t $L "network.passphrase"}}</label><input id="pass-{{$i.Name}}" type="password" name="passphrase" maxlength="64" autocomplete="new-password"{{if $i.Form.HavePass}} placeholder="{{t $L "network.passphraseKeep"}}"{{end}}>
  <div class="inline"><input type="checkbox" id="open-{{$i.Name}}" name="open" value="yes" {{if $i.Form.Open}}checked{{end}}><label for="open-{{$i.Name}}">{{t $L "network.open"}}</label></div>
  {{if $i.Form.SAE}}<input type="hidden" name="sae" value="yes">{{end}}
  <label for="country-{{$i.Name}}">{{t $L "network.country"}}</label><input id="country-{{$i.Name}}" type="text" name="country" maxlength="2" pattern="[A-Za-z]{2}" value="{{$i.Form.Country}}" required>
  <p class="note">{{t $L "network.countryHint"}}</p>
</fieldset>{{end}}
<fieldset><legend>{{t $L "network.addressing"}}</legend>
  <div class="inline"><input type="radio" id="dhcp-{{$i.Name}}" name="method" value="dhcp" {{if ne $i.Form.Method "static"}}checked{{end}}><label for="dhcp-{{$i.Name}}">{{t $L "network.dhcp"}}</label></div>
  <div class="inline"><input type="radio" id="static-{{$i.Name}}" name="method" value="static" {{if eq $i.Form.Method "static"}}checked{{end}}><label for="static-{{$i.Name}}">{{t $L "network.static"}}</label></div>
  <p class="note">{{t $L "network.staticHint"}}</p>
  <div class="row">
    <div><label for="addr-{{$i.Name}}">{{t $L "network.address"}}</label><input id="addr-{{$i.Name}}" type="text" name="address" value="{{$i.Form.Address}}" placeholder="192.168.1.50"></div>
    <div><label for="mask-{{$i.Name}}">{{t $L "network.netmask"}}</label><input id="mask-{{$i.Name}}" type="text" name="netmask" value="{{$i.Form.Netmask}}" placeholder="255.255.255.0"></div>
  </div>
  <div class="row">
    <div><label for="gw-{{$i.Name}}">{{t $L "network.gateway"}}</label><input id="gw-{{$i.Name}}" type="text" name="gateway" value="{{$i.Form.Gateway}}" placeholder="192.168.1.1"></div>
    <div><label for="dns1-{{$i.Name}}">{{t $L "network.dns1"}}</label><input id="dns1-{{$i.Name}}" type="text" name="dns1" value="{{$i.Form.DNS1}}"></div>
    <div><label for="dns2-{{$i.Name}}">{{t $L "network.dns2"}}</label><input id="dns2-{{$i.Name}}" type="text" name="dns2" value="{{$i.Form.DNS2}}"></div>
  </div>
</fieldset>
<p class="note">{{t $L "network.applyHint"}}</p>
<p><button type="submit">{{t $L "network.apply"}}</button></p>
</form>
{{if and (eq $i.Kind "wifi") $i.Configured}}{{if not $i.Configured.Disabled}}<form method="post" action="/network"><input type="hidden" name="csrf" value="{{$p.CSRF}}"><input type="hidden" name="do" value="wifiOff"><input type="hidden" name="iface" value="{{$i.Name}}">
<p><button class="secondary" type="submit">{{t $L "network.wifiOff"}}</button></p></form>{{end}}{{end}}
</div>{{end}}{{end}}

{{define "network_applied"}}<div class="card login"><p>{{t .L "network.applied.text"}}</p><p class="note">{{t .L "network.applied.revert"}}</p>
<p><a href="/login">{{t .L "network.applied.login"}}</a></p></div>{{end}}
`
