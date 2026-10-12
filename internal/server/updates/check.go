package updates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"time"

	"opensignage/internal/common/version"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
)

// nextCheckIn returns the delay until the next periodic check (keeps the 24 h rhythm
// across restarts, but never earlier than CheckDelay after a start).
func (m *Module) nextCheckIn(now time.Time) time.Duration {
	d := m.CheckDelay
	if last := parseTime(m.loadCheck().LastCheckAt); !last.IsZero() {
		if until := last.Add(m.CheckInterval).Sub(now); until > d {
			d = until
		}
	}
	return d
}

// periodicCheck is a hint only: it never downloads or installs anything, and without
// internet it stays quiet (the last error is visible on the updates page).
func (m *Module) periodicCheck(ctx context.Context) {
	if !m.app.Settings.Get().UpdateCheck {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 2*apiTimeout)
	defer cancel()
	if best, err := m.check(cctx); err != nil {
		m.log.Info("updates: periodic update check failed", "err", err)
	} else if best != nil {
		m.log.Info("updates: new release available", "version", best.Version)
	}
}

// check queries the update source and stores the best newer release (and the release of
// the running version, whose client packages align devices).
func (m *Module) check(ctx context.Context) (*candidate, error) {
	m.mu.Lock()
	if m.checking {
		m.mu.Unlock()
		return nil, errBusy.With("operation", "check")
	}
	m.checking = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.checking = false
		m.mu.Unlock()
	}()

	list, err := m.gh.releases(ctx, m.repo())
	c := m.loadCheck()
	c.LastCheckAt = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		var se storedError
		_ = json.Unmarshal([]byte(encodeErr(err)), &se)
		c.LastError = &se
		m.saveCheck(c)
		m.publish("check", "", c)
		return nil, err
	}
	best, same := selectReleases(list, version.Version)
	if err := m.storeCandidates(ctx, best, same); err != nil {
		return nil, err
	}
	c.LastError, c.Latest = nil, ""
	if best != nil {
		c.Latest = best.Version
	}
	m.saveCheck(c)
	m.publish("check", "", c)
	return best, nil
}

func (m *Module) storeCandidates(ctx context.Context, cands ...*candidate) error {
	keep := map[string]bool{}
	for _, c := range cands {
		if c != nil {
			keep[c.Version] = true
		}
	}
	var stale []string
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT version FROM releases WHERE source = 'github' AND state IN (?, ?)`, relAvailable, relFailed)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return err
			}
			if !keep[v] {
				stale = append(stale, v)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, v := range stale {
			if _, err := tx.ExecContext(ctx, `DELETE FROM releases WHERE version = ?`, v); err != nil {
				return err
			}
		}
		for _, c := range cands {
			if c == nil {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO releases(version, source, notes, size, published_at, state, error, path, updated_at)
				VALUES (?, 'github', ?, ?, ?, ?, '', '', ?)
				ON CONFLICT(version) DO UPDATE SET notes = excluded.notes, published_at = excluded.published_at,
					size = CASE WHEN releases.state = 'ready' THEN releases.size ELSE excluded.size END, updated_at = excluded.updated_at`,
				c.Version, c.Notes, c.Size, c.PublishedAt, relAvailable, db.Now()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, v := range stale {
		if op := m.currentOp(); op == nil || op.Version != v {
			_ = os.RemoveAll(m.releaseDir(v)) // partial download of a release that is no longer offered
		}
	}
	return nil
}

func (m *Module) checkHandler(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	ctx, cancel := context.WithTimeout(r.Context(), 2*apiTimeout)
	defer cancel()
	best, err := m.check(ctx)
	entry := core.AuditEntry{Action: "update.checked", ObjectType: "update", ObjectName: m.repo()}
	if err != nil {
		entry.New = map[string]any{"repo": m.repo(), "error": err.Error()}
	} else {
		latest := ""
		if best != nil {
			latest = best.Version
		}
		entry.New = map[string]any{"repo": m.repo(), "latest": latest}
	}
	m.audit(r.Context(), p, clientIP(r), entry)
	if err != nil {
		return err
	}
	return m.overview(w, r, p)
}

type pkgView struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	Size int64  `json:"size"`
}

type releaseView struct {
	Version       string    `json:"version"`
	Source        string    `json:"source"`
	Notes         string    `json:"notes"`
	Size          int64     `json:"size"`
	PublishedAt   string    `json:"publishedAt"`
	State         string    `json:"state"`
	Error         string    `json:"error"`
	UpdatedAt     string    `json:"updatedAt"`
	Newer         bool      `json:"newer"`
	Current       bool      `json:"current"`
	Packages      []pkgView `json:"packages"`
	ServerPackage bool      `json:"serverPackage"`
}

func (m *Module) releaseViews(r *http.Request) ([]releaseView, error) {
	rows, err := m.listReleases(r.Context())
	if err != nil {
		return nil, err
	}
	goos, arch := serverPlatform()
	out := make([]releaseView, 0, len(rows))
	for _, row := range rows {
		cmp := version.CompareStrings(row.Version, version.Version)
		v := releaseView{Version: row.Version, Source: row.Source, Notes: row.Notes, Size: row.Size, PublishedAt: row.PublishedAt,
			State: row.State, Error: row.Error, UpdatedAt: row.UpdatedAt, Newer: cmp > 0, Current: cmp == 0, Packages: []pkgView{}}
		if row.State == relReady {
			info, err := readInfo(m.releaseDir(row.Version))
			if err != nil {
				v.State, v.Error = relFailed, codeErr("release_invalid", err.Error())
			} else {
				for _, p := range info.Packages {
					v.Packages = append(v.Packages, pkgView{OS: p.OS, Arch: p.Arch, Size: p.Size})
				}
				_, v.ServerPackage = info.PackageFor(goos, arch)
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return version.CompareStrings(out[i].Version, out[j].Version) > 0 })
	return out, nil
}

func (m *Module) overview(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	rels, err := m.releaseViews(r)
	if err != nil {
		return err
	}
	st := m.app.Settings.Get()
	c := m.loadCheck()
	m.mu.Lock()
	checking := m.checking
	m.mu.Unlock()
	check := map[string]any{"lastCheckAt": c.LastCheckAt, "lastError": c.LastError, "latest": c.Latest, "checking": checking}
	if until, limited := m.gh.rateLimited(time.Now()); limited {
		check["rateLimitedUntil"] = until.UTC().Format(time.RFC3339)
	}
	goos, arch := serverPlatform()
	server := map[string]any{"possible": m.updater.Layout != nil}
	if m.updater.Layout != nil {
		server["root"] = m.updater.Layout.Root
	} else if m.updater.LayoutErr != nil {
		server["reason"] = m.updater.LayoutErr.Error()
		server["reasonCode"] = "not_installed"
		var le *LayoutError
		if errors.As(m.updater.LayoutErr, &le) {
			server["reasonCode"] = le.Code
		}
	}
	var pending any
	if us, err := LoadState(m.app.Cfg.DataDir); err == nil && us != nil {
		pending = map[string]any{"state": us.State, "from": us.From, "to": us.To, "attempts": us.Attempts,
			"startedAt": us.StartedAt, "error": us.Error}
	}
	return httpx.OK(w, map[string]any{
		"version": version.Version, "platform": goos + "/" + arch,
		"repo": m.repo(), "repoSetting": st.UpdateRepo, "defaultRepo": m.app.Cfg.UpdateRepo,
		"updateCheck": st.UpdateCheck, "autoAlignVersions": st.AutoAlignVersions,
		"check": check, "serverUpdate": server, "pendingUpdate": pending,
		"operation": m.currentOp(), "releases": rels,
	})
}
