// Package images answers what an air-gapped site has to carry.
//
// The chart versions are pinned in this repository, so the set of images the
// platform pulls is fixed for a release and is worked out once -- by
// scripts/images.sh, on a machine with a network -- rather than by every
// operator reading charts at the customer's site.
//
// The list is embedded. `malmok images` therefore answers on a node that has
// no network and no helm, which is the only place the question is ever really
// asked.
package images

import (
	_ "embed"
	"sort"
	"strings"
)

//go:embed images.txt
var raw string

// Image is one image, and the chart that pulls it.
type Image struct {
	Chart   string
	Version string
	Ref     string
}

// All returns every image this release's charts pull.
func All() []Image {
	var out []Image
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		out = append(out, Image{Chart: f[0], Version: f[1], Ref: f[2]})
	}
	return out
}

// For returns the images the named charts pull, deduplicated and sorted.
//
// Two charts can pull the same image, and an operator carrying them wants the
// image once.
func For(charts map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, i := range All() {
		if len(charts) > 0 && !charts[i.Chart] {
			continue
		}
		if seen[i.Ref] {
			continue
		}
		seen[i.Ref] = true
		out = append(out, i.Ref)
	}
	sort.Strings(out)
	return out
}

// Versions reports the chart version each entry was generated from, so a
// version bumped in code without regenerating this file can be caught without
// a network.
func Versions() map[string]string {
	out := map[string]string{}
	for _, i := range All() {
		out[i.Chart] = i.Version
	}
	return out
}
