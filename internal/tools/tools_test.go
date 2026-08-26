package tools

import "testing"

// A default build must carry nothing. The tarballs are 108MB across the
// architectures, and an operator who has a network has no reason to download
// them -- the point of the separate airgap build is that everyone else's
// download stays small.
func TestTheDefaultBuildCarriesNothing(t *testing.T) {
	if IsAirgapBuild() {
		t.Error("a build without the airgap tag reports a payload")
	}
	if got := Carried(); len(got) != 0 {
		t.Errorf("a default build carries %v", got)
	}
	for _, name := range Names {
		for _, arch := range Arches {
			if _, ok := Tarball(name, arch); ok {
				t.Errorf("a default build answers for %s/%s", name, arch)
			}
		}
	}
}

// The payload file name is the contract between the release script that fills
// the directory and the binary that reads it. It is asserted here because the
// two live in different languages and nothing else would notice them drifting
// apart until an airgap build turned out to be empty at a customer site.
func TestThePayloadFileNameIsFixed(t *testing.T) {
	for _, tc := range []struct{ name, arch, want string }{
		{Helm, "amd64", "helm-linux-amd64.tar.gz"},
		{K9s, "arm64", "k9s-linux-arm64.tar.gz"},
	} {
		if got := File(tc.name, tc.arch); got != tc.want {
			t.Errorf("File(%q, %q) = %q, want %q", tc.name, tc.arch, got, tc.want)
		}
	}
}
