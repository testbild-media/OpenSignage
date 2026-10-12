package updates

import (
	"encoding/json"
	"errors"
	"net/http"

	"opensignage/internal/common/version"
	"opensignage/internal/server/httpx"
)

// Error codes of this module (stable snake_case; the UI translates errors.<code>).
var (
	errBusy              = httpx.E(http.StatusConflict, "update_busy", "another update operation is running")
	errUpdateInProgress  = httpx.E(http.StatusConflict, "update_in_progress", "a server update is in progress")
	errGitHubUnreachable = httpx.E(http.StatusBadGateway, "github_unreachable", "the update source cannot be reached")
	errGitHubRateLimited = httpx.E(http.StatusTooManyRequests, "github_rate_limited", "GitHub rate limit reached")
	errRepoNotFound      = httpx.E(http.StatusNotFound, "github_repo_not_found", "update repository not found")
	errGitHubError       = httpx.E(http.StatusBadGateway, "github_error", "GitHub answered with an error")
	errReleaseNotFound   = httpx.E(http.StatusNotFound, "release_not_found", "release not known")
	errReleaseNotReady   = httpx.E(http.StatusConflict, "release_not_ready", "release not downloaded and verified")
	errReleaseInvalid    = httpx.E(http.StatusUnprocessableEntity, "release_invalid", "release incomplete or invalid")
	errReleaseVersion    = httpx.E(http.StatusUnprocessableEntity, "release_version_mismatch", "release.json names another version")
	errSignature         = httpx.E(http.StatusUnprocessableEntity, "release_signature_invalid", "release signature is not valid for any trusted key")
	errChecksum          = httpx.E(http.StatusUnprocessableEntity, "package_checksum_mismatch", "downloaded file does not match its checksum")
	errDownloadFailed    = httpx.E(http.StatusBadGateway, "download_failed", "download failed")
	errDiskSpace         = httpx.E(http.StatusInsufficientStorage, "insufficient_disk", "not enough free disk space")
	errBundleInvalid     = httpx.E(http.StatusUnprocessableEntity, "bundle_invalid", "not a valid release bundle")
	errReleaseInUse      = httpx.E(http.StatusConflict, "release_in_use", "release is in use")
	errNotInstalled      = httpx.E(http.StatusConflict, "server_not_installed", "server is not installed in the release layout")
	errNoServerPackage   = httpx.E(http.StatusConflict, "no_server_package", "release has no package for this server")
	errNotNewer          = httpx.E(http.StatusConflict, "version_not_newer", "release is not newer than the running version")
	errRolloutRunning    = httpx.E(http.StatusConflict, "rollout_running", "a client update is running")
	errNoRollout         = httpx.E(http.StatusConflict, "no_rollout", "no client update is running")
	errNothingToUpdate   = httpx.E(http.StatusConflict, "nothing_to_update", "all devices run the server version")
	errAlreadyAligned    = httpx.E(http.StatusConflict, "already_aligned", "device already runs the server version")
	errNoPackage         = httpx.E(http.StatusConflict, "no_package", "no release package for this device")
	errNoClientPackages  = httpx.E(http.StatusConflict, "no_client_packages", "the client packages of the server version are missing")
	errPlatformUnknown   = httpx.E(http.StatusConflict, "device_platform_unknown", "device platform unknown")
	errDeviceNotFound    = httpx.E(http.StatusNotFound, "device_not_found", "device not found")
	errDeviceNotPaired   = httpx.E(http.StatusConflict, "device_not_paired", "device is not paired")
	errDeviceOffline     = httpx.E(http.StatusConflict, "device_offline", "device is not connected")
	errKeyInvalid        = httpx.E(http.StatusUnprocessableEntity, "key_invalid", "not a valid release key")
	errKeyExists         = httpx.E(http.StatusConflict, "key_exists", "key already trusted")
	errKeyBuiltin        = httpx.E(http.StatusConflict, "key_builtin", "built-in keys cannot be removed")
)

// Device-level failure codes stored in device_updates.error.
const (
	codeTimeout         = "update_timeout"
	codeOffline         = "device_offline"
	codeDeleted         = "device_deleted"
	codeStopped         = "rollout_stopped"
	codeNoPackage       = "no_package"
	codePlatformUnknown = "device_platform_unknown"
	codeRolloutFailed   = "rollout_failed"
	codeCancelled       = "rollout_cancelled"
)

// storedError is the JSON form of an error kept in the database (releases.error,
// update_runs.error) so the UI can translate it later.
type storedError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Params  map[string]any `json:"params,omitempty"`
}

// encodeErr turns err into the stored JSON form ("" for nil).
func encodeErr(err error) string {
	if err == nil {
		return ""
	}
	var ae *httpx.Error
	se := storedError{Code: "internal", Message: err.Error()}
	if errors.As(err, &ae) {
		se = storedError{Code: ae.Code, Message: ae.Message, Params: ae.Params}
		if cause := errors.Unwrap(ae); cause != nil {
			se.Message = ae.Message + ": " + cause.Error()
		}
	}
	b, _ := json.Marshal(se)
	return string(b)
}

// codeErr stores a plain failure code with an optional detail.
func codeErr(code, detail string) string { return codeErrP(code, detail, nil) }

// codeErrP stores a failure code with a detail and message parameters. The detail is
// also available to the translation as {detail}.
func codeErrP(code, detail string, params map[string]any) string {
	if detail != "" {
		if params == nil {
			params = map[string]any{}
		}
		if _, ok := params["detail"]; !ok {
			params["detail"] = detail
		}
	}
	b, _ := json.Marshal(storedError{Code: code, Message: detail, Params: params})
	return string(b)
}

// validVersion accepts plain SemVer (no build metadata). Versions are used as directory
// names, so anything else is rejected.
func validVersion(v string) bool { return version.ValidRelease(v) }
