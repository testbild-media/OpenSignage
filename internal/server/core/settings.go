package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/contracts"
	"opensignage/internal/server/db"
	"opensignage/locales"
)

// Settings are the runtime settings stored in the database (no restart needed).
// Restart-only values live in the config file and are shown read-only.
type Settings struct {
	ServerName         string               `json:"serverName"`
	Language           string               `json:"language"` // default UI language
	Timezone           string               `json:"timezone"` // IANA, evaluated explicitly
	Currency           string               `json:"currency"`
	Defaults           PlaylistDefault      `json:"defaults"`
	Presentations      PresentationDefaults `json:"presentations"`
	TrashRetentionDays int                  `json:"trashRetentionDays"` // 0 = never purge
	DeviceDefaults     DeviceDefaults       `json:"deviceDefaults"`
	ClientWebPassword  string               `json:"-"` // Argon2 hash pushed to devices ("" = local)
	AutoAlignVersions  bool                 `json:"autoAlignVersions"`
	UpdateCheck        bool                 `json:"updateCheck"`
	UpdateRepo         string               `json:"updateRepo"` // "" = build default
	TrustedKeys        []string             `json:"trustedKeys"`
}

// PlaylistDefault holds defaults for new playlist items.
type PlaylistDefault struct {
	Mode       string `json:"mode"` // duration | repeat
	DurationMS int64  `json:"durationMs"`
	Repeat     int    `json:"repeat"`
	Fit        string `json:"fit"` // contain | cover | fill
	SlideMS    int64  `json:"slideMs"`
}

// PresentationDefaults are the start values of new presentations, their new slides and new
// texts (the slide duration is Defaults.SlideMS). Existing content keeps its values.
type PresentationDefaults struct {
	Width      int                  `json:"width"`
	Height     int                  `json:"height"`
	Background string               `json:"background"` // color of new slides
	Transition contracts.Transition `json:"transition"`
	// Text: font and color of new texts, clocks and tickers ("font" like in documents, so
	// the font usage finds it).
	Text       TextDefault          `json:"text"`
	Typography contracts.Typography `json:"typography"`
}

// TextDefault is the font and color of new text elements.
type TextDefault struct {
	Font  string `json:"font"`
	Color string `json:"color"`
}

// Document is a new presentation with these values: one empty slide.
func (d PresentationDefaults) Document(id, name string, width, height int, slideMS int64) contracts.Presentation {
	return contracts.Presentation{
		ID: id, Name: name, Width: width, Height: height,
		Typography: d.Typography,
		Slides: []contracts.Slide{{
			ID:         ids.New(),
			Duration:   contracts.Duration{Mode: "fixed", MS: slideMS},
			Background: contracts.Background{Type: "color", Color: d.Background},
			Transition: d.Transition,
			Elements:   []contracts.Element{},
		}},
	}
}

// Fonts lists the font families the defaults name.
func (d PresentationDefaults) Fonts() []string { return append(d.Typography.Fonts(), d.Text.Font) }

// validate checks the values with the rules of a presentation. Whether the fonts exist is
// checked where the settings are changed (the settings do not know the uploaded fonts).
func (d PresentationDefaults) validate(slideMS int64) error {
	doc := d.Document("defaults", "defaults", d.Width, d.Height, slideMS)
	if err := doc.Validate(nil); err != nil {
		return errors.New("presentations")
	}
	if d.Text.Font == "" || !contracts.ValidColor(d.Text.Color) {
		return errors.New("presentations.text")
	}
	return nil
}

// DeviceDefaults apply to newly paired devices.
type DeviceDefaults struct {
	ScaleMode string `json:"scaleMode"` // contain | cover | none
}

// DefaultSettings returns factory settings.
func DefaultSettings() Settings {
	return Settings{
		ServerName: "OpenSignage",
		Language:   "en",
		Timezone:   "UTC",
		Currency:   "EUR",
		Defaults: PlaylistDefault{Mode: "duration", DurationMS: 10000, Repeat: 1, Fit: "contain",
			SlideMS: 10000},
		Presentations: PresentationDefaults{Width: 1920, Height: 1080, Background: "#000000",
			Transition: contracts.Transition{Type: "fade", MS: 600},
			Text:       TextDefault{Font: "Montserrat", Color: "#ffffff"},
			Typography: contracts.DefaultTypography()},
		TrashRetentionDays: 30,
		DeviceDefaults:     DeviceDefaults{ScaleMode: "contain"},
		UpdateCheck:        true,
		TrustedKeys:        []string{},
	}
}

// Validate checks settings values.
func (s *Settings) Validate() error {
	if s.ServerName == "" || len(s.ServerName) > 100 {
		return errors.New("serverName")
	}
	if !locales.Supported(s.Language) {
		return errors.New("language")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil || s.Timezone == "" || s.Timezone == "Local" {
		return errors.New("timezone")
	}
	if len(s.Currency) != 3 {
		return errors.New("currency")
	}
	switch s.Defaults.Mode {
	case "duration", "repeat":
	default:
		return errors.New("defaults.mode")
	}
	if s.Defaults.DurationMS < 1000 || s.Defaults.DurationMS > 24*3600*1000 || s.Defaults.Repeat < 1 || s.Defaults.Repeat > 1000 {
		return errors.New("defaults")
	}
	if s.Defaults.SlideMS < 1000 || s.Defaults.SlideMS > 24*3600*1000 {
		return errors.New("defaults.slideMs")
	}
	switch s.Defaults.Fit {
	case "contain", "cover", "fill":
	default:
		return errors.New("defaults.fit")
	}
	if err := s.Presentations.validate(s.Defaults.SlideMS); err != nil {
		return err
	}
	switch s.TrashRetentionDays {
	case 0, 7, 30, 60, 90:
	default:
		return errors.New("trashRetentionDays")
	}
	switch s.DeviceDefaults.ScaleMode {
	case "contain", "cover", "none":
	default:
		return errors.New("deviceDefaults.scaleMode")
	}
	return nil
}

// SettingsStore caches settings and persists changes.
type SettingsStore struct {
	db  *db.DB
	mu  sync.RWMutex
	cur Settings
	wmu sync.Mutex // serialises Update so the cache follows the commit order
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ReadTx reads the stored settings inside a transaction (defaults for missing fields).
func ReadTx(ctx context.Context, q rowQuerier) (Settings, error) {
	cur := DefaultSettings()
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingsKey).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return cur, nil
	case err != nil:
		return cur, err
	}
	var stored struct {
		Settings
		ClientWebPassword string `json:"clientWebPassword"`
	}
	stored.Settings = cur
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return cur, fmt.Errorf("settings corrupt: %w", err)
	}
	stored.Settings.ClientWebPassword = stored.ClientWebPassword
	return stored.Settings, nil
}

// Update changes settings atomically: the current row is read inside the write
// transaction, fn modifies it (and may write an audit entry with tx), the result is
// validated and stored, and the cache is updated in commit order. Concurrent updates of
// different fields therefore never overwrite each other.
func (s *SettingsStore) Update(ctx context.Context, fn func(tx *sql.Tx, old Settings, next *Settings) error) (Settings, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	var next Settings
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		cur, err := ReadTx(ctx, tx)
		if err != nil {
			return err
		}
		next = cur
		next.TrustedKeys = append([]string(nil), cur.TrustedKeys...)
		if err := fn(tx, cur, &next); err != nil {
			return err
		}
		return s.Save(ctx, tx, next)
	})
	if err != nil {
		return Settings{}, err
	}
	s.Apply(next)
	return next, nil
}

const settingsKey = "settings"

// LoadSettings reads settings (defaults merged for missing fields).
func LoadSettings(ctx context.Context, d *db.DB) (*SettingsStore, error) {
	s := &SettingsStore{db: d, cur: DefaultSettings()}
	var raw string
	err := d.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingsKey).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	default:
		var stored struct {
			Settings
			ClientWebPassword string `json:"clientWebPassword"`
		}
		stored.Settings = DefaultSettings()
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return nil, fmt.Errorf("settings corrupt: %w", err)
		}
		stored.Settings.ClientWebPassword = stored.ClientWebPassword
		s.cur = stored.Settings
	}
	return s, nil
}

// Get returns a copy of the current settings.
func (s *SettingsStore) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cur
	c.TrustedKeys = append([]string(nil), s.cur.TrustedKeys...)
	return c
}

// Location returns the configured time zone.
func (s *SettingsStore) Location() *time.Location {
	loc, err := time.LoadLocation(s.Get().Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Save validates and stores settings inside tx; the cache updates after commit via Apply.
func (s *SettingsStore) Save(ctx context.Context, tx *sql.Tx, next Settings) error {
	if err := next.Validate(); err != nil {
		return err
	}
	stored := struct {
		Settings
		ClientWebPassword string `json:"clientWebPassword"`
	}{next, next.ClientWebPassword}
	b, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, settingsKey, string(b), db.Now())
	return err
}

// Apply updates the cache (call after a successful commit).
func (s *SettingsStore) Apply(next Settings) {
	s.mu.Lock()
	s.cur = next
	s.mu.Unlock()
}
