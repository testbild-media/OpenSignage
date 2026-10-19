package webgui

// networkTemplates are the pages of the network settings (no scripts: the WebGUI allows
// none, so the scan result links back with the chosen network in the query, and the fields of
// a fixed address or a password show and hide with CSS).
const networkTemplates = `
{{define "network"}}{{$d := .Data}}
{{if $d.HelperNote}}<div class="card"><p>{{t .L "network.helperMissing"}}</p><p class="note">{{t .L "network.helperMissingHint"}}</p></div>{{end}}
{{if $d.View.Applying}}<div class="card"><h2>{{t .L "network.applied.title"}}</h2><p>{{t .L "network.applying.text"}}</p></div>
{{else if $d.View.Pending}}<div class="card"><h2>{{t .L "network.pending.title"}}</h2><p>{{t .L "network.pending.text" "since" $d.Since}}</p>
<form method="post" action="/network"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="do" value="confirm"><p><button type="submit">{{t .L "network.pending.keep"}}</button></p></form></div>{{end}}
{{if $d.LastError}}<div class="error">{{$d.LastError}} {{t .L "network.reverted"}}</div>{{end}}
{{range $d.View.Firmware}}<div class="card"><h2>{{t $.L "network.firmware.title"}}</h2><p>{{t $.L "network.firmware.text" "file" .File "package" .Package}}</p>
{{if $d.View.Available}}<form method="post" action="/network"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="do" value="firmware"><input type="hidden" name="package" value="{{.Package}}">
<p><button type="submit">{{t $.L "network.firmware.install"}}</button></p></form><p class="note">{{t $.L "network.firmware.hint"}}</p>{{end}}</div>{{end}}
{{range $d.Ethernet}}{{template "netiface" (netctx $ .)}}{{end}}
{{range $d.Wifi}}{{template "netiface" (netctx $ .)}}{{end}}
{{if not $d.Wifi}}<div class="card net"><div class="net-head"><h2>{{t .L "network.wifiTitle"}}</h2><span class="badge">{{t .L "network.noWifi"}}</span></div><p class="note">{{t .L "network.noWifiHint"}}</p></div>{{end}}
<p class="note net-foot">{{t .L "network.dns"}}: {{if $d.View.Status.DNS}}{{join $d.View.Status.DNS}}{{else}}–{{end}}{{if $d.View.Status.Country}} · {{t .L "network.countryInEffect"}}: {{$d.View.Status.Country}}{{end}}</p>{{end}}

{{define "netiface"}}{{$p := .P}}{{$L := .P.L}}{{$i := .I}}
<div class="card net" id="if-{{$i.Name}}">
<div class="net-head"><h2>{{if eq $i.Kind "wifi"}}{{t $L "network.wifiTitle"}}{{else}}{{t $L "network.ethernetTitle"}}{{end}} <code>{{$i.Name}}</code></h2>
<span class="badge {{if eq $i.State "connected"}}ok{{else}}warn{{end}}">{{t $L (printf "network.state.%s" $i.State)}}</span>
{{if $i.Pending}}<span class="badge warn">{{t $L "network.pendingBadge"}}</span>{{end}}</div>
<dl class="facts">
  {{if $i.SSID}}<div><dt>{{t $L "network.ssid"}}</dt><dd>{{$i.SSID}}</dd></div>
  <div><dt>{{t $L "network.signal"}}</dt><dd>{{$i.SignalDBM}} dBm · {{$i.SignalPct}} % <span class="badge {{quality $i.Quality}}">{{t $L (printf "network.quality.%s" $i.Quality)}}</span></dd></div>
  {{if $i.Band}}<div><dt>{{t $L "network.band"}}</dt><dd>{{t $L "network.bandValue" "band" $i.Band "channel" $i.Channel}}</dd></div>{{end}}
  {{if $i.BitrateMbps}}<div><dt>{{t $L "network.bitrate"}}</dt><dd>{{rate $i.BitrateMbps}} Mbit/s</dd></div>{{end}}{{end}}
  {{if $i.SpeedMbps}}<div><dt>{{t $L "network.speed"}}</dt><dd>{{$i.SpeedMbps}} Mbit/s</dd></div>{{end}}
  <div><dt>{{t $L "network.ipv4"}}</dt><dd>{{if $i.IPv4Text}}{{$i.IPv4Text}}{{else}}–{{end}}</dd></div>
  {{if $i.Gateway}}<div><dt>{{t $L "network.gateway"}}</dt><dd>{{$i.Gateway}}</dd></div>{{end}}
  <div><dt>{{t $L "network.method"}}</dt><dd>{{if $i.Method}}{{t $L (printf "network.method.%s" $i.Method)}}{{else}}{{t $L "network.method.none"}}{{end}} · {{if $i.Managed}}{{t $L "network.source.webgui"}}{{else}}{{t $L "network.source.system"}}{{end}}</dd></div>
</dl>
{{if and (eq $i.Kind "wifi") (ne $i.State "connected") $i.Configured}}{{if and $i.Configured.Wifi (not $i.Configured.Disabled)}}<p class="note">{{t $L "network.waitingFor" "ssid" $i.Configured.Wifi.SSID}}</p>{{end}}{{end}}
<p class="net-hw">{{if $i.IPv6}}IPv6 {{join $i.IPv6}} · {{end}}MAC {{$i.MAC}}{{if $i.Driver}} · {{$i.Driver}}{{end}}</p>
{{if $p.Data.View.Available}}
<details class="net-edit" id="form-{{$i.Name}}"{{if $i.Open}} open{{end}}>
<summary>{{t $L "network.change"}}</summary>
{{if eq $i.Kind "wifi"}}<form method="post" action="/network" class="net-scan"><input type="hidden" name="csrf" value="{{$p.CSRF}}"><input type="hidden" name="do" value="scan"><input type="hidden" name="iface" value="{{$i.Name}}">
<button class="secondary" type="submit">{{t $L "network.scan"}}</button></form>
{{if $i.Scanned}}{{if $i.ScanError}}<p class="error">{{t $L "network.scanFailed" "error" $i.ScanError}}</p>{{else if not $i.Scan}}<p class="note">{{t $L "network.scanEmpty"}}</p>{{else}}
<table class="aps"><tr><th>{{t $L "network.ssid"}}</th><th>{{t $L "network.signal"}}</th><th>{{t $L "network.security"}}</th><th>{{t $L "network.bandShort"}}</th><th></th></tr>
{{range $i.Scan}}<tr{{if not .Supported}} class="unsupported"{{end}}><td>{{.SSID}}</td><td>{{.SignalPct}} %{{if .SignalDBM}} <span class="note">{{.SignalDBM}} dBm</span>{{end}}</td>
<td>{{t $L (printf "network.security.%s" .SecKey)}}</td><td>{{if .Band}}{{.Band}} GHz{{end}}</td>
<td>{{if .Supported}}<a href="/network?iface={{$i.Name}}&amp;ssid={{.SSID}}&amp;sec={{.Security}}#form-{{$i.Name}}">{{t $L "network.select"}}</a>{{else}}{{t $L "network.unsupported"}}{{end}}</td></tr>{{end}}
</table>{{end}}{{end}}{{end}}
<form method="post" action="/network" class="net-form">
<input type="hidden" name="csrf" value="{{$p.CSRF}}"><input type="hidden" name="iface" value="{{$i.Name}}"><input type="hidden" name="kind" value="{{$i.Kind}}">
{{if eq $i.Kind "wifi"}}<div class="fields">
  <div><label for="ssid-{{$i.Name}}">{{t $L "network.ssid"}}</label><input id="ssid-{{$i.Name}}" type="text" name="ssid" maxlength="32" value="{{$i.Form.SSID}}" required></div>
  <div class="pass-field"><label for="pass-{{$i.Name}}">{{t $L "network.passphrase"}}</label><input id="pass-{{$i.Name}}" type="password" name="passphrase" maxlength="64" autocomplete="new-password"{{if $i.Form.HavePass}} placeholder="{{t $L "network.passphraseKeep"}}"{{end}}></div>
  <div><label for="country-{{$i.Name}}">{{t $L "network.country"}}</label><input id="country-{{$i.Name}}" type="text" name="country" maxlength="2" pattern="[A-Za-z]{2}" value="{{$i.Form.Country}}" title="{{t $L "network.countryHint"}}" required></div>
</div>
<div class="choice">
  <label><input type="checkbox" name="hidden" value="yes"{{if $i.Form.Hidden}} checked{{end}}> {{t $L "network.hidden"}}</label>
  <label><input type="checkbox" name="open" value="yes"{{if $i.Form.Open}} checked{{end}}> {{t $L "network.open"}}</label>
</div>
{{if $i.Form.SAE}}<input type="hidden" name="sae" value="yes">{{end}}{{end}}
<div class="choice"><span>{{t $L "network.addressing"}}:</span>
  <label><input type="radio" name="method" value="dhcp"{{if ne $i.Form.Method "static"}} checked{{end}}> {{t $L "network.dhcp"}}</label>
  <label><input type="radio" name="method" value="static"{{if eq $i.Form.Method "static"}} checked{{end}}> {{t $L "network.static"}}</label>
</div>
<div class="fields static-fields">
  <div><label for="addr-{{$i.Name}}">{{t $L "network.address"}}</label><input id="addr-{{$i.Name}}" type="text" name="address" value="{{$i.Form.Address}}" placeholder="192.168.1.50"></div>
  <div><label for="mask-{{$i.Name}}">{{t $L "network.netmask"}}</label><input id="mask-{{$i.Name}}" type="text" name="netmask" value="{{$i.Form.Netmask}}" placeholder="255.255.255.0"></div>
  <div><label for="gw-{{$i.Name}}">{{t $L "network.gateway"}}</label><input id="gw-{{$i.Name}}" type="text" name="gateway" value="{{$i.Form.Gateway}}" placeholder="192.168.1.1"></div>
  <div><label for="dns1-{{$i.Name}}">{{t $L "network.dns1"}}</label><input id="dns1-{{$i.Name}}" type="text" name="dns1" value="{{$i.Form.DNS1}}"></div>
  <div><label for="dns2-{{$i.Name}}">{{t $L "network.dns2"}}</label><input id="dns2-{{$i.Name}}" type="text" name="dns2" value="{{$i.Form.DNS2}}"></div>
</div>
<div class="net-actions">
  <button type="submit" name="do" value="apply">{{t $L "network.apply"}}</button>
  {{if and (eq $i.Kind "wifi") $i.Configured}}{{if not $i.Configured.Disabled}}<button class="secondary" type="submit" name="do" value="wifiOff" formnovalidate>{{t $L "network.wifiOff"}}</button>{{end}}{{end}}
  <span class="note">{{t $L "network.applyHint"}}</span>
</div>
</form>
</details>{{end}}
</div>{{end}}

{{define "network_applied"}}<div class="card login"><p>{{t .L "network.applied.text"}}</p><p class="note">{{t .L "network.applied.revert"}}</p>
<p><a href="/login">{{t .L "network.applied.login"}}</a></p></div>{{end}}
`
