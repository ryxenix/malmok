package preflight

import (
	"strings"
	"testing"

	"github.com/ryxen/malmok/api/v1alpha1"
)

// baseSpec is a document that passes every local probe, so a case can break one
// thing and be sure that is what it measured.
func baseSpec() v1alpha1.ClusterSpec {
	return v1alpha1.ClusterSpec{
		Network: v1alpha1.NetworkSpec{
			Mode:    v1alpha1.NetworkOnline,
			PodCIDR: "10.42.0.0/16",
			SvcCIDR: "10.43.0.0/16",
		},
		Topology: v1alpha1.TopologySpec{
			RegistrationAddress: "k8s-api.acme.internal",
			Servers:             []v1alpha1.NodeSpec{{Host: "10.10.0.11"}},
			Agents:              []v1alpha1.NodeSpec{{Host: "10.10.0.21"}},
		},
		Kubernetes: v1alpha1.KubernetesSpec{
			Dataplane: v1alpha1.DataplaneSpec{LoadBalancerPool: []string{"10.10.0.240/29"}},
		},
	}
}

func TestCheckCIDRs(t *testing.T) {
	tests := []struct {
		name string
		mut  func(s *v1alpha1.ClusterSpec)
		// want is empty when the document is fine.
		wantReason string
		wantIn     string
	}{
		{
			name: "no collision",
			mut:  func(*v1alpha1.ClusterSpec) {},
		},
		{
			name: "empty CIDRs fall back to the RKE2 defaults and still pass",
			mut: func(s *v1alpha1.ClusterSpec) {
				s.Network.PodCIDR, s.Network.SvcCIDR = "", ""
			},
		},
		{
			name: "pod and service networks overlap",
			mut: func(s *v1alpha1.ClusterSpec) {
				s.Network.SvcCIDR = "10.42.128.0/17"
			},
			wantReason: "CIDR_COLLISION",
			wantIn:     "overlap",
		},
		{
			// The case that costs the most to find later: the customer's own
			// nodes sit inside the pod network, so some pods get an address
			// that also belongs to a node.
			name: "a node sits inside the pod network",
			mut: func(s *v1alpha1.ClusterSpec) {
				s.Network.PodCIDR = "10.10.0.0/16"
			},
			wantReason: "CIDR_COLLISION",
			wantIn:     "node 10.10.0.11",
		},
		{
			name: "the VIP sits inside the service network",
			mut: func(s *v1alpha1.ClusterSpec) {
				s.Topology.VIP = &v1alpha1.VIPSpec{Address: "10.43.0.5"}
			},
			wantReason: "CIDR_COLLISION",
			wantIn:     "the VIP",
		},
		{
			name: "a gateway address sits inside the pod network",
			mut: func(s *v1alpha1.ClusterSpec) {
				s.Gateway.Gateways = []v1alpha1.Gateway{{Name: "public", Address: "10.42.0.9"}}
			},
			wantReason: "CIDR_COLLISION",
			wantIn:     "gateway public",
		},
		{
			name: "the load balancer pool overlaps the service network",
			mut: func(s *v1alpha1.ClusterSpec) {
				s.Kubernetes.Dataplane.LoadBalancerPool = []string{"10.43.9.0/24"}
			},
			wantReason: "CIDR_COLLISION",
			wantIn:     "load balancer pool",
		},
		{
			name: "a nodeIP on a multi-homed host sits inside the pod network",
			mut: func(s *v1alpha1.ClusterSpec) {
				s.Topology.Servers[0].NodeIP = "10.42.7.7"
			},
			wantReason: "CIDR_COLLISION",
			wantIn:     "nodeIP of",
		},
		{
			name: "a hostname registration address is not treated as an address",
			mut: func(s *v1alpha1.ClusterSpec) {
				s.Topology.RegistrationAddress = "k8s.acme.internal"
			},
		},
		{
			name: "a malformed pod CIDR",
			mut: func(s *v1alpha1.ClusterSpec) {
				s.Network.PodCIDR = "10.42.0.0/33"
			},
			wantReason: "CIDR_INVALID",
			wantIn:     "podCIDR",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSpec()
			tc.mut(&s)
			got := CheckCIDRs(s)

			if tc.wantReason == "" {
				if got.Failed() {
					t.Fatalf("PF-611 failed on a healthy document: %s", got.Detail)
				}
				return
			}
			if !got.Failed() {
				t.Fatalf("PF-611 passed: %s", got.Detail)
			}
			if got.Code != tc.wantReason {
				t.Errorf("reason is %q, want %q", got.Code, tc.wantReason)
			}
			if !strings.Contains(got.Detail, tc.wantIn) {
				t.Errorf("the detail does not name what collided (%q): %s", tc.wantIn, got.Detail)
			}
		})
	}
}

// The listener hostnames feed the certificate gate, so the walk has to find
// them wherever they are and not find the ones belonging to another secret.
func TestListenerHostnames(t *testing.T) {
	s := baseSpec()
	s.Gateway.Gateways = []v1alpha1.Gateway{
		{
			Name: "public",
			Listeners: []v1alpha1.ListenerSpec{
				{Name: "https", Hostname: "api.acme.co.kr", TLS: &v1alpha1.ListenerTLS{SecretRef: "public-tls"}},
				{Name: "alt", Hostname: "www.acme.co.kr", TLS: &v1alpha1.ListenerTLS{SecretRef: "public-tls"}},
				{Name: "dup", Hostname: "api.acme.co.kr", TLS: &v1alpha1.ListenerTLS{SecretRef: "public-tls"}},
				{Name: "plain", Hostname: "plain.acme.co.kr"},
			},
		},
		{
			Name: "internal",
			Listeners: []v1alpha1.ListenerSpec{
				{Name: "https", Hostname: "api.acme.internal", TLS: &v1alpha1.ListenerTLS{SecretRef: "internal-tls"}},
			},
		},
	}

	got := ListenerHostnames(s, "public-tls")
	want := []string{"api.acme.co.kr", "www.acme.co.kr"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
