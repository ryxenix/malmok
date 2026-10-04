package upgrade

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/exec"
	"github.com/ryxenix/malmok/internal/preflight"
	"github.com/ryxenix/malmok/internal/rke2"
)

// ---------------------------------------------------------------------------
// Measurement
// ---------------------------------------------------------------------------

// Read asks the cluster what each node is running and each node what it has.
//
// Two sources, because they answer two questions, and getting them the wrong
// way round is a mistake this made once. `rke2 --version` is the binary on
// disk: what the node will run after a restart. The kubelet version the API
// server reports is what the node is running now. A half-finished upgrade --
// new binary installed, service not yet restarted -- is the case where they
// disagree, and reading the disk as though it were the running version made the
// tool refuse to finish the very upgrade it had started.
//
// So the skew rules are about the running version, and the installed version is
// carried alongside to explain a node that is between the two.
func Read(ctx context.Context, s v1alpha1.ClusterSpec,
	byHost map[string]exec.Runner, control exec.Runner) State {

	st := State{
		Now:          time.Now(),
		ArtifactPath: strings.TrimSpace(s.Kubernetes.ArtifactPath),
		Airgap:       s.Network.Mode == v1alpha1.NetworkAirgap,
		CNIArchive:   preflight.CNIArchive(s.Kubernetes.Dataplane.Preset),
	}
	cluster := clusterNodes(ctx, control)

	for _, group := range []struct {
		nodes []v1alpha1.NodeSpec
		agent bool
	}{
		{s.Topology.Servers, false},
		{s.Topology.Agents, true},
	} {
		for _, n := range group.nodes {
			node := NodeState{Host: n.Host, Agent: group.agent}

			addr := n.NodeIP
			if addr == "" {
				addr = n.Host
			}
			if seen, ok := cluster[addr]; ok {
				node.Ready = seen.ready
				node.Unschedulable = seen.unschedulable
				node.Evidence = seen.kubelet
				if v, err := ParseVersion(seen.kubelet); err == nil {
					node.Version = v
				}
			}

			if r := byHost[n.Host]; r != nil {
				if res, err := r.Run(ctx, "rke2 --version 2>/dev/null | head -1"); err == nil && res.OK() {
					if fields := strings.Fields(strings.TrimSpace(res.Out())); len(fields) >= 3 {
						if v, err := ParseVersion(fields[2]); err == nil {
							node.Installed = v
						}
					}
				}
			}
			if st.ArtifactPath != "" {
				node.Artifacts = readArtifacts(ctx, byHost[n.Host], st.ArtifactPath)
			}
			st.Nodes = append(st.Nodes, node)
		}
	}

	st.SnapshotDir = SnapshotDir(s)
	st.LastSnapshot, st.SnapshotProblem = newestSnapshot(ctx, control, st.SnapshotDir)
	return st
}

// clusterView is what the control plane says about one node.
type clusterView struct {
	ready         bool
	kubelet       string
	unschedulable bool
}

// clusterNodes maps a node's address to what the cluster reports about it.
func clusterNodes(ctx context.Context, control exec.Runner) map[string]clusterView {
	out := map[string]clusterView{}
	if control == nil {
		return out
	}
	res, err := control.Run(ctx, rke2.Kubectl+
		`kubectl get nodes -o jsonpath='{range .items[*]}`+
		`{range .status.addresses[?(@.type=="InternalIP")]}{.address}{end}{" "}`+
		`{range .status.conditions[?(@.type=="Ready")]}{.status}{end}{" "}`+
		`{.status.nodeInfo.kubeletVersion}{" "}{.spec.unschedulable}{"\n"}{end}' 2>/dev/null`)
	if err != nil || !res.OK() {
		return out
	}
	for _, line := range strings.Split(res.Out(), "\n") {
		// The fourth field is absent on a node that was never cordoned:
		// the API leaves spec.unschedulable out rather than writing false.
		if f := strings.Fields(line); len(f) == 3 || len(f) == 4 {
			out[f[0]] = clusterView{ready: f[1] == "True", kubelet: f[2],
				unschedulable: len(f) == 4 && f[3] == "true"}
		}
	}
	return out
}

// DefaultSnapshotDir is where RKE2 keeps etcd snapshots unless told otherwise.
const DefaultSnapshotDir = "/var/lib/rancher/rke2/server/db/snapshots"

// SnapshotDir is where the document's servers keep their etcd snapshots.
func SnapshotDir(s v1alpha1.ClusterSpec) string {
	if d := strings.TrimSpace(s.Kubernetes.Etcd.SnapshotTarget); d != "" {
		return d
	}
	return DefaultSnapshotDir
}

// newestSnapshot is the time of the most recent etcd snapshot in dir on a
// server, or why there is none to report.
//
// Read from disk rather than from the API, because a snapshot that exists only
// as a CR is not one anybody can restore from. It used to read one fixed
// directory and fold every failure into "no snapshot": a cluster snapshotting
// into the directory its document named was told it had no way back, and a
// server that could not be asked looked like one that had never snapshotted.
// The problem is "" when the answer is a real one -- a time, or an empty
// directory.
//
// Only root can say a directory is absent. RKE2's server directory is root's
// alone, so to anyone else its snapshot directory is invisible whether or not
// it exists: run as an ordinary account on the lab, the first version of this
// reported "does not exist" for a directory holding a snapshot taken that
// morning. The certificate scan learned the same thing the same way.
func newestSnapshot(ctx context.Context, control exec.Runner, dir string) (time.Time, string) {
	if control == nil {
		return time.Time{}, "there is no connection to a server"
	}
	res, err := control.Run(ctx, `d=`+rke2.ShellQuote(dir)+`
if [ ! -d "$d" ]; then
  if [ "$(id -u)" = 0 ]; then echo '===NODIR'; else echo '===NOACCESS'; fi
  exit 0
fi
[ -r "$d" ] && [ -x "$d" ] || { echo '===NOACCESS'; exit 0; }
n=$(find "$d" -maxdepth 1 -type f -printf '%T@ %f\n' | sort -n | tail -1)
[ -n "$n" ] || { echo '===EMPTY'; exit 0; }
echo "===NEWEST ${n%%.*} ${n#* }"`)
	if err != nil {
		return time.Time{}, "the server could not be asked: " + err.Error()
	}
	out := strings.TrimSpace(res.Stdout)
	switch {
	case out == "===NODIR":
		return time.Time{}, dir + " does not exist on the server, so no snapshot has been written there"
	case out == "===NOACCESS":
		return time.Time{}, dir + " could not be read by the account Malmok connects as"
	case out == "===EMPTY":
		return time.Time{}, ""
	case strings.HasPrefix(out, "===NEWEST "):
		var unix int64
		if _, err := fmt.Sscanf(strings.TrimPrefix(out, "===NEWEST "), "%d", &unix); err == nil && unix > 0 {
			return time.Unix(unix, 0), ""
		}
	}
	return time.Time{}, fmt.Sprintf("the server's answer was not understood (exit %d): %q %s",
		res.ExitCode, out, res.Err())
}

// Markers the artifact script prints, so a line of ls output can never be
// mistaken for an answer.
const (
	artMissing = "===MISSING"
	artArch    = "===ARCH "
	artFiles   = "===FILES "
	artVersion = "===VERSION "
	artNoRun   = "===NORUN "
)

// artifactScript reads one node's artifact directory without changing it.
//
// The version is read by running the rke2 binary out of the node's own tarball
// from a temporary directory that is removed afterwards. RKE2's artifact
// names carry no version, and the binary's answer is the same observable the
// install step converges on.
//
// The directory goes under /var/lib/rancher/rke2 when it can. A hardened node
// mounts /tmp noexec -- the CIS benchmarks ask for it -- and the binary then
// cannot be run to be asked, which would block every upgrade on exactly the
// sites most careful about them. RKE2 runs its own binaries from under that
// directory, so it is not mounted noexec. /tmp is the fallback when the
// runner cannot write there; the first mktemp's error is expected then, and a
// failure of both is reported.
func artifactScript(path string) string {
	return `p=` + rke2.ShellQuote(path) + `
[ -d "$p" ] || { echo '` + artMissing + `'; exit 0; }
[ -r "$p" ] && [ -x "$p" ] || { echo '` + artNoRun + `the directory cannot be read by this user'; exit 0; }
case "$(uname -m)" in x86_64|amd64) a=amd64 ;; aarch64|arm64) a=arm64 ;; *) a=$(uname -m) ;; esac
echo "` + artArch + `$a"
echo "` + artFiles + `$(ls -1 "$p" | tr '\n' ' ')"
t="$p/rke2.linux-$a.tar.gz"
[ -r "$t" ] || exit 0
l=$(tar -tzf "$t" 2>&1) || { echo "` + artNoRun + `the tarball cannot be listed: $(printf '%s' "$l" | tail -1)"; exit 0; }
m=$(printf '%s\n' "$l" | grep -E '(^|/)bin/rke2$' | head -1)
[ -n "$m" ] || { echo '` + artNoRun + `the tarball holds no bin/rke2'; exit 0; }
d=$(mktemp -d -p /var/lib/rancher/rke2 .malmok-artifact.XXXXXX 2>/dev/null || mktemp -d) || { echo '` + artNoRun + `no temporary directory'; exit 0; }
if tar -xzf "$t" -C "$d" "$m" 2>"$d/err"; then
  echo "` + artVersion + `$("$d/$m" --version 2>&1 | head -1)"
else
  echo "` + artNoRun + `the binary cannot be extracted: $(head -1 "$d/err")"
fi
rm -rf "$d"`
}

// readArtifacts asks one node what it holds at the artifact path.
func readArtifacts(ctx context.Context, r exec.Runner, path string) Artifacts {
	if r == nil {
		return Artifacts{Problem: "there is no connection to the node"}
	}
	res, err := r.Run(ctx, artifactScript(path))
	if err != nil {
		return Artifacts{Problem: err.Error()}
	}
	a := Artifacts{Measured: true}
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == artMissing:
			a.Missing = true
		case strings.HasPrefix(line, artArch):
			a.Arch = strings.TrimSpace(strings.TrimPrefix(line, artArch))
		case strings.HasPrefix(line, artFiles):
			a.Files = strings.Fields(strings.TrimPrefix(line, artFiles))
		case strings.HasPrefix(line, artVersion):
			said := strings.TrimSpace(strings.TrimPrefix(line, artVersion))
			if f := strings.Fields(said); len(f) >= 3 {
				if v, err := ParseVersion(f[2]); err == nil {
					a.Version = v
					continue
				}
			}
			a.Problem = fmt.Sprintf("it answered %q", said)
		case strings.HasPrefix(line, artNoRun):
			a.Problem = strings.TrimSpace(strings.TrimPrefix(line, artNoRun))
		}
	}
	if !res.OK() && a.Problem == "" {
		a.Problem = fmt.Sprintf("the script exited %d: %s", res.ExitCode, res.Err())
	}
	return a
}
