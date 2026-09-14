// Package expiry measures when the certificates a node already runs on stop
// working.
//
// Nothing in this tool looked at them until now. Preflight gates the
// certificate material an operator supplies (PF-9xx) and the wire checks read
// what a listener serves (PV-xxx); both happen around an install. The files
// RKE2 writes on the node afterwards -- the ones that take the API server down
// a year later with no warning -- were measured by nobody, and an operator
// handed a finished cluster had no way to ask when they end.
//
// Two things this deliberately does not do.
//
// It reads certificates and only certificates. The same directory holds the
// cluster's CA private keys. A tool with no reason to open them should not
// carry the code that could, so the glob names *.crt, the key files are never
// listed, and nothing here has a path that reaches one.
//
// It records absolute expiry times, not days remaining. A report handed to a
// customer is read weeks after it was written, and "expires in 30 days" stops
// being true the day after. How long is left is computed where it is printed.
package expiry

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/ryxenix/malmok/internal/cert"
	"github.com/ryxenix/malmok/internal/exec"
	"github.com/ryxenix/malmok/internal/rke2"
)

// Dir is where RKE2 keeps the certificates a server runs on.
const Dir = rke2.DataDir + "/server/tls"

// Row is one certificate found on one node.
//
// A single file can hold more than one certificate, so a row is a certificate
// and not a file; Path says which file it came out of.
type Row struct {
	// Node is the address the runner talked to.
	Node string `json:"node"`
	// Path is the file the certificate was read from.
	Path string `json:"path"`
	// Subject is the certificate's subject DN, as the one thing that
	// distinguishes two certificates in the same file.
	Subject string `json:"subject"`
	// NotAfter is the moment it stops being valid. This is the measurement;
	// everything else about time is derived from it.
	NotAfter time.Time `json:"notAfter"`
	// Kind is leaf, intermediate or root, as classified by internal/cert.
	Kind cert.Kind `json:"kind"`
}

// Remaining is how long this certificate has left at the given moment.
// Negative once it has expired, which is the honest answer and not an error.
func (r Row) Remaining(now time.Time) time.Duration { return r.NotAfter.Sub(now) }

// Reason is why a certificate that might exist did not become a row.
type Reason string

const (
	// ReasonUnreachable is the node not answering at all.
	ReasonUnreachable Reason = "unreachable"
	// ReasonDirMissing is no TLS directory, which on an agent is the correct
	// and expected answer rather than a fault.
	ReasonDirMissing Reason = "dir-missing"
	// ReasonNoPrivilege is the directory being there and unreadable, which
	// says nothing about what is in it.
	//
	// Measured on a live node: /var/lib/rancher/rke2/server/tls is mode 0700
	// and owned by root, while every directory above it is 0755. So an
	// unprivileged shell gets TRUE from `test -d` -- stat only needs the
	// parents -- and then FALSE from `test -r`. The glob inside it expands to
	// nothing, every iteration is skipped, and the command exits zero having
	// printed not one line. Read as an answer, that is "this server holds no
	// certificates": a clean bill of health for a node nothing was read from.
	ReasonNoPrivilege Reason = "no-privilege"
	// ReasonEmpty is a readable server TLS directory holding no certificate.
	//
	// A real measurement rather than a failed one, and still not a healthy
	// answer: a running server always has these files, so finding none means
	// something is wrong with the node rather than with the scan. It is kept
	// separate from silence for the same reason everything else here is.
	ReasonEmpty Reason = "empty"
	// ReasonUnreadable is a file that exists and could not be read, which is
	// nearly always the command not running with enough privilege.
	ReasonUnreadable Reason = "unreadable"
	// ReasonNotPEM is a .crt holding no certificate this tool could parse.
	ReasonNotPEM Reason = "not-pem"
)

// Problem is something the scan could not turn into a row.
//
// Kept apart from the rows on purpose. A measurement that did not happen must
// never render as a measurement that came back fine, which is the single way
// an inventory of expiry dates misleads the person relying on it.
type Problem struct {
	Node string `json:"node"`
	// Path is empty when what failed was the node or the directory rather
	// than one file.
	Path   string `json:"path,omitempty"`
	Reason Reason `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Inventory is one node's answer.
type Inventory struct {
	Node string
	// Rows are sorted soonest-expiring first, which is the order the question
	// is asked in.
	Rows []Row
	// Problems is everything that could not be measured.
	Problems []Problem
}

// Markers separate the files inside one command's output. PEM is base64 and
// BEGIN/END lines, so a line starting with "===" cannot be part of a
// certificate and cannot be forged by one.
const (
	markerNoDir      = "===NODIR"
	markerNoAccess   = "===NOACCESS"
	markerEmpty      = "===EMPTY"
	markerFile       = "===FILE "
	markerUnreadable = "===UNREADABLE"
)

// script lists and reads every certificate in one round trip.
//
// One command rather than one per file: a server's TLS directory holds around
// twenty certificates, and twenty SSH round trips to read twenty small files
// is a second of waiting bought for nothing.
//
// The glob names *.crt and nothing else. That narrowness is the whole
// protection against reading a CA private key that sits in the same directory,
// so it is not a detail to relax for convenience later.
//
// One level down as well as the top, because RKE2 keeps whole certificate
// families in subdirectories and a top-level glob misses them silently.
// Measured on a live server: thirteen certificates at the top, twenty in the
// tree -- the seven missing were etcd's five, the controller-manager's and the
// scheduler's. etcd's are the ones whose expiry stops a cluster hardest, and
// nothing was looking at them. Two levels is what the tree actually has; the
// globs are written out rather than replaced with find so that a path
// containing a space cannot be split into two.
//
// temporary-certs is skipped. It is RKE2's scratch area during rotation --
// empty on a settled node -- and a half-written copy reported beside the real
// certificate is one expiry date shown twice.
// An absent directory and a directory that cannot be traversed answer `test
// -d` identically, so the shell is asked which one it is rather than left to
// imply it. Root and no directory is a real absence; not root and no
// directory is not an answer at all.
// Each question is asked of the shell rather than inferred from the answer to
// a different one. Existence, readability and emptiness are three facts, and
// every pair of them is indistinguishable from the outside if only one is
// asked: a glob in an unreadable directory expands to nothing and exits zero,
// which is byte for byte what an empty directory produces.
const script = `d=` + Dir + `
if [ ! -d "$d" ]; then
  if [ "$(id -u)" = 0 ]; then echo '` + markerNoDir + `'; else echo '` + markerNoAccess + `'; fi
  exit 0
fi
if [ ! -r "$d" ] || [ ! -x "$d" ]; then echo '` + markerNoAccess + `'; exit 0; fi
n=0
for f in "$d"/*.crt "$d"/*/*.crt; do
  [ -e "$f" ] || continue
  case "$f" in */temporary-certs/*) continue ;; esac
  n=$((n+1))
  echo "` + markerFile + `$f"
  cat "$f" 2>/dev/null || echo '` + markerUnreadable + `'
done
[ "$n" -gt 0 ] || echo '` + markerEmpty + `'`

// Scan measures one node.
//
// The runner must already be able to read root-owned files: RKE2's TLS
// directory is mode 0700 and owned by root, so an unelevated runner reports
// every file unreadable rather than reporting nothing. Elevation is the
// caller's to arrange, the same way the preflight probes arrange it.
//
// It returns an Inventory rather than an error. A node that cannot be reached
// and a node with no certificates are different answers, and both are answers;
// collapsing either into an error loses which one happened.
func Scan(ctx context.Context, r exec.Runner) Inventory {
	inv := Inventory{Node: r.Host()}

	res, err := r.Run(ctx, script)
	if err != nil {
		inv.Problems = append(inv.Problems, Problem{
			Node: inv.Node, Reason: ReasonUnreachable, Detail: err.Error(),
		})
		return inv
	}
	if !res.OK() {
		detail := res.Err()
		if detail == "" {
			detail = "the command listing the certificate directory failed"
		}
		inv.Problems = append(inv.Problems, Problem{
			Node: inv.Node, Reason: ReasonUnreadable, Detail: detail,
		})
		return inv
	}

	inv.Rows, inv.Problems = parse(inv.Node, res.Stdout)

	// A scan that produced neither a certificate nor a reason is the failure
	// this package exists to prevent, and it has happened: an unprivileged
	// glob returned nothing and the empty result read as a healthy cluster.
	// The markers above now cover every route the script can take, so this
	// says only that something unforeseen did -- without claiming to know
	// what, and without letting the answer be silence.
	if len(inv.Rows) == 0 && len(inv.Problems) == 0 {
		inv.Problems = append(inv.Problems, Problem{
			Node: inv.Node, Reason: ReasonUnreadable,
			Detail: "the scan returned no output and no reason, so nothing was established",
		})
	}
	sort.Slice(inv.Rows, func(i, j int) bool {
		if !inv.Rows[i].NotAfter.Equal(inv.Rows[j].NotAfter) {
			return inv.Rows[i].NotAfter.Before(inv.Rows[j].NotAfter)
		}
		if inv.Rows[i].Path != inv.Rows[j].Path {
			return inv.Rows[i].Path < inv.Rows[j].Path
		}
		return inv.Rows[i].Subject < inv.Rows[j].Subject
	})
	return inv
}

// parse turns the script's output into rows and problems.
//
// Split out from Scan so the shape of the output is testable without a node,
// and so a malformed answer from one file cannot cost the others: each file is
// finished and recorded before the next begins.
func parse(node, out string) ([]Row, []Problem) {
	var rows []Row
	var problems []Problem

	// Checked before the absence marker, because only one of the two is an
	// answer: an unprivileged shell that cannot see the directory has not
	// established that there is nothing there.
	if strings.HasPrefix(strings.TrimSpace(out), markerNoAccess) {
		return nil, []Problem{{
			Node: node, Reason: ReasonNoPrivilege,
			Detail: Dir + " is not visible to this account, which runs unprivileged",
		}}
	}
	if strings.HasPrefix(strings.TrimSpace(out), markerNoDir) {
		return nil, []Problem{{
			Node: node, Reason: ReasonDirMissing,
			Detail: Dir + " does not exist, so this node runs no RKE2 server",
		}}
	}
	if strings.HasPrefix(strings.TrimSpace(out), markerEmpty) {
		return nil, []Problem{{
			Node: node, Reason: ReasonEmpty,
			Detail: Dir + " is readable and holds no certificate",
		}}
	}

	var path string
	var buf strings.Builder

	flush := func() {
		if path == "" {
			return
		}
		data := buf.String()
		buf.Reset()
		name := path
		path = ""

		if strings.Contains(data, markerUnreadable) {
			problems = append(problems, Problem{
				Node: node, Path: name, Reason: ReasonUnreadable,
				Detail: "the file exists and could not be read",
			})
			return
		}

		// The existing parser, rather than a second one. A package that
		// classifies certificates already lives in this repository, and two
		// implementations of "what is in this PEM" diverge.
		found := cert.Scan([]cert.File{{Name: name, Data: []byte(data)}}, nil).Certs()
		if len(found) == 0 {
			problems = append(problems, Problem{
				Node: node, Path: name, Reason: ReasonNotPEM,
				Detail: "the file holds no certificate this tool could read",
			})
			return
		}
		for _, c := range found {
			rows = append(rows, Row{
				Node:     node,
				Path:     name,
				Subject:  c.SubjectLine(),
				NotAfter: c.NotAfter,
				Kind:     c.Kind,
			})
		}
	}

	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, markerFile) {
			flush()
			path = strings.TrimSpace(strings.TrimPrefix(line, markerFile))
			continue
		}
		// Anything before the first marker is not part of a file and is
		// dropped rather than attributed to one.
		if path != "" {
			buf.WriteString(line)
			buf.WriteString("\n")
		}
	}
	flush()

	return rows, problems
}
