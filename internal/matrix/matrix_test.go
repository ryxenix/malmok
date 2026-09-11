package matrix

import (
	"strings"
	"testing"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/spec"
)

// Coverage is checkable without a single node, and this is what makes the
// matrix a contract rather than a habit: a dimension gains a value and this
// test fails until some case exercises it.
func TestEveryValueIsExercised(t *testing.T) {
	if missing := Uncovered(Cases()); len(missing) > 0 {
		t.Errorf("no case exercises: %s", strings.Join(missing, ", "))
	}
}

// The pairs are the combinations that have actually broken. Coverage of each
// value separately would not have caught any of them: one node passes, the
// Gateway API passes, and one node with the Gateway API deadlocked.
func TestEveryRequiredPairIsExercised(t *testing.T) {
	if missing := UncoveredPairs(Cases()); len(missing) > 0 {
		t.Errorf("no case combines: %s", strings.Join(missing, "; "))
	}
}

// A case whose document does not validate is a case that will fail on a node
// for a reason that has nothing to do with the node. The whole matrix is
// checked here, before anything is built.
func TestEveryCaseProducesAValidDocument(t *testing.T) {
	hosts := Hosts{
		Server: "192.168.88.241", Agent: "192.168.88.244", Third: "192.168.88.242",
		User: "k8s", PasswordRef: "env://NODE_PASSWORD",
	}
	material := Material{
		RootCert: "pki/root.crt", IntermediateCert: "pki/inter.crt",
		IntermediateKey: "pki/inter.key", LeafCert: "pki/leaf.crt", LeafKey: "pki/leaf.key",
	}

	for _, c := range Cases() {
		t.Run(c.Name, func(t *testing.T) {
			if c.Why == "" {
				t.Error("the case says nothing about what it is here to catch")
			}
			for _, grown := range []bool{false, true} {
				if grown && c.Op != OpGrow {
					continue
				}
				// The profile baseline first, exactly as the CLI does it:
				// a document is validated after inheritance, and validating
				// before it would demand fields the profile supplies.
				doc := &spec.Document{Spec: c.Document("v1.36.3+rke2r1", hosts, material, grown)}
				if _, err := doc.ApplyProfile(); err != nil {
					t.Fatalf("grown=%t: %v", grown, err)
				}
				if err := doc.Validate(false); err != nil {
					t.Errorf("grown=%t: %v", grown, err)
				}
			}
		})
	}
}

// The shapes each case promises, read back off the document it produces.
func TestDocumentsMatchTheirCase(t *testing.T) {
	hosts := Hosts{Server: "10.0.0.11", Agent: "10.0.0.12", Third: "10.0.0.13", User: "k8s", PasswordRef: "env://P"}
	for _, c := range Cases() {
		doc := c.Document("v1.36.3+rke2r1", hosts, Material{
			RootCert: "r", IntermediateCert: "i", IntermediateKey: "k", LeafCert: "l", LeafKey: "lk",
		}, c.Op == OpGrow)
		_ = doc

		servers, agents := len(doc.Topology.Servers), len(doc.Topology.Agents)
		wantServers, wantAgents := 1, c.Nodes-1
		if c.Nodes == 3 {
			// Three nodes are three servers: the quorum is the point.
			wantServers, wantAgents = 3, 0
		}
		if c.Op != OpGrow && (servers != wantServers || agents != wantAgents) {
			t.Errorf("%s: %d servers and %d agents, want %d and %d",
				c.Name, servers, agents, wantServers, wantAgents)
		}
		// A document without a VIP has to accept registration on the server's
		// own address, or the node cannot join at all (ADR-008).
		if !c.VIP && !doc.Topology.AcceptNodeRegistration {
			t.Errorf("%s: no VIP and no acceptNodeRegistration", c.Name)
		}
		if c.VIP && doc.Topology.VIP == nil {
			t.Errorf("%s: asks for a VIP and the document has none", c.Name)
		}
		if c.Exposure == "none" && len(doc.Gateway.Gateways) != 0 {
			t.Errorf("%s: exposes nothing and still has a gateway", c.Name)
		}
		if c.Exposure == "lb-pool" && len(doc.Kubernetes.Dataplane.LoadBalancerPool) == 0 {
			t.Errorf("%s: a pool gateway with no pool to take an address from", c.Name)
		}
		if c.PKI == "byo-cert" {
			if doc.PKI.BYOCert == nil {
				t.Errorf("%s: byo-cert with no material", c.Name)
			}
			// Supplied material with nothing to serve it on is material
			// nobody can tell is broken.
			https := false
			for _, gw := range doc.Gateway.Gateways {
				for _, l := range gw.Listeners {
					if l.Protocol == v1alpha1.ListenerHTTPS {
						https = true
					}
				}
			}
			if !https {
				t.Errorf("%s: supplies a certificate and serves no HTTPS listener", c.Name)
			}
		}
	}
}
