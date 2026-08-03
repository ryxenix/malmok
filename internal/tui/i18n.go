package tui

import (
	"embed"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Screen strings live in catalogues, never in code (CLAUDE.md, ADR-009). They
// are embedded rather than read from disk because the binary has to work on an
// air-gapped node with nothing beside it.
//
//go:embed catalogue/*.yaml
var catalogues embed.FS

// Lang is a catalogue name.
type Lang string

const (
	// LangEN is the default. Everything ships working in English.
	LangEN Lang = "en"
	// LangKO is the toggle. Korean is what a customer engineer reads at a site
	// visit, but it is a toggle rather than the default so that a screenshot in
	// a bug report is legible to everyone.
	LangKO Lang = "ko"
)

// Catalogue resolves dotted keys such as "status.running".
type Catalogue struct {
	lang  Lang
	table map[string]string
}

// LoadCatalogue reads one embedded catalogue.
func LoadCatalogue(lang Lang) (*Catalogue, error) {
	raw, err := catalogues.ReadFile("catalogue/" + string(lang) + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("tui: no catalogue for %q: %w", lang, err)
	}
	var tree map[string]any
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("tui: catalogue %q: %w", lang, err)
	}
	c := &Catalogue{lang: lang, table: map[string]string{}}
	flatten("", tree, c.table)
	return c, nil
}

// T returns the string for key, falling back to the key itself.
//
// A missing key renders as the key rather than as blank space: an operator
// seeing "status.running" on screen knows a catalogue entry is missing, whereas
// an empty column just looks broken.
func (c *Catalogue) T(key string) string {
	if c == nil {
		return key
	}
	if v, ok := c.table[key]; ok {
		return v
	}
	return key
}

// Lang returns which catalogue this is.
func (c *Catalogue) Lang() Lang {
	if c == nil {
		return LangEN
	}
	return c.lang
}

// Other returns the catalogue the language key toggles to.
func (c *Catalogue) Other() Lang {
	if c.Lang() == LangEN {
		return LangKO
	}
	return LangEN
}

func flatten(prefix string, tree map[string]any, out map[string]string) {
	for k, v := range tree {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch t := v.(type) {
		case map[string]any:
			flatten(key, t, out)
		case string:
			out[key] = t
		default:
			out[key] = strings.TrimSpace(fmt.Sprint(t))
		}
	}
}
