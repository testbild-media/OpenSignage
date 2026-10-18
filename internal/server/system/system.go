// Package system serves settings, network/certificate information and certificate upload.
package system

import (
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"opensignage/internal/common/atomicfile"
	"opensignage/internal/common/passwords"
	"opensignage/internal/common/tlsutil"
	"opensignage/internal/common/version"
	"opensignage/internal/server/config"
	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
	"opensignage/locales"
)

// CertRefresher re-reads certificates after an upload (implemented by app.Server).
type CertRefresher interface {
	RefreshWebCert() error
}

// Host is implemented by the server: certificate reload and controlled restart.
type Host interface {
	CertRefresher
	Restarter
}

// Module implements settings and system endpoints.
type Module struct {
	app       *core.App
	cert      CertRefresher
	restarter Restarter
}

// New creates the module.
func New(app *core.App, host Host) *Module { return &Module{app: app, cert: host, restarter: host} }

// Routes registers the endpoints.
func (m *Module) Routes(rt *core.Router) {
	rt.Scoped("GET /api/v1/settings", core.RoleOperator, m.get) // defaults for the editors (no secrets)
	rt.Role("PATCH /api/v1/settings", core.RoleAdmin, m.patch)
	rt.Role("POST /api/v1/settings/client-password", core.RoleAdmin, m.clientPassword)
	rt.Role("GET /api/v1/system/network", core.RoleAdmin, m.network)
	rt.Role("POST /api/v1/system/certificate", core.RoleAdmin, m.uploadCert)
	rt.Role("DELETE /api/v1/system/certificate", core.RoleAdmin, m.removeCert)
	m.backupRoutes(rt)
}

type settingsView struct {
	core.Settings
	ClientPasswordSet bool   `json:"clientPasswordSet"`
	DefaultUpdateRepo string `json:"defaultUpdateRepo"`
}

func (m *Module) view() settingsView {
	s := m.app.Settings.Get()
	return settingsView{Settings: s, ClientPasswordSet: s.ClientWebPassword != "", DefaultUpdateRepo: m.app.Cfg.UpdateRepo}
}

func (m *Module) get(w http.ResponseWriter, _ *http.Request, _ *core.Principal) error {
	return httpx.OK(w, m.view())
}

type patchReq struct {
	ServerName         *string               `json:"serverName"`
	Language           *string               `json:"language"`
	Timezone           *string               `json:"timezone"`
	Currency           *string               `json:"currency"`
	Defaults           *core.PlaylistDefault `json:"defaults"`
	TrashRetentionDays *int                  `json:"trashRetentionDays"`
	DeviceDefaults     *core.DeviceDefaults  `json:"deviceDefaults"`
	AutoAlignVersions  *bool                 `json:"autoAlignVersions"`
	UpdateCheck        *bool                 `json:"updateCheck"`
	UpdateRepo         *string               `json:"updateRepo"`
}

func (m *Module) patch(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req patchReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Language != nil && !locales.Supported(*req.Language) {
		return httpx.Invalid("language", "invalid")
	}
	if req.UpdateRepo != nil {
		if repo := strings.TrimSpace(*req.UpdateRepo); repo != "" && !config.ValidRepo(repo) {
			return httpx.Invalid("updateRepo", "invalid")
		}
	}
	var timezoneChanged bool
	_, err := m.app.Settings.Update(r.Context(), func(tx *sql.Tx, old core.Settings, next *core.Settings) error {
		if req.ServerName != nil {
			next.ServerName = strings.TrimSpace(*req.ServerName)
		}
		if req.Language != nil {
			next.Language = *req.Language
		}
		if req.Timezone != nil {
			next.Timezone = *req.Timezone
		}
		if req.Currency != nil {
			next.Currency = strings.ToUpper(*req.Currency)
		}
		if req.Defaults != nil {
			next.Defaults = *req.Defaults
		}
		if req.TrashRetentionDays != nil {
			next.TrashRetentionDays = *req.TrashRetentionDays
		}
		if req.DeviceDefaults != nil {
			next.DeviceDefaults = *req.DeviceDefaults
		}
		if req.AutoAlignVersions != nil {
			next.AutoAlignVersions = *req.AutoAlignVersions
		}
		if req.UpdateCheck != nil {
			next.UpdateCheck = *req.UpdateCheck
		}
		if req.UpdateRepo != nil {
			next.UpdateRepo = strings.TrimSpace(*req.UpdateRepo)
		}
		if err := next.Validate(); err != nil {
			return httpx.Invalid(err.Error(), "invalid")
		}
		timezoneChanged = old.Timezone != next.Timezone
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "settings.updated", ObjectType: "settings", Old: old, New: *next})
	})
	if err != nil {
		return err
	}
	m.app.Bus.Publish(core.Event{Topic: core.TopicSettings, Kind: "updated"})
	if timezoneChanged {
		m.app.Bus.Publish(core.Event{Topic: core.TopicContent, Kind: "timezone"}) // manifests carry the time zone
	}
	return httpx.OK(w, m.view())
}

type clientPwReq struct {
	Password        string `json:"password"`
	PasswordConfirm string `json:"passwordConfirm"`
	Clear           bool   `json:"clear"`
}

// clientPassword sets (or clears) the central client WebGUI password pushed to devices.
func (m *Module) clientPassword(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req clientPwReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	hash := ""
	if !req.Clear {
		if req.Password != req.PasswordConfirm {
			return httpx.Invalid("passwordConfirm", "mismatch")
		}
		if err := passwords.CheckPolicy(req.Password); err != nil {
			return httpx.Invalid("password", err.Error()).With("min", passwords.MinLength)
		}
		h, err := passwords.Hash(req.Password) // slow: outside the settings lock
		if err != nil {
			return err
		}
		hash = h
	}
	_, err := m.app.Settings.Update(r.Context(), func(tx *sql.Tx, _ core.Settings, next *core.Settings) error {
		next.ClientWebPassword = hash
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "settings.client_password", ObjectType: "settings", New: map[string]bool{"set": !req.Clear}})
	})
	if err != nil {
		return err
	}
	m.app.Bus.Publish(core.Event{Topic: core.TopicSettings, Kind: "client_password"})
	m.app.Bus.Publish(core.Event{Topic: core.TopicDevices, Kind: "config"}) // devices module pushes device.config
	return httpx.OK(w, m.view())
}

type certInfo struct {
	Source      string   `json:"source"` // local-ca | uploaded
	Subject     string   `json:"subject"`
	Issuer      string   `json:"issuer"`
	NotAfter    string   `json:"notAfter"`
	DNSNames    []string `json:"dnsNames"`
	IPs         []string `json:"ips"`
	Fingerprint string   `json:"fingerprint"`
}

func describe(c *x509.Certificate, source string) certInfo {
	ci := certInfo{Source: source, Subject: c.Subject.CommonName, Issuer: c.Issuer.CommonName,
		NotAfter: c.NotAfter.UTC().Format(time.RFC3339), DNSNames: c.DNSNames, Fingerprint: tlsutil.SPKIFingerprint(c), IPs: []string{}}
	for _, ip := range c.IPAddresses {
		ci.IPs = append(ci.IPs, ip.String())
	}
	if ci.DNSNames == nil {
		ci.DNSNames = []string{}
	}
	return ci
}

func (m *Module) tlsDir() string { return filepath.Join(m.app.Cfg.DataDir, "tls") }

func (m *Module) network(w http.ResponseWriter, _ *http.Request, _ *core.Principal) error {
	cfg := m.app.Cfg
	source := "local-ca"
	if _, err := os.Stat(filepath.Join(m.tlsDir(), "custom.crt")); err == nil {
		source = "uploaded"
	}
	out := map[string]any{
		"version":       version.Version,
		"addresses":     tlsutil.InterfaceIPs(false),
		"ports":         map[string]int{"http": cfg.HTTPPort, "https": cfg.HTTPSPort, "device": cfg.DevicePort, "discovery": cfg.DiscoveryPort},
		"deviceChannel": describe(m.app.DeviceID.Cert, "self-signed"),
		"localCA":       describe(m.app.CA.Cert, "local-ca"),
		"dataDir":       cfg.DataDir,
		"configFile":    cfg.File,
	}
	if p := m.app.WebCert.Get(); p != nil {
		out["webCertificate"] = describe(p.Cert, source)
	}
	return httpx.OK(w, out)
}

type certUpload struct {
	Certificate string `json:"certificate"` // PEM, chain allowed
	Key         string `json:"key"`         // PEM
}

func (m *Module) uploadCert(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req certUpload
	if err := httpx.Decode(w, r, 256<<10, &req); err != nil {
		return err
	}
	pair, err := tlsutil.Parse([]byte(req.Certificate), []byte(req.Key))
	if err != nil {
		return httpx.E(http.StatusUnprocessableEntity, "certificate_invalid", "certificate and key do not match or cannot be parsed").With("detail", err.Error())
	}
	if time.Now().After(pair.Cert.NotAfter) {
		return httpx.E(http.StatusUnprocessableEntity, "certificate_expired", "certificate expired")
	}
	if blk, _ := pem.Decode([]byte(req.Key)); blk == nil {
		return httpx.E(http.StatusUnprocessableEntity, "certificate_invalid", "key is not PEM")
	}
	if err := atomicfile.Write(filepath.Join(m.tlsDir(), "custom.key"), []byte(req.Key), 0o600); err != nil {
		return err
	}
	if err := atomicfile.Write(filepath.Join(m.tlsDir(), "custom.crt"), []byte(req.Certificate), 0o644); err != nil {
		return err
	}
	if err := m.cert.RefreshWebCert(); err != nil {
		return err
	}
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "certificate.uploaded", ObjectType: "certificate", New: describe(pair.Cert, "uploaded")})
	})
	if err != nil {
		return err
	}
	return httpx.OK(w, describe(pair.Cert, "uploaded"))
}

func (m *Module) removeCert(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	for _, f := range []string{"custom.crt", "custom.key"} {
		if err := atomicfile.Remove(filepath.Join(m.tlsDir(), f)); err != nil {
			return err
		}
	}
	if err := m.cert.RefreshWebCert(); err != nil {
		return err
	}
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "certificate.removed", ObjectType: "certificate"})
	})
	if err != nil {
		return err
	}
	return httpx.NoContent(w)
}
