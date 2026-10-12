package rke2

import (
	"testing"

	"github.com/ryxenix/malmok/api/v1alpha1"
)

// kube-vip's image is one this tool deploys, so it has to be on the list a
// closed site carries. It was not: `malmok images` listed the charts', the
// storage provisioner's and the metrics stack's, and an air-gapped build with
// a VIP stopped at the vip step for forty-five minutes while containerd asked
// ghcr.io for an image nobody had carried.
func TestKubeVIPImagesFollowTheDocument(t *testing.T) {
	withVIP := v1alpha1.ClusterSpec{Topology: v1alpha1.TopologySpec{
		VIP: &v1alpha1.VIPSpec{Provider: "kube-vip", Address: "192.0.2.10", Mode: "arp"},
	}}
	if got := KubeVIPImages(withVIP); len(got) != 1 || got[0] != KubeVIPImage {
		t.Errorf("a document with a VIP lists %v, want [%s]", got, KubeVIPImage)
	}

	// No VIP, or one with no address, deploys nothing: the catalogue skips
	// the vip phase on exactly that condition.
	for _, s := range []v1alpha1.ClusterSpec{
		{},
		{Topology: v1alpha1.TopologySpec{VIP: &v1alpha1.VIPSpec{Provider: "kube-vip"}}},
	} {
		if got := KubeVIPImages(s); len(got) != 0 {
			t.Errorf("a document that deploys no kube-vip lists %v", got)
		}
	}
}
