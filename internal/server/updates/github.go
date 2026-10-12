package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensignage/internal/common/version"
)

// The only network access of the update system is the GitHub releases API and the
// release asset downloads.
const (
	githubAPI       = "https://api.github.com"
	apiTimeout      = 15 * time.Second
	maxAPIBody      = 8 << 20
	releasesPerPage = 10
)

type ghAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt string    `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

// candidate is a release found in the update source.
type candidate struct {
	Version     string
	Notes       string
	PublishedAt string
	Size        int64 // sum of the assets except the offline bundle
	Prerelease  bool
	Assets      map[string]ghAsset
}

// selectReleases picks the highest release newer than current and the release of the
// current version itself (client packages for aligning devices). Pre-releases count
// only when the running version is a pre-release; drafts never.
func selectReleases(list []ghRelease, current string) (best, same *candidate) {
	cur, err := version.Parse(current)
	if err != nil {
		return nil, nil
	}
	curPre := cur.Pre != ""
	var bestV version.SemVer
	for _, r := range list {
		if r.Draft {
			continue
		}
		v := strings.TrimPrefix(strings.TrimSpace(r.TagName), "v")
		if !validVersion(v) {
			continue
		}
		sv, err := version.Parse(v)
		if err != nil {
			continue
		}
		pre := r.Prerelease || sv.Pre != ""
		if pre && !curPre {
			continue
		}
		c := &candidate{Version: v, Notes: r.Body, PublishedAt: r.PublishedAt, Prerelease: pre, Assets: map[string]ghAsset{}}
		for _, a := range r.Assets {
			c.Assets[a.Name] = a
			if !strings.HasSuffix(a.Name, "-bundle.tar") {
				c.Size += a.Size
			}
		}
		switch cmp := version.Compare(sv, cur); {
		case cmp == 0:
			same = c
		case cmp > 0 && (best == nil || version.Compare(sv, bestV) > 0):
			best, bestV = c, sv
		}
	}
	return best, same
}

// ghClient talks to the GitHub releases API without a token and honors rate limits.
type ghClient struct {
	base    string
	api     *http.Client // short timeout (API)
	dl      *http.Client // no overall timeout (assets), stalls are detected while reading
	allowed func(*url.URL) bool

	mu           sync.Mutex
	limitedUntil time.Time
}

// errForeignURL refuses asset URLs and redirects that do not lead to GitHub.
var errForeignURL = errors.New("not a GitHub download URL")

// githubURL reports whether the update system may fetch u: https on the default port to
// api.github.com, github.com or a *.githubusercontent.com download host. Asset URLs come
// from the API answer and redirects; nothing else is followed.
func githubURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "github.com" || h == "api.github.com" || strings.HasSuffix(h, ".githubusercontent.com")
}

func newGHClient() *ghClient {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: apiTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   apiTimeout,
		ResponseHeaderTimeout: apiTimeout,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConns:          4,
		ForceAttemptHTTP2:     true,
	}
	c := &ghClient{base: githubAPI, allowed: githubURL}
	redirect := func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if !c.allowed(req.URL) {
			return fmt.Errorf("redirect to %s: %w", req.URL.Host, errForeignURL)
		}
		return nil
	}
	c.api = &http.Client{Timeout: apiTimeout, Transport: tr, CheckRedirect: redirect}
	c.dl = &http.Client{Transport: tr, CheckRedirect: redirect}
	return c
}

func userAgent() string { return "OpenSignage/" + version.Version + " (+update check)" }

// rateLimited returns the time until which GitHub asked us to wait.
func (c *ghClient) rateLimited(now time.Time) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.limitedUntil, now.Before(c.limitedUntil)
}

// releases lists the newest releases of repo ("owner/name").
func (c *ghClient) releases(ctx context.Context, repo string) ([]ghRelease, error) {
	if until, limited := c.rateLimited(time.Now()); limited {
		return nil, errGitHubRateLimited.With("until", until.UTC().Format(time.RFC3339))
	}
	u := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", c.base, repo, releasesPerPage)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent())
	res, err := c.api.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errGitHubUnreachable.With("detail", shortNetErr(err)).Wrap(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxAPIBody))
	if err != nil {
		return nil, errGitHubUnreachable.With("detail", shortNetErr(err)).Wrap(err)
	}
	switch {
	case res.StatusCode == http.StatusOK:
	case res.StatusCode == http.StatusNotFound:
		return nil, errRepoNotFound.With("repo", repo)
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusForbidden && limitHeaders(res):
		until := c.noteLimit(res)
		return nil, errGitHubRateLimited.With("until", until.UTC().Format(time.RFC3339))
	default:
		return nil, errGitHubError.With("status", res.StatusCode)
	}
	var list []ghRelease
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, errGitHubError.With("status", res.StatusCode).Wrap(err)
	}
	return list, nil
}

func limitHeaders(res *http.Response) bool {
	return res.Header.Get("X-RateLimit-Remaining") == "0" || res.Header.Get("Retry-After") != ""
}

// noteLimit stores how long to wait (Retry-After, X-RateLimit-Reset, else one hour).
func (c *ghClient) noteLimit(res *http.Response) time.Time {
	until := time.Now().Add(time.Hour)
	if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && s > 0 {
		until = time.Now().Add(time.Duration(s) * time.Second)
	} else if r, err := strconv.ParseInt(res.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && r > 0 {
		until = time.Unix(r, 0)
	}
	if until.After(time.Now().Add(24 * time.Hour)) {
		until = time.Now().Add(24 * time.Hour)
	}
	c.mu.Lock()
	c.limitedUntil = until
	c.mu.Unlock()
	return until
}

// shortNetErr keeps network errors readable (no URLs with query strings).
func shortNetErr(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "name resolution failed (" + dns.Name + ")"
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}
