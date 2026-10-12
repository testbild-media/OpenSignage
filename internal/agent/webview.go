package agent

import (
	"context"
	"time"

	"opensignage/internal/agent/display"
	"opensignage/internal/agent/link"
	"opensignage/internal/agent/sysinfo"
	"opensignage/internal/agent/webgui"
	"opensignage/internal/agent/wire"
	"opensignage/internal/common/tlsutil"
	"opensignage/internal/common/version"
)

var _ webgui.Backend = (*Agent)(nil)

// StatusView implements webgui.Backend.
func (a *Agent) StatusView(ctx context.Context) webgui.Status {
	dep, active := a.sync.Current()
	out := a.sched.Current()
	s := webgui.Status{
		Version:    version.Version,
		Hostname:   a.opts.Hostname,
		Mode:       a.ModeKind(),
		Source:     out.Source,
		Sync:       a.sync.Info(),
		CacheBytes: a.sync.CacheBytes(),
		CPU:        a.cpu.Percent(),
		Uptime:     time.Since(a.started),
	}
	if dep.Active != nil && !dep.Cleared {
		s.ActiveDeployment = dep.Active.ID
	}
	if active != nil && out.Source.PlaylistID != "" {
		for _, p := range active.M.Playlists {
			if p.ID == out.Source.PlaylistID {
				s.PlaylistName = p.Name
			}
		}
	}
	s.Player, s.PlayerRestarts, s.PlayerError, _ = a.PlayerStatus()
	if mem, ok := sysinfo.ReadMemory(); ok {
		s.RAMTotal, s.RAMUsed = mem.Total, mem.Used()
	}
	if t, ok := sysinfo.Temperature(); ok {
		s.Temperature = &t
	}
	if total, free, err := sysinfo.Disk(a.cfg.DataDir); err == nil {
		s.DiskTotal, s.DiskFree = total, free
	}
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if info, ok := display.Info(dctx, a.display); ok {
		s.Display, s.HaveDisplay = info, true
	}
	cancel()

	a.mu.Lock()
	defer a.mu.Unlock()
	s.IPs = append([]string{}, a.ips...)
	s.WebGUIURL = a.webguiURLLocked()
	ls := a.linkState
	s.LinkPhase, s.LinkError, s.LinkDetail = ls.Phase, ls.Error, ls.Detail
	s.Paired = a.server.Paired()
	s.ManualAddress = a.manualAddressLocked()
	s.ServerAddress = s.ManualAddress
	if s.Paired {
		s.ServerAddress = a.server.Address
		s.Fingerprint = tlsutil.ShortFingerprint(a.server.Fingerprint)
		s.FingerprintFull = a.server.Fingerprint
	} else if ls.Address != "" {
		s.ServerAddress = ls.Address
	}
	if ls.Phase == link.PhasePairing && ls.Pairing != nil {
		s.PairingPIN = ls.Pairing.PIN
		s.Fingerprint = tlsutil.ShortFingerprint(ls.Pairing.Fingerprint)
	}
	if ls.Error == link.ErrCertificateChanged && ls.ServerFingerprint != "" {
		s.Presented = tlsutil.ShortFingerprint(ls.ServerFingerprint)
	}
	if a.welcome != nil {
		s.HaveWelcome, s.ServerVersion, s.VersionMatch = true, a.welcome.ServerVersion, a.welcome.VersionMatch
	}
	if c := a.device.Config; c != nil {
		s.DeviceName, s.ZoneID = c.Name, c.ZoneID
	}
	s.ZoneName = a.liveHeader.Zone.Name
	s.ClockOffset = a.offset
	if s.Source.Kind == "" {
		s.Source.Kind = wire.SourceCleared
	}
	return s
}

// serverRelease returns the server version and update repository known from the
// server (welcome wins over device.config).
func (a *Agent) serverRelease() (ver, repo string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.device.Config; c != nil {
		ver, repo = c.ServerVersion, c.UpdateRepo
	}
	if a.welcome != nil && a.welcome.ServerVersion != "" {
		ver = a.welcome.ServerVersion
	}
	return ver, repo
}

// UpdateView implements webgui.Backend.
func (a *Agent) UpdateView(context.Context) webgui.UpdateView {
	us := a.updater.Status()
	sv, repo := a.serverRelease()
	v := webgui.UpdateView{Own: version.Version, Server: sv, HaveServer: sv != "", Match: sv == version.Version, Repo: repo,
		Phase: us.Phase, Version: us.Version, Done: us.Done, Total: us.Total, Error: us.Error, UpdatedAt: us.UpdatedAt}
	if v.HaveServer && !v.Match {
		if err := a.updater.GitHubAvailable(sv, repo); err != nil {
			v.FallbackNote = err.Error()
		} else {
			v.Fallback = true
		}
	}
	return v
}

// StartGitHubUpdate implements webgui.Backend (only exactly the server's version).
func (a *Agent) StartGitHubUpdate() error {
	sv, repo := a.serverRelease()
	return a.updater.StartGitHub(sv, repo)
}

// ServerAddressSettings implements webgui.Backend.
func (a *Agent) ServerAddressSettings() (manual string, setByUser bool, fromConfig string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.manualAddressLocked(), a.server.AddressSetByUser, a.cfg.Server
}

// DeviceLanguage implements webgui.Backend.
func (a *Agent) DeviceLanguage() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.device.Language
}

// LogFiles implements webgui.Backend.
func (a *Agent) LogFiles() []string {
	if a.opts.Logs == nil {
		return nil
	}
	return a.opts.Logs.Files()
}
