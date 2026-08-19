package preflight

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/cert"
	"platform.ryxen.dev/malmok/internal/codes"
)

// Local probes are the ones that need no node and no SSH.
//
// They are worth separating because they can run before anything is reachable:
// at a customer site the document is written days before the VMs exist, and a
// CIDR collision or a certificate that covers the wrong name is better found
// then than during the install window.

// RKE2's defaults, which apply when the document leaves the fields empty.
const (
	DefaultPodCIDR = "10.42.0.0/16"
	DefaultSvcCIDR = "10.43.0.0/16"
)

// CheckCIDRs implements PF-611.
//
// The pod and service networks are invisible to the customer's own routing:
// nothing on their network is told about them, so an overlap does not fail at
// install. It fails later, intermittently, when a pod happens to be given an
// address that also belongs to something on the physical network -- and the
// symptom is a service that works from some nodes and not others.
func CheckCIDRs(spec v1alpha1.ClusterSpec) ProbeResult {
	pod, svc := spec.Network.PodCIDR, spec.Network.SvcCIDR
	if pod == "" {
		pod = DefaultPodCIDR
	}
	if svc == "" {
		svc = DefaultSvcCIDR
	}

	podNet, err := netip.ParsePrefix(pod)
	if err != nil {
		return fail("PF-611", "CIDR_INVALID", fmt.Sprintf("network.podCIDR %q is not a CIDR", pod))
	}
	svcNet, err := netip.ParsePrefix(svc)
	if err != nil {
		return fail("PF-611", "CIDR_INVALID", fmt.Sprintf("network.svcCIDR %q is not a CIDR", svc))
	}

	if podNet.Overlaps(svcNet) {
		return fail("PF-611", "CIDR_COLLISION", fmt.Sprintf(
			"the pod network %s and the service network %s overlap; "+
				"a service address and a pod address can then be the same address", pod, svc))
	}

	// Everything the document already commits to an address. A cluster network
	// covering any of them takes traffic that was meant for the real thing.
	type claim struct{ what, addr string }
	var claims []claim

	for _, n := range append(append([]v1alpha1.NodeSpec{}, spec.Topology.Servers...), spec.Topology.Agents...) {
		claims = append(claims, claim{"node " + n.Host, n.Host})
		if n.NodeIP != "" && n.NodeIP != n.Host {
			claims = append(claims, claim{"nodeIP of " + n.Host, n.NodeIP})
		}
	}
	if v := spec.Topology.VIP; v != nil && v.Address != "" {
		claims = append(claims, claim{"the VIP", v.Address})
	}
	claims = append(claims, claim{"registrationAddress", spec.Topology.RegistrationAddress})
	for _, gw := range spec.Gateway.Gateways {
		if gw.Address != "" {
			claims = append(claims, claim{"gateway " + gw.Name, gw.Address})
		}
	}

	for _, c := range claims {
		addr, err := netip.ParseAddr(strings.TrimSpace(c.addr))
		if err != nil {
			continue // a hostname, which this check has nothing to say about
		}
		for _, n := range []struct {
			field string
			pfx   netip.Prefix
		}{{"network.podCIDR", podNet}, {"network.svcCIDR", svcNet}} {
			if n.pfx.Contains(addr) {
				return fail("PF-611", "CIDR_COLLISION", fmt.Sprintf(
					"%s (%s) falls inside %s (%s); the cluster network would take traffic meant for it",
					c.what, addr, n.field, n.pfx))
			}
		}
	}

	// The load balancer pool is checked separately: it is a range rather than
	// an address, so containment is the wrong test.
	for _, raw := range spec.Kubernetes.Dataplane.LoadBalancerPool {
		p, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		for _, n := range []struct {
			field string
			pfx   netip.Prefix
		}{{"network.podCIDR", podNet}, {"network.svcCIDR", svcNet}} {
			if p.Overlaps(n.pfx) {
				return fail("PF-611", "CIDR_COLLISION", fmt.Sprintf(
					"the load balancer pool %s overlaps %s (%s)", p, n.field, n.pfx))
			}
		}
	}

	return ProbeResult{
		ID: "PF-611", Status: StatusPass, Severity: codes.SeverityInfo,
		Detail: fmt.Sprintf("the pod network %s and the service network %s collide with nothing the document claims",
			podNet, svcNet),
	}
}

// ---------------------------------------------------------------------------
// Certificate gates
// ---------------------------------------------------------------------------

// FromCert adapts the certificate gates into probe results.
//
// The cert package produces its own finding type so that it does not have to
// import this one; the two would otherwise be a cycle. This is the whole of the
// adapter.
func FromCert(findings []cert.Finding) []ProbeResult {
	out := make([]ProbeResult, 0, len(findings))
	for _, f := range findings {
		out = append(out, ProbeResult{
			ID:       f.ID,
			Status:   statusOf(f.Status),
			Severity: f.Severity,
			Code:     f.Reason,
			Detail:   f.Detail,
			Evidence: f.Evidence,
		})
	}
	return out
}

// statusOf maps a gate outcome onto a probe status.
//
// A warning is a pass that carries a message: the probe either stopped the run
// or it did not, and PF-904 inside its warning window did not. The severity
// carried alongside is what keeps the warning visible in the report.
func statusOf(s cert.Status) Status {
	switch s {
	case cert.StatusFail:
		return StatusFail
	case cert.StatusSkip:
		return StatusSkip
	default:
		return StatusPass
	}
}

// ---------------------------------------------------------------------------
// Listener hostnames
// ---------------------------------------------------------------------------

// ListenerHostnames collects the names a bundle has to cover, for the listeners
// that use the given secret.
//
// Selecting a leaf needs the hostnames, and they live on the listeners rather
// than on the TLS block, so the caller would otherwise have to walk the gateway
// tree itself and get it subtly wrong.
func ListenerHostnames(spec v1alpha1.ClusterSpec, secretRef string) []string {
	seen := map[string]bool{}
	var out []string
	for _, gw := range spec.Gateway.Gateways {
		for _, l := range gw.Listeners {
			if l.TLS == nil || l.TLS.SecretRef != secretRef || l.Hostname == "" {
				continue
			}
			if seen[l.Hostname] {
				continue
			}
			seen[l.Hostname] = true
			out = append(out, l.Hostname)
		}
	}
	sort.Strings(out)
	return out
}

// fail builds a blocking result.
func fail(id, reason, detail string) ProbeResult {
	return ProbeResult{
		ID: id, Status: StatusFail, Severity: codes.SeverityBlock,
		Code: reason, Detail: detail,
	}
}
