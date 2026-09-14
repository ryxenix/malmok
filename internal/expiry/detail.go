package expiry

import (
	"fmt"
	"strings"
)

// How a finding and a problem say what they are, in the one line the event
// schema allows.
//
// The lines live here rather than in the command because they are the thing an
// audit report keeps: a renderer can be rewritten, but the sentence written
// into events.jsonl six months ago is what somebody reads when the cluster
// stops. Testing them where the model lives is the only way they stay honest.

// Alarming reports whether an alert step has been crossed.
//
// Not the same as "bad". A finding is produced for every certificate, and most
// of them are fine; this is what separates the ones an operator has to act on
// from the ones a handover document has to state.
func (f Finding) Alarming() bool { return f.Step != 0 }

// Line is the single English line carried into the event stream.
func (f Finding) Line() string {
	when := f.NotAfter.UTC().Format("2006-01-02")
	subject := oneLineASCII(f.Subject)

	switch f.Code {
	case "MC-112":
		return fmt.Sprintf(
			"%s is inside the %d-day renewal window and expires on %s; restarting the service rotates it",
			subject, RenewalWindowDays, when)
	case "MC-121":
		if f.Days < 0 {
			return fmt.Sprintf("the RKE2 CA %s expired on %s, %d days ago; the cluster needs rotate-ca",
				subject, when, -f.Days)
		}
		return fmt.Sprintf("the RKE2 CA %s expires on %s, in %d days; renewing it stops the whole cluster",
			subject, when, f.Days)
	default:
		if f.Days < 0 {
			return fmt.Sprintf("%s expired on %s, %d days ago", subject, when, -f.Days)
		}
		return fmt.Sprintf("%s expires on %s, in %d days", subject, when, f.Days)
	}
}

// Item is the maintenance item a problem stopped from being measured.
//
// A problem reports under the code it prevented rather than under one of its
// own. preflight established the shape: the check keeps its identity and says
// it could not be measured, because a reader looking for "what did MC-111 say
// about this node" must not find silence.
func (p Problem) Item() string { return "MC-111" }

// Applicable reports whether the item was ever going to produce an answer here.
//
// An agent has no server TLS directory. That is the correct state for that
// machine, and reporting it as a failed measurement would put a permanent red
// line in every report for a node that is behaving exactly as designed.
func (p Problem) Applicable() bool { return p.Reason != ReasonDirMissing }

// Line is the single English line explaining what was not measured.
//
// It reads the Detail field, which holds what the node or the registry
// actually said, and wraps it in a sentence naming what went unmeasured.
func (p Problem) Line() string {
	where := "the certificate directory"
	if p.Path != "" {
		where = oneLineASCII(p.Path)
	}

	switch p.Reason {
	case ReasonUnreachable:
		return "the node could not be reached, so no certificate on it was measured: " +
			oneLineASCII(p.Detail)
	case ReasonDirMissing:
		return "this node runs no RKE2 server, so it holds no server certificates"
	case ReasonNoPrivilege:
		return "nothing was measured on this node: " + oneLineASCII(p.Detail) +
			", and an account that cannot read the directory has not established that it is empty"
	case ReasonEmpty:
		return "nothing was measured on this node: " + oneLineASCII(p.Detail) +
			", and a running RKE2 server always has them, so the node needs looking at"
	case ReasonUnreadable:
		detail := oneLineASCII(p.Detail)
		if detail == "" {
			detail = "the command returned nothing"
		}
		return where + " could not be read, so its expiry is unknown: " + detail
	case ReasonNotPEM:
		return where + " holds no certificate this tool could read, so its expiry is unknown"
	}
	return where + " was not measured"
}

// oneLineASCII makes a string safe for the event schema.
//
// Detail has to be printable ASCII on one line, and a certificate subject is
// written by whoever issued the certificate: a private CA with a Korean
// organisation name in its DN is ordinary, and it would otherwise produce an
// event the reader rejects. A line displayed imperfectly beats a finding that
// never reaches the audit report.
func oneLineASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r > 0x7e:
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
