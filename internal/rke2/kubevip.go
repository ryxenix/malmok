package rke2

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/engine"
	"platform.ryxen.dev/malmok/internal/exec"
)

// kube-vip gives the control plane an address that is not any node's.
//
// ADR-008 requires the registration address to be a VIP or a DNS name because a
// node that joined through a peer's own address has that peer baked into its
// configuration -- and removing that peer later means re-joining every node
// that used it. Something has to actually serve that address, and this is it.
//
// The ordering is not circular even though it looks it. The first server comes
// up without needing the VIP: it is the node being joined, not one joining.
// kube-vip then starts as a DaemonSet on it and claims the address, and every
// node after that joins through the VIP.

// KubeVIPVersion is the image tag deployed.
//
// Pinned rather than tracking latest: an airgap bundle has to carry this exact
// image, and a floating tag means the bundle and the manifest disagree about
// what is installed.
const KubeVIPVersion = "v1.2.2"

// KubeVIPImage is the full reference. A private registry rewrites the host via
// system-default-registry, which RKE2 applies to its own manifests.
const KubeVIPImage = "ghcr.io/kube-vip/kube-vip:" + KubeVIPVersion

// ManifestDir is where RKE2 picks up manifests to apply automatically. Putting
// the DaemonSet here rather than running kubectl means the cluster reapplies it
// on restart without this tool being present.
const ManifestDir = DataDir + "/server/manifests"

// KubeVIPManifest is where the DaemonSet lands.
const KubeVIPManifest = ManifestDir + "/malmok-kube-vip.yaml"

// ResolveVIPInterface finds the interface that will carry the VIP.
//
// Same rule as PF-607: the interface already on the VIP's subnet. It is read
// from the node rather than assumed, because a node with two NICs on different
// networks has exactly one that can answer ARP for the address, and picking the
// other produces a VIP reachable from nowhere anybody cares about.
func ResolveVIPInterface(ctx context.Context, runner exec.Runner, vip string) (string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(vip))
	if err != nil {
		return "", fmt.Errorf("rke2: the VIP %q is not an address: %w", vip, err)
	}

	res, err := runner.Run(ctx, "ip -o -4 addr show scope global | awk '{print $2, $4}'")
	if err != nil {
		return "", fmt.Errorf("rke2: read the addresses of %s: %w", runner.Host(), err)
	}

	var found []string
	seen := map[string]bool{}
	for _, line := range strings.Split(res.Out(), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		p, err := netip.ParsePrefix(fields[1])
		if err != nil || !p.Contains(addr) {
			continue
		}
		// The VIP already assigned is the answer, not a candidate: re-running
		// against a cluster this tool built must resolve to the interface
		// already carrying it.
		if p.Addr() == addr {
			return fields[0], nil
		}
		// Several addresses on one interface are still one interface.
		if !seen[fields[0]] {
			seen[fields[0]] = true
			found = append(found, fields[0])
		}
	}

	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("rke2: no interface on %s is on the VIP's subnet (%s), "+
			"so nothing there can answer for it", runner.Host(), addr)
	}
	sortStrings(found)
	return "", fmt.Errorf("rke2: %s has %s on the VIP's subnet and the document does not say which to use; "+
		"set topology.vip.interface, or the VIP may come up on the wrong network",
		runner.Host(), strings.Join(found, " and "))
}

// VIPSteps deploy kube-vip and wait for the address to answer.
//
// Empty when the document has no VIP: a single-node homelab that joins nothing
// needs no floating address, and deploying one anyway would be a component
// somebody has to keep alive for no reason.
func VIPSteps(runner exec.Runner, spec v1alpha1.ClusterSpec, iface string, o Options) []engine.Step {
	v := spec.Topology.VIP
	if v == nil || strings.TrimSpace(v.Address) == "" {
		return nil
	}
	if v.Mode == "bgp" {
		// BGP needs the customer's routers configured to peer, which is not
		// something this tool can do or verify from here.
		return nil
	}

	host := runner.Host()
	add := func(s *engine.ShellStep) engine.Step {
		s.Phase, s.Runner, s.Host = PhaseBootstrap, runner, host
		return s
	}
	body := KubeVIPManifestBody(spec, iface)

	return []engine.Step{
		add(manifestStep(body)),
		add(vipAnswersStep(strings.TrimSpace(v.Address), o)),
	}
}

// manifestStep writes the DaemonSet into the directory RKE2 watches.
func manifestStep(body string) *engine.ShellStep {
	return &engine.ShellStep{
		Name: "kube-vip-manifest",
		Check: fmt.Sprintf(`[ -f %s ] || { echo "%s does not exist"; exit 1; }
printf '%%s' %s | cmp -s - %s || { echo "%s differs from the document"; exit 1; }
echo "%s matches the document"`,
			KubeVIPManifest, KubeVIPManifest, shellQuote(body), KubeVIPManifest,
			KubeVIPManifest, KubeVIPManifest),
		Do: fmt.Sprintf(`set -e
install -d -m 0755 %s
printf '%%s' %s > %s`, ManifestDir, shellQuote(body), KubeVIPManifest),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// vipAnswersStep waits until the VIP serves the API.
//
// A DaemonSet that exists is not an address that answers. What the join
// actually needs is a TLS listener on 6443 at the VIP, and that is what is
// waited for -- anything less would let the next node try to join something
// that is not there yet.
func vipAnswersStep(vip string, o Options) *engine.ShellStep {
	probe := fmt.Sprintf(`timeout 3 bash -c '</dev/tcp/%s/6443'`, vip)
	return &engine.ShellStep{
		Name: "vip",
		Check: fmt.Sprintf(`%s 2>/dev/null || { echo "nothing answers on %s:6443 yet"; exit 1; }
echo "the VIP %s answers on 6443"`, probe, vip, vip),
		Do: fmt.Sprintf(`export PATH=$PATH:%s
export KUBECONFIG=%s
deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  if %s 2>/dev/null; then exit 0; fi
  sleep 5
done
echo "the VIP %s never answered on 6443. kube-vip reports:"
kubectl -n kube-system get pods -l app.kubernetes.io/name=kube-vip -o wide 2>&1 | tail -10
kubectl -n kube-system logs -l app.kubernetes.io/name=kube-vip --tail=30 2>&1 | tail -30
exit 1`, BinDir, Kubeconfig, int(o.readyTimeout().Seconds()), probe, vip),
		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: o.readyTimeout() + time.Minute,
	}
}

// KubeVIPManifestBody renders the RBAC and DaemonSet.
//
// Written out rather than templated from a chart: it is one DaemonSet, an
// airgap has to carry the image anyway, and a Helm dependency here would mean
// the control plane's own address depended on a chart repository being
// reachable at install time.
func KubeVIPManifestBody(spec v1alpha1.ClusterSpec, iface string) string {
	v := spec.Topology.VIP
	vip := ""
	if v != nil {
		vip = strings.TrimSpace(v.Address)
	}

	// svc_enable is off: handing out addresses to Services is the dataplane's
	// job, and ADR-004 binds that to the CNI choice. Two components allocating
	// from the same pool is a conflict nobody can debug from the symptom.
	env := [][2]string{
		{"vip_arp", "true"},
		{"port", "6443"},
		{"vip_interface", iface},
		{"vip_cidr", "32"},
		{"cp_enable", "true"},
		{"cp_namespace", "kube-system"},
		{"svc_enable", "false"},
		{"vip_leaderelection", "true"},
		{"vip_leaseduration", "15"},
		{"vip_renewdeadline", "10"},
		{"vip_retryperiod", "2"},
		{"address", vip},
	}

	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: kube-vip
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: malmok:kube-vip
rules:
  - apiGroups: [""]
    resources: ["services", "services/status", "nodes", "endpoints"]
    verbs: ["list", "get", "watch", "update"]
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["list", "get", "watch", "update", "create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: malmok:kube-vip
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: malmok:kube-vip
subjects:
  - kind: ServiceAccount
    name: kube-vip
    namespace: kube-system
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: kube-vip
  namespace: kube-system
  labels:
    app.kubernetes.io/name: kube-vip
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: kube-vip
  template:
    metadata:
      labels:
        app.kubernetes.io/name: kube-vip
    spec:
      serviceAccountName: kube-vip
      hostNetwork: true
      dnsPolicy: ClusterFirstWithHostNet
      # The VIP belongs to the control plane, so it lives only where the API
      # server does. A worker holding it would answer for a service it does
      # not run.
      nodeSelector:
        node-role.kubernetes.io/control-plane: "true"
      tolerations:
        - key: node-role.kubernetes.io/control-plane
          operator: Exists
          effect: NoSchedule
        - key: node-role.kubernetes.io/etcd
          operator: Exists
          effect: NoExecute
        - key: node.kubernetes.io/not-ready
          operator: Exists
          effect: NoSchedule
      containers:
        - name: kube-vip
          image: ` + KubeVIPImage + `
          imagePullPolicy: IfNotPresent
          args: ["manager"]
          securityContext:
            capabilities:
              add: ["NET_ADMIN", "NET_RAW"]
          env:
`)
	for _, e := range env {
		b.WriteString("            - name: " + e[0] + "\n")
		b.WriteString("              value: " + yamlString(e[1]) + "\n")
	}
	return b.String()
}
