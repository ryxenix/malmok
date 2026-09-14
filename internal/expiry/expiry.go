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
	Node string
	// Path is the file the certificate was read from.
	Path string
	// Subject is the certificate's subject DN, as the one thing that
	// distinguishes two certificates in the same file.
	Subject string
	// NotAfter is the moment it stops being valid. This is the measurement;
	// everything else about time is derived from it.
	NotAfter time.Time
	// Kind is leaf, intermediate or root, as classified by internal/cert.
	Kind cert.Kind
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
	Node string
	// Path is empty when what failed was the node or the directory rather
	// than one file.
	Path   string
	Reason Reason
	Detail string
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
const script = `d=` + Dir + `
if [ ! -d "$d" ]; then echo '` + markerNoDir + `'; exit 0; fi
for f in "$d"/*.crt; do
  [ -e "$f" ] || continue
  echo "` + markerFile + `$f"
  cat "$f" 2>/dev/null || echo '` + markerUnreadable + `'
done`

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

	if strings.HasPrefix(strings.TrimSpace(out), markerNoDir) {
		return nil, []Problem{{
			Node: node, Reason: ReasonDirMissing,
			Detail: Dir + " does not exist, so this node runs no RKE2 server",
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
