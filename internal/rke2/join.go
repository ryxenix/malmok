package rke2

import (
	"context"
	"fmt"
	"strings"
	"time"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/engine"
	"platform.ryxen.dev/malmok/internal/exec"
)

// Joining phases. Both are traversed one node at a time (docs/11-execute.md
// §2.1), for the same reason the prohibitions forbid restarting every node at
// once: a control plane that loses quorum while two members are joining is a
// restore, not a retry.

const (
	// PhaseJoinServer adds another control-plane node.
	PhaseJoinServer = "l1-join-server"
	// PhaseJoinAgent adds a worker.
	PhaseJoinAgent = "l1-join-agent"
)

// SupervisorPort is where RKE2 listens for joins. It is not a Kubernetes port,
// which is why a firewall rule set written from a Kubernetes reference leaves
// it closed (PF-601).
const SupervisorPort = 9345

// ReadToken reads the cluster token from a server node.
//
// The token is not in the document by design: cluster.yaml is an audit artifact
// handed to customers, and a plaintext credential in one is what the schema's
// SourceRef indirection exists to prevent. RKE2 generates the token on the
// first server, so that node is the source of truth and this is where the join
// phases get it from.
func ReadToken(ctx context.Context, server exec.Runner) (string, error) {
	res, err := server.Run(ctx, "cat "+TokenFile)
	if err != nil {
		return "", fmt.Errorf("rke2: read the cluster token from %s: %w", server.Host(), err)
	}
	if !res.OK() {
		return "", fmt.Errorf("rke2: %s has no cluster token at %s (exit %d): %s; "+
			"the first server writes it once it has started",
			server.Host(), TokenFile, res.ExitCode, engine.CleanForEvent(res.Err()))
	}
	token := strings.TrimSpace(res.Stdout)
	if token == "" {
		return "", fmt.Errorf("rke2: the cluster token at %s on %s is empty", TokenFile, server.Host())
	}
	return token, nil
}

// JoinSteps adds one node to an existing cluster.
//
// control is a runner for a node that is already in the cluster. It is needed
// for the last step and only for it: an agent has no kubeconfig, so whether it
// joined can only be answered by the control plane. Asking the agent whether
// its own kubelet is running answers a different and much weaker question.
func JoinSteps(node exec.Runner, control exec.Runner, spec v1alpha1.ClusterSpec,
	target v1alpha1.NodeSpec, token string, o Options) []engine.Step {

	phase, kind, unit := PhaseJoinAgent, "agent", "rke2-agent"
	if target.Role == v1alpha1.RoleServer {
		phase, kind, unit = PhaseJoinServer, "server", "rke2-server"
	}

	host := target.Host
	add := func(s *engine.ShellStep, runner exec.Runner) engine.Step {
		s.Phase, s.Runner, s.Host = phase, runner, host
		return s
	}

	var config string
	if kind == "server" {
		config = ServerConfig(target, spec, token)
		config = withServerURL(config, spec)
	} else {
		config = AgentConfig(target, spec, token)
	}

	steps := []engine.Step{
		add(installStep(spec.Kubernetes.Version, kind, o), node),
		add(configStep(config), node),
		add(unitStep(unit), node),
		add(registeredStep(target, o), control),
	}
	if kind == "server" {
		// Every server holds a kubeconfig, and the operator may sit at any of
		// them. An agent has none to copy.
		steps = append(steps, add(kubeconfigStep(target.SSH.User), node))
	}
	return steps
}

// unitStep starts the unit without waiting for the cluster to agree.
//
// Split from readiness on purpose: a unit that will not start is a different
// problem from a node the control plane has not accepted, and reporting the
// two as one failure sends people to the wrong logs.
func unitStep(unit string) *engine.ShellStep {
	return &engine.ShellStep{
		Name: "service",
		// The same stale-configuration test the first server gets. A joining
		// node whose config.yaml changed and whose unit was never restarted
		// keeps running the old settings, and every step still reports success
		// -- which is how one node ends up running kube-proxy while the rest
		// have replaced it.
		Check: fmt.Sprintf(`systemctl is-active --quiet %s || { echo "%s is not running"; exit 1; }
%s
echo "%s is running"`, unit, unit, configIsOlderThanProcess(unit), unit),
		Do: fmt.Sprintf(`set -e
if systemctl is-active --quiet %s; then
  systemctl restart %s
else
  systemctl enable --now %s
fi
for i in $(seq 1 30); do
  systemctl is-active --quiet %s && exit 0
  sleep 2
done
echo "%s did not start:"; journalctl -u %s -n 30 --no-pager 2>&1 | tail -30
exit 1`, unit, unit, unit, unit, unit, unit),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// registeredStep waits for the control plane to report the node Ready.
//
// It runs on a node that is already in the cluster, not on the one joining: an
// agent has no kubeconfig and cannot answer this about itself.
//
// The node is matched by its address rather than its name. A hostname depends
// on what the node happens to call itself and on whether node-name was set,
// while the address is what the document says and what the operator typed.
func registeredStep(target v1alpha1.NodeSpec, o Options) *engine.ShellStep {
	addr := target.NodeIP
	if addr == "" {
		addr = target.Host
	}

	query := fmt.Sprintf(`export PATH=$PATH:%s
export KUBECONFIG=%s
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{" "}{range .status.addresses[?(@.type=="InternalIP")]}{.address}{end}{" "}{range .status.conditions[?(@.type=="Ready")]}{.status}{end}{"\n"}{end}' 2>/dev/null`,
		BinDir, Kubeconfig)

	return &engine.ShellStep{
		Name: "registered",
		Check: fmt.Sprintf(`line=$(%s | grep ' %s ' || true)
[ -n "$line" ] || { echo "%s has not registered with the cluster"; exit 1; }
case "$line" in
  *" True") echo "$line" ;;
  *) echo "registered but not Ready: $line"; exit 1 ;;
esac`, query, addr, addr),

		Do: fmt.Sprintf(`deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  line=$(%s | grep ' %s ' || true)
  case "$line" in *" True") exit 0 ;; esac
  sleep 5
done
echo "%s did not become Ready within %ds; the cluster currently reports:"
%s
exit 1`, int(o.readyTimeout().Seconds()), query, addr, addr, int(o.readyTimeout().Seconds()), query),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: o.readyTimeout() + time.Minute,
	}
}

// AgentConfig renders config.yaml for a worker.
//
// An agent runs no API server, so it has neither tls-san nor a disable list;
// what it needs is where to join, the token to join with, and which address to
// advertise.
func AgentConfig(node v1alpha1.NodeSpec, spec v1alpha1.ClusterSpec, token string) string {
	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString("server: " + yamlString(ServerURL(spec)) + "\n")
	b.WriteString("token: " + yamlString(token) + "\n")

	if ip := strings.TrimSpace(node.NodeIP); ip != "" {
		b.WriteString("node-ip: " + yamlString(ip) + "\n")
	}
	if h := strings.TrimSpace(node.Hostname); h != "" {
		b.WriteString("node-name: " + yamlString(h) + "\n")
	}
	// kube-proxy runs on workers too, so the replacement has to be consistent
	// across the cluster: one node still running it programs service rules the
	// others do not have.
	if DisablesKubeProxy(spec) {
		b.WriteString("disable-kube-proxy: true\n")
	}
	if r := strings.TrimSpace(spec.Registry.SystemDefaultRegistry); r != "" &&
		spec.Registry.Mode != v1alpha1.RegistryEmbedded {
		b.WriteString("system-default-registry: " + yamlString(r) + "\n")
	}
	writeList(&b, "node-label", labelArgs(node))
	writeList(&b, "node-taint", node.Taints)
	writeList(&b, "kubelet-arg", spec.Kubernetes.KubeletArgs)
	return b.String()
}

// ServerURL is what a joining node points at.
//
// Always the registration address, never a node's own (ADR-008). A node that
// joined through a peer's address has that peer baked into its configuration,
// and removing that peer later means re-joining every node that used it.
func ServerURL(spec v1alpha1.ClusterSpec) string {
	return fmt.Sprintf("https://%s:%d", strings.TrimSpace(spec.Topology.RegistrationAddress), SupervisorPort)
}

// withServerURL adds the join target to a server's configuration.
//
// The first server has no server: line -- it is the one being joined -- and
// every server after it does.
func withServerURL(config string, spec v1alpha1.ClusterSpec) string {
	lines := strings.SplitN(config, "\n", 2)
	rest := ""
	if len(lines) > 1 {
		rest = lines[1]
	}
	return lines[0] + "\nserver: " + yamlString(ServerURL(spec)) + "\n" + rest
}

// labelArgs renders a node's labels as the key=value list RKE2 expects.
func labelArgs(node v1alpha1.NodeSpec) []string {
	if len(node.Labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(node.Labels))
	for k := range node.Labels {
		keys = append(keys, k)
	}
	sortStrings(keys)

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+node.Labels[k])
	}
	return out
}

// sortStrings is an insertion sort, which is the right shape for the handful of
// labels a node carries and avoids pulling in sort for it.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
