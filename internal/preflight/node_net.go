package preflight

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// PF-6xx: how the node sits on the network
// ---------------------------------------------------------------------------

// ControlPlanePorts are what a server has to reach on its peers.
//
// 9345 is the one people miss: it is the RKE2 supervisor, it is not a
// Kubernetes port, and a firewall rule set written from a Kubernetes reference
// leaves it closed. The symptom is a join that hangs rather than fails.
var ControlPlanePorts = []int{6443, 9345, 2379, 2380, 10250}

// CheckPortMatrix implements PF-601 from this node outward.
//
// Reachability is measured from the node rather than from the machine running
// the tool, because that is the path that has to work: a bastion with its own
// route to the control plane proves nothing about what the nodes see.
func (n *Node) CheckPortMatrix(ctx context.Context, peers []string) ProbeResult {
	if len(peers) == 0 {
		return skipped("PF-601", "there is no other node to reach")
	}

	var blocked []string
	for _, peer := range peers {
		for _, port := range ControlPlanePorts {
			// A refused connection means the host is up and nothing is
			// listening, which is expected before an install. A timeout means
			// something is dropping the packet, which is the firewall finding.
			cmd := fmt.Sprintf(
				"timeout 3 bash -c '</dev/tcp/%s/%d' 2>&1; echo rc=$?", peer, port)
			r := n.run(ctx, cmd)
			if r.ExitCode < 0 {
				return unmeasured("PF-601", "the node could not be asked: "+r.Err())
			}
			out := r.Out()
			// 124 is what timeout returns when it had to kill the attempt.
			if strings.Contains(out, "rc=124") {
				blocked = append(blocked, fmt.Sprintf("%s:%d", peer, port))
			}
		}
	}

	if len(blocked) == 0 {
		return passf("PF-601", "nothing silently drops traffic to %s on %v; "+
			"connections are refused rather than timing out, which is the state before an install",
			strings.Join(peers, ", "), ControlPlanePorts)
	}
	return failf("PF-601", "PORTS_FILTERED",
		"traffic to %s times out rather than being refused, so something between the nodes is dropping it; "+
			"9345 is the RKE2 supervisor and is absent from Kubernetes port references, "+
			"which is why a rule set written from one leaves it closed",
		strings.Join(blocked, ", "))
}

// CheckMTU implements PF-602.
//
// An overlay costs 50 bytes of header. Where the path MTU is smaller than the
// nodes believe, small packets pass and large ones vanish, so the cluster comes
// up and then fails on exactly the requests that carry a payload.
func (n *Node) CheckMTU(ctx context.Context, peers []string) ProbeResult {
	if len(peers) == 0 {
		return skipped("PF-602", "there is no other node to measure a path to")
	}

	// Local MTU first: the path cannot exceed it and reading it is free.
	local := n.run(ctx, "ip -o link show | awk '$0 !~ /LOOPBACK/ {for(i=1;i<=NF;i++) if($i==\"mtu\") print $(i+1)}' | sort -n | head -1")
	localMTU := 0
	if local.OK() {
		localMTU, _ = strconv.Atoi(local.Out())
	}

	var small []string
	for _, peer := range peers {
		// A payload of 1472 plus 28 bytes of headers is exactly 1500. If that
		// passes unfragmented, the path carries a standard frame.
		r := n.run(ctx, fmt.Sprintf("ping -c1 -W2 -M do -s 1472 %s >/dev/null 2>&1; echo rc=$?", peer))
		if r.ExitCode < 0 {
			return unmeasured("PF-602", "the node could not be asked: "+r.Err())
		}
		if !strings.Contains(r.Out(), "rc=0") {
			small = append(small, peer)
		}
	}

	if len(small) == 0 {
		return passf("PF-602", "a 1500-byte frame reaches every peer unfragmented (local MTU %d)", localMTU)
	}
	return warnf("PF-602", "MTU_SMALL",
		"a 1500-byte frame does not reach %s unfragmented; an overlay costs another 50 bytes, "+
			"and the symptom of getting this wrong is that small requests work and large ones hang",
		strings.Join(small, ", "))
}

// CheckHostname implements PF-604.
//
// Uniqueness is the hard part: two nodes with the same hostname join and the
// second one takes the first one's identity, which is not recoverable without
// removing a node. Whether the name is fully qualified is a separate and much
// softer question -- RKE2 works either way.
func CheckHostnames(byHost map[string]string) ProbeResult {
	seen := map[string][]string{}
	for host, name := range byHost {
		if name == "" {
			continue
		}
		seen[strings.ToLower(name)] = append(seen[strings.ToLower(name)], host)
	}
	for name, hosts := range seen {
		if len(hosts) > 1 {
			sortStrings(hosts)
			return failf("PF-604", "HOSTNAME_DUPLICATE",
				"%s all report the hostname %q; the second node to join takes the first one's identity, "+
					"and undoing that means removing a node", strings.Join(hosts, " and "), name)
		}
	}
	if len(seen) == 0 {
		return unmeasured("PF-604", "no node reported a hostname")
	}
	return passf("PF-604", "every node has a distinct hostname")
}

// requiredSysctls are what the kubelet and the dataplane assume.
//
// bridge-nf-call-iptables is deliberately absent: it does not exist until
// br_netfilter is loaded, and loading it is a change preflight must not make.
// PF-207 already reports whether the module is available, which is the part
// that cannot be fixed at install time.
var requiredSysctls = map[string]string{
	"net.ipv4.ip_forward": "1",
}

// CheckSysctls implements PF-605.
func (n *Node) CheckSysctls(ctx context.Context) ProbeResult {
	var wrong []string
	for key, want := range requiredSysctls {
		r := n.run(ctx, "sysctl -n "+key+" 2>/dev/null || echo ABSENT")
		if r.ExitCode < 0 {
			return unmeasured("PF-605", "the node could not be asked: "+r.Err())
		}
		got := r.Out()
		if got == "ABSENT" {
			wrong = append(wrong, fmt.Sprintf("%s is not present", key))
			continue
		}
		if got != want {
			wrong = append(wrong, fmt.Sprintf("%s is %s, want %s", key, got, want))
		}
	}
	if len(wrong) == 0 {
		return passf("PF-605", "the sysctl values the kubelet needs are set")
	}
	sortStrings(wrong)
	// The install sets these. What matters is saying so, rather than reporting
	// a failure somebody then goes and fixes by hand for no reason.
	return warnf("PF-605", "SYSCTL_UNSET",
		"%s; the install sets these, and this is worth knowing only if the node is managed centrally "+
			"and would overwrite them", strings.Join(wrong, "; "))
}

// CheckVIPInterface implements PF-607.
//
// "auto" has to resolve to exactly one interface. Where a node is multi-homed
// and both interfaces could carry the VIP, picking one silently means the VIP
// may come up on the wrong network -- reachable from the installer and from
// nowhere the customer cares about.
func (n *Node) CheckVIPInterface(ctx context.Context) ProbeResult {
	v := n.Cluster.Topology.VIP
	if v == nil || v.Address == "" {
		return skipped("PF-607", "no VIP is configured")
	}
	if v.Interface != "" && v.Interface != "auto" {
		r := n.run(ctx, "ip -o link show "+v.Interface+" >/dev/null 2>&1; echo rc=$?")
		if strings.Contains(r.Out(), "rc=0") {
			return passf("PF-607", "the VIP interface %s exists on this node", v.Interface)
		}
		return failf("PF-607", "VIP_INTERFACE_MISSING",
			"the document pins the VIP to %s and this node has no such interface", v.Interface)
	}

	addr, err := netip.ParseAddr(strings.TrimSpace(v.Address))
	if err != nil {
		return unmeasured("PF-607", "the VIP address could not be parsed")
	}
	// The interface that carries the VIP is the one already on its subnet.
	r := n.run(ctx, "ip -o -4 addr show scope global | awk '{print $2, $4}'")
	if !r.OK() {
		return unmeasured("PF-607", "the node's addresses could not be read: "+r.Err())
	}

	var candidates []string
	for _, line := range strings.Split(r.Out(), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		p, err := netip.ParsePrefix(fields[1])
		if err != nil {
			continue
		}
		if p.Contains(addr) {
			candidates = append(candidates, fields[0])
		}
	}

	switch len(candidates) {
	case 1:
		return passf("PF-607", "the VIP %s resolves to %s", addr, candidates[0])
	case 0:
		return failf("PF-607", "VIP_NO_INTERFACE",
			"no interface on this node is on the VIP's subnet (%s), so 'auto' cannot resolve to anything", addr)
	}
	sortStrings(candidates)
	return failf("PF-607", "VIP_INTERFACE_AMBIGUOUS",
		"%s are all on the VIP's subnet, so 'auto' would pick one silently; "+
			"pin topology.vip.interface, or the VIP may come up on the wrong network",
		strings.Join(candidates, ", "))
}

// CheckNodeIP implements PF-609.
//
// A multi-homed node that does not pin its address advertises whichever one
// the default route happens to use. On a DMZ node that is frequently the
// external interface, and the cluster then routes internal traffic out and back.
func (n *Node) CheckNodeIP(ctx context.Context) ProbeResult {
	r := n.run(ctx, "ip -o -4 addr show scope global | awk '{print $2, $4}'")
	if !r.OK() {
		return unmeasured("PF-609", "the node's addresses could not be read: "+r.Err())
	}

	var addrs []string
	for _, line := range strings.Split(r.Out(), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// A dataplane's own interface is not a second network. Once Cilium is
		// running the node carries cilium_host with a /32 out of the pod CIDR,
		// and counting it as multi-homing would warn about every node the tool
		// has already built.
		if isDataplaneInterface(fields[0]) {
			continue
		}
		addrs = append(addrs, fields[0]+" "+fields[1])
	}

	if len(addrs) <= 1 {
		return passf("PF-609", "the node has one global address, so there is nothing to disambiguate")
	}
	if n.Spec.NodeIP != "" {
		for _, a := range addrs {
			if strings.Contains(a, n.Spec.NodeIP+"/") {
				return passf("PF-609", "the node is multi-homed and nodeIP pins %s", n.Spec.NodeIP)
			}
		}
		return failf("PF-609", "NODEIP_ABSENT",
			"nodeIP is %s and this node does not carry that address (it has %s)",
			n.Spec.NodeIP, strings.Join(addrs, ", "))
	}
	sortStrings(addrs)
	return warnf("PF-609", "NODEIP_AMBIGUOUS",
		"the node is multi-homed (%s) and nodeIP is unset, so it advertises whichever address "+
			"the default route uses; on a DMZ node that is often the external one, "+
			"and internal traffic then leaves the network and comes back",
		strings.Join(addrs, ", "))
}

// CheckStubResolver implements PF-610.
//
// CoreDNS forwarding to 127.0.0.53 forwards to a resolver that lives in the
// host's network namespace and not in the pod's, so the query goes nowhere.
// RKE2 handles this, and knowing it is the case is what makes an unexplained
// DNS failure explicable later.
func (n *Node) CheckStubResolver(ctx context.Context) ProbeResult {
	r := n.run(ctx, "grep -E '^nameserver' /etc/resolv.conf 2>/dev/null || true")
	if r.ExitCode < 0 {
		return unmeasured("PF-610", "/etc/resolv.conf could not be read: "+r.Err())
	}
	if !strings.Contains(r.Out(), "127.0.0.53") {
		return passf("PF-610", "/etc/resolv.conf points at a real resolver")
	}

	// The upstream servers are what CoreDNS actually has to be given.
	up := n.run(ctx, "resolvectl status 2>/dev/null | grep -i 'DNS Servers' | head -2 || true").Out()
	detail := "/etc/resolv.conf points at the systemd-resolved stub (127.0.0.53), which does not exist " +
		"inside a pod's network namespace; the upstream list is what CoreDNS has to be given"
	if up != "" {
		detail += ", and it is " + strings.Join(strings.Fields(up), " ")
	}
	return warnResult("PF-610", "RESOLVED_STUB", detail)
}

// ---------------------------------------------------------------------------
// PF-8xx: what is already on the node
// ---------------------------------------------------------------------------

// CheckExistingRuntime implements PF-801.
func (n *Node) CheckExistingRuntime(ctx context.Context) ProbeResult {
	r := n.run(ctx, "for b in docker containerd; do command -v $b >/dev/null 2>&1 && echo $b; done; true")
	if r.ExitCode < 0 {
		return unmeasured("PF-801", "the node could not be asked: "+r.Err())
	}
	found := strings.Fields(r.Out())
	if len(found) == 0 {
		return passf("PF-801", "no container runtime is installed")
	}
	// RKE2 carries its own containerd. A second one is not fatal, but the two
	// compete for the same cgroup and the same image store, and the symptom is
	// a node that looks fine until it runs out of disk twice as fast.
	return warnf("PF-801", "RUNTIME_PRESENT",
		"%s is already installed; RKE2 carries its own containerd, and two runtimes "+
			"keep two image stores on the same disk", strings.Join(found, " and "))
}

// ManagedMarker is the header this tool writes into every file it owns.
//
// It is what separates "a previous installation somebody else left" from "the
// installation this document built". Without that distinction PF-802 blocks
// every run after the first, which would make resume (§4) and adding a node
// impossible -- and the tool would be refusing its own work.
const ManagedMarker = "Managed by platformctl"

// ours reports whether the RKE2 on this node was put there by this tool.
//
// The evidence is the config file's marker rather than a flag the caller
// passes, because a mode flag can be wrong and a file on the node cannot: what
// is being asked is who wrote this, and the file says.
func (n *Node) ours(ctx context.Context) bool {
	r := n.run(ctx, "grep -qF '"+ManagedMarker+"' /etc/rancher/rke2/config.yaml 2>/dev/null; echo rc=$?")
	return strings.Contains(r.Out(), "rc=0")
}

// CheckExistingKubernetes implements PF-802.
func (n *Node) CheckExistingKubernetes(ctx context.Context) ProbeResult {
	r := n.run(ctx, "for p in /etc/rancher/rke2 /etc/rancher/k3s /var/lib/rancher/rke2 /var/lib/rancher/k3s; "+
		"do [ -e $p ] && echo $p; done; true")
	if r.ExitCode < 0 {
		return unmeasured("PF-802", "the node could not be asked: "+r.Err())
	}
	found := strings.Fields(r.Out())
	if len(found) == 0 {
		return passf("PF-802", "no previous RKE2 or k3s installation is present")
	}
	if n.ours(ctx) {
		return passf("PF-802", "the RKE2 installation on this node was written by platformctl; "+
			"the install phases re-observe it rather than treating it as a leftover")
	}
	return failf("PF-802", "KUBERNETES_PRESENT",
		"an installation this tool did not write is still on this node (%s); installing over it produces "+
			"a cluster that inherits the old certificates and the old etcd, which is not what anybody wants "+
			"and is not what an uninstall leaves behind",
		strings.Join(found, ", "))
}

// CheckPortsFree implements PF-803.
func (n *Node) CheckPortsFree(ctx context.Context) ProbeResult {
	if n.Spec.Role != "" && n.Spec.Role != "server" {
		// An agent needs 10250 and nothing else from the control plane set.
		return n.portsFree(ctx, []int{10250})
	}
	return n.portsFree(ctx, ControlPlanePorts)
}

func (n *Node) portsFree(ctx context.Context, ports []int) ProbeResult {
	var list []string
	for _, p := range ports {
		list = append(list, strconv.Itoa(p))
	}
	// ss reports the process only for root, which is why preflight runs through
	// sudo; without it the port still shows as taken, just anonymously.
	cmd := "ss -lntpH 2>/dev/null | awk '{print $4, $6}' | grep -E ':(" + strings.Join(list, "|") + ")$|:(" +
		strings.Join(list, "|") + ") ' || true"
	r := n.run(ctx, cmd)
	if r.ExitCode < 0 {
		return unmeasured("PF-803", "the node could not be asked: "+r.Err())
	}
	if r.Out() == "" {
		return passf("PF-803", "%s are free", strings.Join(list, ", "))
	}
	if n.ours(ctx) {
		return passf("PF-803", "%s are held by the cluster this document already built", strings.Join(list, ", "))
	}
	return failf("PF-803", "PORT_IN_USE",
		"something this tool did not start is already listening on a port the control plane needs: %s",
		strings.Join(strings.Fields(r.Out()), " "))
}

// cniInterfacePrefixes are what the dataplanes create.
//
// They mean two different things depending on who created them: leftovers from
// a previous installation (PF-804), or the running dataplane of the cluster
// this tool built, which is why PF-609 has to ignore them rather than count
// them as a second network.
var cniInterfacePrefixes = []string{"cni0", "flannel.", "cilium_", "vxlan.calico", "kube-ipvs0", "cali", "lxc"}

func isDataplaneInterface(name string) bool {
	for _, prefix := range cniInterfacePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// CheckCNILeftovers implements PF-804.
//
// A leftover interface from a previous dataplane carries the previous
// dataplane's addresses. The new one comes up beside it and traffic is split
// between them, which presents as intermittent packet loss between pods.
func (n *Node) CheckCNILeftovers(ctx context.Context) ProbeResult {
	r := n.run(ctx, "ip -br link show 2>/dev/null | awk '{print $1}'")
	if !r.OK() {
		return unmeasured("PF-804", "the node's interfaces could not be listed: "+r.Err())
	}

	var found []string
	for _, name := range strings.Fields(r.Out()) {
		if clean := strings.SplitN(name, "@", 2)[0]; isDataplaneInterface(clean) {
			found = append(found, clean)
		}
	}
	if len(found) == 0 {
		return passf("PF-804", "no dataplane interfaces are left over")
	}
	sortStrings(found)
	if n.ours(ctx) {
		return passf("PF-804", "the dataplane interfaces belong to the cluster this document built (%s)",
			strings.Join(found, ", "))
	}
	return failf("PF-804", "CNI_LEFTOVER",
		"interfaces from a dataplane this tool did not install are still present (%s); a new dataplane comes "+
			"up beside them and traffic splits between the two, which presents as intermittent loss between pods",
		strings.Join(found, ", "))
}

// CheckPacketFilterLeftovers implements PF-805.
//
// Requires root: both nft and iptables refuse to read the ruleset otherwise,
// and an unprivileged run reports an empty ruleset that is not empty.
func (n *Node) CheckPacketFilterLeftovers(ctx context.Context) ProbeResult {
	r := n.run(ctx, "iptables-save 2>&1 | grep -cE '^-A (KUBE|CILIUM|CNI|FLANNEL)' || true")
	if r.ExitCode < 0 {
		return unmeasured("PF-805", "the node could not be asked: "+r.Err())
	}
	out := r.Out()
	if strings.Contains(strings.ToLower(out), "permission denied") || strings.Contains(out, "must be root") {
		return unmeasured("PF-805",
			"the packet filter ruleset needs root to read; an unprivileged run reports an empty ruleset "+
				"that is not empty, so nothing is claimed here")
	}
	count, err := strconv.Atoi(strings.Fields(out + " 0")[0])
	if err != nil {
		return unmeasured("PF-805", "the rule count could not be read from "+strconv.Quote(out))
	}
	if count == 0 {
		return passf("PF-805", "no Kubernetes or dataplane rules are left in the packet filter")
	}
	if n.ours(ctx) {
		return passf("PF-805", "%d packet filter rules belong to the cluster this document built", count)
	}
	return failf("PF-805", "PACKET_FILTER_LEFTOVER",
		"%d rules from an installation this tool did not write are still loaded; they outlive the packages "+
			"and they redirect traffic to services that no longer exist", count)
}
