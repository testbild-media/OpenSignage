// Package locales embeds the translation files shared by Go (client WebGUI) and the
// TypeScript apps. Layout: <lang>/<namespace>.json with flat keys; the full key is
// "<namespace>.<key>". English is the source and fallback.
package locales

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed */*.json
var files embed.FS

// Languages lists the 24 official EU languages with their native names.
var Languages = []struct{ Code, Name string }{
	{"bg", "Български"}, {"cs", "Čeština"}, {"da", "Dansk"}, {"de", "Deutsch"}, {"el", "Ελληνικά"},
	{"en", "English"}, {"es", "Español"}, {"et", "Eesti"}, {"fi", "Suomi"}, {"fr", "Français"},
	{"ga", "Gaeilge"}, {"hr", "Hrvatski"}, {"hu", "Magyar"}, {"it", "Italiano"}, {"lt", "Lietuvių"},
	{"lv", "Latviešu"}, {"mt", "Malti"}, {"nl", "Nederlands"}, {"pl", "Polski"}, {"pt", "Português"},
	{"ro", "Română"}, {"sk", "Slovenčina"}, {"sl", "Slovenščina"}, {"sv", "Svenska"},
}

// Default is the default language.
const Default = "en"

// Supported reports whether code is one of the 24 languages.
func Supported(code string) bool {
	for _, l := range Languages {
		if l.Code == code {
			return true
		}
	}
	return false
}

// Load returns all messages of a language ("ns.key" → text).
func Load(lang string) (map[string]string, error) {
	out := map[string]string{}
	entries, err := fs.ReadDir(files, lang)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ns := strings.TrimSuffix(e.Name(), ".json")
		data, err := files.ReadFile(path.Join(lang, e.Name()))
		if err != nil {
			return nil, err
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("%s/%s: %w", lang, e.Name(), err)
		}
		for k, v := range m {
			out[ns+"."+k] = v
		}
	}
	return out, nil
}

// Bundle translates with English fallback.
type Bundle struct {
	msgs map[string]map[string]string
}

// NewBundle loads all languages.
func NewBundle() (*Bundle, error) {
	b := &Bundle{msgs: map[string]map[string]string{}}
	for _, l := range Languages {
		m, err := Load(l.Code)
		if err != nil {
			return nil, err
		}
		b.msgs[l.Code] = m
	}
	return b, nil
}

// T translates key into lang, replacing {name} placeholders from params.
func (b *Bundle) T(lang, key string, params ...any) string {
	s, ok := b.msgs[lang][key]
	if !ok || s == "" {
		s, ok = b.msgs[Default][key]
		if !ok {
			return key
		}
	}
	for i := 0; i+1 < len(params); i += 2 {
		s = strings.ReplaceAll(s, "{"+fmt.Sprint(params[i])+"}", fmt.Sprint(params[i+1]))
	}
	return s
}

// Placeholders returns the sorted {placeholders} used in s.
func Placeholders(s string) []string {
	var out []string
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			break
		}
		out = append(out, s[i+1:i+j])
		s = s[i+j+1:]
	}
	sort.Strings(out)
	return out
}
