package devices

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"opensignage/internal/server/httpx"
)

// Error codes of this module (stable snake_case; the UI translates errors.<code>).
var (
	errInternal          = httpx.ErrInternal
	errRevoked           = httpx.E(http.StatusUnauthorized, "revoked", "device token revoked or unknown")
	errNoToken           = httpx.E(http.StatusUnauthorized, "unauthorized", "device token required")
	errPairingNotFound   = httpx.E(http.StatusNotFound, "pairing_not_found", "no open pairing request with this PIN")
	errPairingSecret     = httpx.E(http.StatusForbidden, "pairing_secret_invalid", "pairing secret does not match")
	errPairingBusy       = httpx.E(http.StatusTooManyRequests, "pairing_busy", "too many pairing requests")
	errDeviceNotFound    = httpx.E(http.StatusNotFound, "device_not_found", "device not found")
	errDeploymentMissing = httpx.E(http.StatusNotFound, "deployment_not_found", "deployment not found")
	errBlobMissing       = httpx.E(http.StatusNotFound, "blob_not_found", "blob not found")
	errVersionMismatch   = httpx.E(http.StatusConflict, "version_mismatch", "agent version differs from the server version")
	errDeviceOffline     = httpx.E(http.StatusConflict, "device_offline", "device is not connected")
	errActionNotFound    = httpx.E(http.StatusNotFound, "action_not_found", "action not found")
	errActionsBusy       = httpx.E(http.StatusTooManyRequests, "actions_busy", "too many pending actions")
	errActionUnsupported = httpx.E(http.StatusConflict, "action_not_supported", "the device software does not support this action yet")
	errNoScreenshot      = httpx.E(http.StatusNotFound, "no_screenshot", "no screenshot available")
	errInvalidImage      = httpx.E(http.StatusUnprocessableEntity, "invalid_image", "expected a JPEG image")
	errNoPackage         = httpx.E(http.StatusNotFound, "no_package", "no update package available")
)

func writeErr(w http.ResponseWriter, r *http.Request, err error) { httpx.WriteError(w, r, err) }

// decodeDevice decodes a device request body leniently: unknown fields are ignored so
// agents of other versions can always pair and report (forward compatible).
func decodeDevice(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		return httpx.E(http.StatusUnsupportedMediaType, "unsupported_media_type", "expected application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return httpx.ErrTooLarge
		}
		return httpx.ErrBadRequest.With("detail", err.Error()).Wrap(err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return httpx.ErrBadRequest.With("detail", "trailing data")
	}
	return nil
}
