package updates

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"

	"opensignage/internal/common/release"
	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
)

type keyView struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Builtin bool   `json:"builtin"`
}

// keyID is a short stable identifier of a public key (URL-safe, unlike the key).
func keyID(k string) string {
	pub, err := release.ParsePublicKey(k)
	if err != nil {
		s := sha256.Sum256([]byte(k))
		return hex.EncodeToString(s[:8])
	}
	s := sha256.Sum256(pub)
	return hex.EncodeToString(s[:8])
}

func (m *Module) keyList() []keyView {
	out := []keyView{}
	seen := map[string]bool{}
	for _, k := range release.BuiltinKeys() {
		id := keyID(k)
		seen[id] = true
		out = append(out, keyView{ID: id, Key: k, Builtin: true})
	}
	for _, k := range m.app.Settings.Get().TrustedKeys {
		if id := keyID(k); !seen[id] {
			seen[id] = true
			out = append(out, keyView{ID: id, Key: k})
		}
	}
	return out
}

func (m *Module) keysHandler(w http.ResponseWriter, _ *http.Request, _ *core.Principal) error {
	return httpx.OK(w, map[string]any{"keys": m.keyList()})
}

func (m *Module) addKeyHandler(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req struct {
		Key string `json:"key"`
	}
	if err := httpx.Decode(w, r, 4096, &req); err != nil {
		return err
	}
	pub, err := release.ParsePublicKey(strings.TrimSpace(req.Key))
	if err != nil {
		return errKeyInvalid
	}
	key := release.FormatPublicKey(pub)
	id := keyID(key)
	for _, k := range m.keyList() {
		if k.ID == id {
			return errKeyExists
		}
	}
	if err := m.saveKeys(r, p, func(keys []string) []string { return append(keys, key) }, "update.key_added", id, key); err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"keys": m.keyList()})
}

func (m *Module) removeKeyHandler(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var found *keyView
	for _, k := range m.keyList() {
		if k.ID == id {
			k := k
			found = &k
		}
	}
	if found == nil {
		return httpx.ErrNotFound
	}
	if found.Builtin {
		return errKeyBuiltin
	}
	err := m.saveKeys(r, p, func(keys []string) []string {
		out := keys[:0]
		for _, k := range keys {
			if keyID(k) != id {
				out = append(out, k)
			}
		}
		return out
	}, "update.key_removed", id, found.Key)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"keys": m.keyList()})
}

// saveKeys changes settings.TrustedKeys (read-modify-write inside the write transaction).
func (m *Module) saveKeys(r *http.Request, p *core.Principal, change func([]string) []string, action, id, key string) error {
	ctx := r.Context()
	var next core.Settings
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		cur := m.app.Settings.Get()
		next = cur
		next.TrustedKeys = change(append([]string{}, cur.TrustedKeys...))
		if next.TrustedKeys == nil {
			next.TrustedKeys = []string{}
		}
		if err := m.app.Settings.Save(ctx, tx, next); err != nil {
			return err
		}
		return core.Audit(ctx, tx, p, clientIP(r), core.AuditEntry{Action: action, ObjectType: "release_key", ObjectID: id, ObjectName: key})
	})
	if err != nil {
		return err
	}
	m.app.Settings.Apply(next)
	m.app.Bus.Publish(core.Event{Topic: core.TopicSettings, Kind: "updated"})
	m.publish("keys", id, nil)
	return nil
}
