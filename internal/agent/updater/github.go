package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"opensignage/internal/agent/safe"
	"opensignage/internal/common/release"
	"opensignage/internal/common/version"
)

// ValidRepo checks "owner/name" for GitHub.
func ValidRepo(s string) bool {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") || len(s) > 200 {
		return false
	}
	for _, c := range owner + name {
		if !(c == '-' || c == '_' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return !strings.Contains(s, "..")
}

// GitHubAvailable reports whether the GitHub fallback may be offered: only for exactly
// the server's version, only when it differs from the running one.
func (u *Updater) GitHubAvailable(serverVersion, repo string) error {
	switch {
	case u.o.Internet == nil:
		return errors.New("disabled")
	case u.o.InstallDir == "":
		return errors.New(ErrNotInstalled)
	case serverVersion == "" || repo == "":
		return errors.New("server version or update repository unknown")
	case !ValidRepo(repo):
		return fmt.Errorf("invalid update repository %q", repo)
	case serverVersion == u.o.Version:
		return errors.New("already at the server version")
	}
	if !version.Valid(serverVersion) {
		return fmt.Errorf("invalid server version %q", serverVersion)
	}
	return nil
}

// StartGitHub installs exactly serverVersion from the GitHub release v<serverVersion> of
// repo. SHA256SUMS must carry a valid signature of a trusted key; release.json and the
// package are checked against it.
func (u *Updater) StartGitHub(serverVersion, repo string) error {
	if err := u.GitHubAvailable(serverVersion, repo); err != nil {
		return err
	}
	u.mu.Lock()
	if u.busy || u.healthPending {
		u.mu.Unlock()
		return errors.New(ErrBusy)
	}
	u.busy = true
	u.mu.Unlock()
	safe.Go("github update "+serverVersion, func() {
		defer func() {
			u.mu.Lock()
			u.busy = false
			u.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		src, code, err := u.resolveGitHub(ctx, serverVersion, repo)
		if err != nil {
			u.fail(source{name: "github", version: serverVersion}, code, err)
			return
		}
		u.install(ctx, src)
	})
	return nil
}

func (u *Updater) resolveGitHub(ctx context.Context, ver, repo string) (source, string, error) {
	base := fmt.Sprintf("%s/%s/releases/download/v%s/", strings.TrimRight(u.o.GitHubBase, "/"), repo, ver)
	u.set(PhaseDownloading, ver, "github", 0, 0, "")
	fetch := func(name string) ([]byte, error) {
		resp, err := u.o.Internet(ctx, base+name, nil)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: HTTP %d", name, resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}
	sums, err := fetch(release.SumsFile)
	if err != nil {
		return source{}, ErrDownload, err
	}
	sig, err := fetch(release.SigFile)
	if err != nil {
		return source{}, ErrSignature, err
	}
	infoData, err := fetch(release.InfoFile)
	if err != nil {
		return source{}, ErrDownload, err
	}
	pkg, code, err := u.verifyRelease(sums, sig, infoData, ver)
	if err != nil {
		return source{}, code, err
	}
	slog.Info("GitHub release verified", "repo", repo, "version", ver, "package", pkg.File)
	return source{name: "github", version: ver, url: base + pkg.File, sha256: pkg.SHA256, size: pkg.Size, get: u.o.Internet}, "", nil
}

// verifyRelease checks release metadata before anything of the release is used:
// SHA256SUMS must carry a valid signature of a trusted key (Options.TrustedKeys),
// release.json must match SHA256SUMS and name exactly version want, and the package for
// this OS/architecture must have the same checksum in both. It returns that package; its
// checksum and size are the only ones the download is checked against. On failure it
// returns the update.progress error code.
func (u *Updater) verifyRelease(sums, sig, infoData []byte, want string) (release.Package, string, error) {
	if err := release.VerifySignature(sums, string(sig), u.o.TrustedKeys); err != nil {
		return release.Package{}, ErrSignature, err
	}
	table, err := release.Sums(sums)
	if err != nil {
		return release.Package{}, ErrSignature, err
	}
	sum := sha256.Sum256(infoData)
	if table[release.InfoFile] != hex.EncodeToString(sum[:]) {
		return release.Package{}, ErrSignature, errors.New("release.json does not match SHA256SUMS")
	}
	var info release.Info
	if err := json.Unmarshal(infoData, &info); err != nil {
		return release.Package{}, ErrPackageInvalid, err
	}
	if err := info.Validate(); err != nil {
		return release.Package{}, ErrPackageInvalid, err
	}
	if info.Version != want {
		return release.Package{}, ErrPackageInvalid, fmt.Errorf("release is %s, expected exactly %s", info.Version, want)
	}
	pkg, ok := info.PackageFor(u.o.OS, u.o.Arch)
	if !ok {
		return release.Package{}, ErrNoPackage, fmt.Errorf("release %s has no package for %s/%s", want, u.o.OS, u.o.Arch)
	}
	if table[pkg.File] != pkg.SHA256 {
		return release.Package{}, ErrSignature, fmt.Errorf("%s: checksum differs between release.json and SHA256SUMS", pkg.File)
	}
	return pkg, "", nil
}

// InternetGetter is a plain HTTPS client for the GitHub fallback (system CA roots, no
// proxy configuration beyond the environment, redirects to the GitHub CDN allowed).
func InternetGetter() Getter {
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second, IdleConnTimeout: 60 * time.Second}}
	return func(ctx context.Context, target string, hdr http.Header) (*http.Response, error) {
		if !strings.HasPrefix(target, "https://") {
			return nil, errors.New("only https URLs are allowed")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range hdr {
			req.Header[k] = v
		}
		return c.Do(req)
	}
}
