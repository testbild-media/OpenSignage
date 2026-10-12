package devices

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"time"

	"opensignage/internal/common/atomicfile"
	"opensignage/internal/common/ids"
	"opensignage/internal/common/version"
	"opensignage/internal/server/devices/deploy"
	"opensignage/internal/server/httpx"
)

const maxScreenshotBytes = 5 << 20

// manifest serves GET /device/v1/deployments/{id}: only deployments of the device's own
// zone, and only to agents running the server version.
func (m *Module) manifest(w http.ResponseWriter, r *http.Request) error {
	dev, err := m.authDevice(r)
	if err != nil {
		return err
	}
	if dev.Version != version.Version {
		return errVersionMismatch.With("server", version.Version).With("agent", dev.Version)
	}
	id := r.PathValue("id")
	if dev.ZoneID == "" || len(id) > 64 {
		return errDeploymentMissing
	}
	var body string
	err = m.app.DB.R.QueryRowContext(r.Context(), `SELECT manifest FROM deployments WHERE id = ? AND zone_id = ?`, id, dev.ZoneID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return errDeploymentMissing
	}
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, body); err != nil {
		m.log.Debug("devices: writing manifest", "device", dev.ID, "err", err)
	}
	return nil
}

// blob serves GET /device/v1/blobs/{sha256} with Range support. Only blobs listed in a
// deployment of the device's zone are served; anything else is 404.
func (m *Module) blob(w http.ResponseWriter, r *http.Request) error {
	dev, err := m.authDevice(r)
	if err != nil {
		return err
	}
	sha := r.PathValue("sha256")
	if !deploy.ValidSHA256(sha) || dev.ZoneID == "" {
		return errBlobMissing
	}
	var mimeType string
	err = m.app.DB.R.QueryRowContext(r.Context(), `SELECT b.mime FROM deployment_blobs db
		JOIN deployments d ON d.id = db.deployment_id
		JOIN blobs b ON b.sha256 = db.sha256
		WHERE db.sha256 = ? AND d.zone_id = ? LIMIT 1`, sha, dev.ZoneID).Scan(&mimeType)
	if errors.Is(err, sql.ErrNoRows) {
		return errBlobMissing
	}
	if err != nil {
		return err
	}
	f, err := os.Open(deploy.BlobPath(m.app.Cfg.DataDir, sha))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			m.log.Error("devices: blob listed in a deployment is missing on disk", "sha256", sha)
			return errBlobMissing
		}
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", mimeType)
	h.Set("ETag", `"`+sha+`"`)
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, "", fi.ModTime(), f)
	return nil
}

// uploadScreenshot handles POST /device/v1/screenshots/{requestId}: a JPEG (≤ 5 MB)
// answering a pending screenshot action. Only the latest screenshot per device is kept.
func (m *Module) uploadScreenshot(w http.ResponseWriter, r *http.Request) error {
	dev, err := m.authDevice(r)
	if err != nil {
		return err
	}
	reqID := r.PathValue("requestId")
	if !m.actions.pendingScreenshot(dev.ID, reqID) || !ids.Valid(dev.ID) {
		return errActionNotFound
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxScreenshotBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return httpx.ErrTooLarge.With("max", maxScreenshotBytes)
		}
		return httpx.ErrBadRequest.Wrap(err)
	}
	if len(body) < 4 || body[0] != 0xFF || body[1] != 0xD8 || body[2] != 0xFF {
		return errInvalidImage
	}
	if err := atomicfile.Write(m.screenshotPath(dev.ID), body, 0o640); err != nil {
		return err
	}
	data, _ := json.Marshal(map[string]any{"size": len(body), "at": time.Now().UTC().Format(time.RFC3339)})
	if m.actions.completeScreenshot(dev.ID, reqID, data) {
		m.publish(EvAction, dev.ID, dev.ZoneID, map[string]any{"requestId": reqID, "state": ActionOK})
	}
	return httpx.NoContent(w)
}

// updatePackage serves GET /device/v1/update/package (stable across versions). The
// updates module provides the packages; without one the answer is 404 no_package.
func (m *Module) updatePackage(w http.ResponseWriter, r *http.Request) error {
	dev, err := m.authDevice(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	ver, goos, arch := q.Get("version"), q.Get("os"), q.Get("arch")
	if !printable(ver, 64) || !printable(goos, 32) || !printable(arch, 32) {
		return httpx.ErrBadRequest
	}
	pp := m.packages.Load()
	if pp == nil || *pp == nil {
		return errNoPackage
	}
	pkg, err := (*pp).DevicePackage(r.Context(), ver, goos, arch)
	if errors.Is(err, ErrNoPackage) {
		return errNoPackage
	}
	if err != nil {
		return err
	}
	if pkg == nil || pkg.Content == nil {
		return errNoPackage
	}
	defer pkg.Content.Close()
	m.log.Info("devices: serving update package", "device", dev.ID, "version", ver, "os", goos, "arch", arch)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": pkg.Name}))
	http.ServeContent(w, r, pkg.Name, pkg.ModTime, pkg.Content)
	return nil
}

// updateRelease serves GET /device/v1/update/release?version=: the signed metadata of a
// release (release.json, SHA256SUMS, SHA256SUMS.sig, unchanged). The agent verifies the
// signature with its own trusted keys and takes the package checksum only from it.
func (m *Module) updateRelease(w http.ResponseWriter, r *http.Request) error {
	if _, err := m.authDevice(r); err != nil {
		return err
	}
	ver := r.URL.Query().Get("version")
	if !version.ValidRelease(ver) {
		return httpx.ErrBadRequest
	}
	pp := m.packages.Load()
	if pp == nil || *pp == nil {
		return errNoPackage
	}
	rel, err := (*pp).DeviceRelease(r.Context(), ver)
	if errors.Is(err, ErrNoPackage) {
		return errNoPackage
	}
	if err != nil {
		return err
	}
	if rel == nil {
		return errNoPackage
	}
	return httpx.OK(w, rel)
}
