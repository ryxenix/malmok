// Command gen extracts image references from a rendered chart and its values.
//
// Both are needed and neither is enough. Rendered templates carry the images of
// everything the chart creates directly. They do not carry the images of what
// an operator creates on the chart's behalf: victoria-metrics-k8s-stack
// templates a VMSingle custom resource whose image the operator fills in at
// runtime, so rendering shows the resource and not the image. Those live in the
// chart's values as repository/tag pairs, which is where this looks second.
//
// A grep was tried first and was quietly wrong: it saw 7 of this chart's images
// and missed the data plane entirely, because a nested key has no value on its
// own line. An incomplete list is worse than none -- it is discovered in a room
// with no way to fetch what is missing -- so this walks the documents instead,
// and says on stderr when it finds an image-shaped thing it could not read.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: gen <rendered.yaml> <values.yaml>")
		os.Exit(2)
	}

	found := map[string]bool{}
	for _, doc := range documents(os.Args[1]) {
		fromRendered(doc, found)
	}
	for _, doc := range documents(os.Args[2]) {
		fromValues(doc, found)
	}

	// An untagged reference beside a tagged one for the same repository is the
	// values half seeing a repository whose tag lives elsewhere. Carrying both
	// would have an operator pull a floating tag they did not ask for.
	tagged := map[string]bool{}
	for ref := range found {
		if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
			tagged[ref[:i]] = true
		}
	}
	out := make([]string, 0, len(found))
	for ref := range found {
		if !strings.Contains(ref[strings.LastIndex(ref, "/")+1:], ":") && tagged[ref] {
			continue
		}
		out = append(out, ref)
	}
	sort.Strings(out)
	for _, ref := range out {
		fmt.Println(ref)
	}
}

// documents parses every YAML document in a file, skipping the ones that do not
// parse: helm emits comments and empty documents between manifests.
func documents(path string) []any {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen: %v\n", err)
		os.Exit(1)
	}
	var out []any
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			break
		}
		if v != nil {
			out = append(out, v)
		}
	}
	return out
}

// fromRendered collects `image: <string>` and `somethingImage: <string>`.
//
// A CRD's OpenAPI schema also has keys called image, so a value that is not a
// reference is skipped rather than reported: `type: string` under `properties`
// is not a chart forgetting to name an image.
func fromRendered(node any, found map[string]bool) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if isImageKey(k) {
				if s, ok := v.(string); ok && looksLikeRef(s) {
					found[s] = true
				}
			}
			fromRendered(v, found)
		}
	case []any:
		for _, v := range n {
			fromRendered(v, found)
		}
	}
}

// fromValues assembles repository/tag pairs, which is how a chart states an
// image its templates never write.
func fromValues(node any, found map[string]bool) {
	m, ok := node.(map[string]any)
	if !ok {
		if l, ok := node.([]any); ok {
			for _, v := range l {
				fromValues(v, found)
			}
		}
		return
	}

	if repo, _ := m["repository"].(string); repo != "" {
		ref := repo
		if reg, _ := m["registry"].(string); reg != "" && !strings.Contains(repo, "/") {
			ref = reg + "/" + repo
		}
		tag, _ := m["tag"].(string)
		switch {
		case tag != "":
			if r := ref + ":" + tag; looksLikeRef(r) {
				found[r] = true
			}
		default:
			// The tag comes from the chart's appVersion, which is not in the
			// values. Guessing one would put a wrong image in an operator's
			// hands, and dropping it silently would lose an image they need,
			// so it is reported and the walk continues.
			fmt.Fprintf(os.Stderr, "unresolved\t%s\n", ref)
		}
	}

	for _, v := range m {
		fromValues(v, found)
	}
}

func isImageKey(k string) bool {
	return k == "image" || (strings.HasSuffix(k, "Image") && k != "Image")
}

// looksLikeRef rejects the values that share a key name with an image and are
// not one: a schema type, a pull policy, a templated placeholder.
func looksLikeRef(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "{} \t") {
		return false
	}
	switch s {
	case "string", "object", "array", "boolean", "integer":
		return false
	}
	// A reference has a tag or a digest, or at least a registry path. Anything
	// else is a bare word that happened to sit under a key called image.
	return strings.Contains(s, ":") || strings.Contains(s, "/")
}
