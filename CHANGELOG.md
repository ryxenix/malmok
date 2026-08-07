# Changelog

All notable changes to platformctl are recorded here.

Semantic versioning. The project is pre-1.0 and pre-implementation, so breaking
schema changes land in MINOR releases rather than MAJOR ones.

## [0.28.0] - 2026-08-07

### Added

- ADR-013: RKE2 installs from the tarball. One artifact set an airgap bundle can
  carry, the same path on both families, and the version pinned by one variable
  rather than by whatever a repository happens to hold.
- `internal/rke2`: `l1-bootstrap`. Verified end to end against 192.168.88.241 --
  a real single-node RKE2 cluster came up in 2m08s and reported Ready, with
  Cilium running and no ingress-nginx.
- The install step observes `rke2 --version`, not the presence of a file. That
  makes an install and an upgrade the same operation and stops a half-finished
  extraction from counting as installed. It is also why `curl | sh` cannot be a
  step (§3.1): the installer is called inside Apply, and the target on either
  side is something somebody can look at.
- The service step waits for a Ready node, not for a started unit. systemd calls
  a unit active the moment the process is up, which on a first start is minutes
  before the API server answers -- and a phase that moved on there would try to
  join a second server to something that cannot accept it. A unit that dies
  while starting is reported with its own journal.
- `tls-san` covers every future server the document names. Adding a SAN later
  regenerates the certificates on every existing server, which is what HA
  promotion trips over.
- `internal/engine/shell.go`: `ShellStep`, the check/do pair both L0 and L1 use.
  Extracted from `nodeprep` when `rke2` needed the same thing rather than
  duplicated.

### Changed

- `ShellStep` carries per-half timeouts and a retry budget. A file test and an
  install that pulls a few hundred megabytes do not deserve the same patience.

### Fixed

- `config.yaml` rendered a repeated key per list item, which is a different
  document: the last one wins and everything before it is silently dropped.
  `kubelet-arg` and `kube-apiserver-arg` now emit one sequence, and the tests
  parse the rendered file as YAML rather than comparing strings -- a file RKE2
  cannot read is a cluster that does not start.

## [0.27.0] - 2026-08-07

### Added

- `internal/nodeprep`: the first real `l0-node-prep` steps, built as engine
  steps rather than with a configuration management tool (ADR-012). Applied to
  a live Ubuntu 24.04 node and re-run to confirm the second run changes
  nothing.
- Kernel modules, sysctls, swap, the data directory, the private CA trust store
  and containerd's `registries.yaml`. The last two are conditional: they exist
  only when the document supplies material for them.
- Every file the phase writes carries a header saying what put it there. An
  operator who finds a sysctl they did not write cannot otherwise tell whether
  removing it breaks something.
- The swap step looks at `/etc/fstab`, not only at the running state. Swap that
  is off now and listed in fstab comes back at the next reboot, which turns a
  working cluster into a broken one months later with nothing having visibly
  changed. The entry is commented rather than deleted, and fstab is backed up
  first.
- The trust step compares the installed CA's content, not its presence. A node
  carrying last year's CA fails every image pull with an opaque x509 error.
- `registries.yaml` is written 0600 and its check never prints its contents: it
  holds a registry password, and a step's evidence reaches the audit report.

### Fixed

Measured on the live node, `net.ipv4.ip_forward` was 0 and
`fs.inotify.max_user_instances` was 128. Swap was off with `/swap.img` still in
fstab -- somebody had run `swapoff` and left the entry. After the phase ran,
preflight went from 34 pass / 3 warn to 35 pass / 2 warn: PF-605 was raised by
the checks, fixed by the phase, and confirmed gone by re-measuring.

## [0.26.0] - 2026-08-07

### Added

- `platformctl preflight -f cluster.yaml` runs every check the document calls
  for and prints only what needs attention, or everything with `-v`. Measured
  against a live Ubuntu 24.04 node: 64 checks in under five seconds.
- `preflight.Session`, the orchestrator. It resolves nothing itself -- reading a
  SourceRef needs the document's directory and its secret policy, both of which
  belong to the loader -- so it takes a Resolve function, which also makes the
  whole thing testable without a filesystem.
- Nodes are probed concurrently. A serial run over twenty nodes spends its time
  waiting on round trips, and preflight is what an operator is watching before
  they can start.
- `preflight.Emitter` turns probe results into `kind=probe` events. ADR-002: a
  screen that read a Report directly would be reading engine state, and the run
  would then look different depending on whether anybody was watching.
- An SSH password field in the wizard. It is deliberately absent from
  `Config.ToSpec` -- `cluster.yaml` is an audit artifact handed to customers,
  and a plaintext credential in one is exactly what the schema's SourceRef
  indirection exists to prevent -- so it reaches the preflight session directly
  and is never serialised.

### Changed

- `plan` runs preflight instead of refusing. The plan is a pure function of the
  document and what the nodes turned out to be, so there was nothing to plan
  against until now; against the live node it keeps `cilium-gw`, because the
  eBPF probes passed.
- `apply -f cluster.yaml` runs the checks and then names precisely what is
  missing, rather than refusing at the door. `apply --tui` without `--demo`
  drives the wizard against real nodes and stops at the same place.
- Probe events carry no `Level`. The schema reserves it for `kind=log`, and the
  writer rejected every probe event until this was corrected.
- A warning is emitted as `failed`, not `ok`. The schema already separates
  "blocked" (no retry helps) from "failed" (did not pass), which is exactly the
  distinction preflight needs; collapsing warnings into `ok` left a renderer
  unable to tell one from a clean check.

### Fixed

- `--demo` contacts no node again. Wiring the real checks into the wizard's
  preflight step broke the one promise that flag makes, and the addresses in a
  freshly opened wizard are placeholders nobody owns.

## [0.25.0] - 2026-08-07

### Added

- PF-704, PF-705 and PF-706: the cluster's own PKI material, as distinct from
  the gateway bundles PF-9xx covers. One pass over one set of files, because
  three passes could disagree with each other.
- PF-704 refuses a root with no intermediate (cert-manager signs with the
  intermediate and the root key must stay offline, so such material can issue
  nothing) and warns when the intermediate expires within ninety days -- every
  certificate it signs is capped at its date, so the failure arrives months
  later during a routine renewal.
- PF-707 verifies the airgap bundle against a `.sha256` sidecar. A truncated
  transfer does not announce itself: the install proceeds until the first
  missing layer, by which point half the cluster is up. With no sidecar the
  probe says so rather than computing a digest nobody can compare against.
- PF-708 checks the ACME prerequisites, starting with the one that is free to
  find and expensive to discover during an install window: an airgapped site
  cannot reach a certificate authority. HTTP-01 additionally requires a pinned
  gateway address, because the challenge is delivered to whatever the DNS
  record points at.
- `TestEveryPreflightCodeIsImplemented` walks the code registry and fails on
  any preflight code nothing produces, with an explicit table naming where the
  eleven codes implemented in other packages live.
  `TestNoProbeEmitsAnUnregisteredCode` closes the other direction: an
  unregistered code reaches a customer's audit report as an identifier with
  nothing behind it.
- `TestFailuresExplainThemselves` asserts every failing probe carries a reason
  code and enough detail to act on.

All 68 preflight codes now have an implementation.

## [0.24.0] - 2026-08-07

### Added

- `internal/exec`: a Runner interface with an SSH implementation and a fake.
  One connection is held for the life of a preflight rather than dialled per
  command -- a run asks a node forty questions, and a handshake each time turns
  a four-second probe into a minute of key exchange. `golang.org/x/crypto/ssh`
  is the dependency; the standard library has no SSH, and shelling out to the
  `ssh` binary would break the single-static-binary requirement an airgap
  imposes.
- `Sudo`, which feeds the password on stdin with `-p ''` so a probe reading
  stderr does not find a prompt in it. Measured on a real node: `sudo -n` fails
  there, and reading the packet filter ruleset needs root.
- 39 node probes: PF-101..107, PF-201..209, PF-301..305, PF-401..407,
  PF-501/503, PF-605/607/609/610, PF-801..805. Plus the cross-node ones
  (PF-108, PF-502, PF-504, PF-604) and the peer ones (PF-601, PF-602).
- PF-204 attempts a real program load through `bpftool`, falling back to a
  direct `bpf()` syscall through python3's ctypes. Everything the other eBPF
  probes read can be true on a node that still refuses the load, and inferring
  from symbols is what this code exists not to do.
- Every fixture in the tests is output recorded from a live Ubuntu 24.04 node,
  not output written from memory.

### Changed

- The Longhorn prerequisites (PF-404, PF-405, PF-406) are explicit stubs by
  decision: probing external block storage properly is a larger scope than is
  worth carrying before the storage layer exists. They report skip rather than
  pass, so nothing claims Longhorn works, and `FailedAt` does not see them, so
  nothing downgrades storage on evidence that was never collected.
- `exec.Fake` resolves the longest matching pattern. Map order is random and a
  command often contains two patterns -- the shell that reads the kernel
  configuration also contains `uname -r` -- so first-match made a test depend
  on how Go walked the map that run.

### Fixed

Four probe designs, corrected against the real node rather than assumption:

- `systemctl is-active ufw` is not the firewall. The measured node answers
  "active" while `ufw status` answers "Status: inactive": the unit is running
  and the policy allows everything. PF-304 asks each firewall about itself.
- PF-208 and PF-605 cannot read what does not exist. `nf_conntrack_max` and
  `bridge-nf-call-iptables` only appear once their modules are loaded, and
  loading one is a change preflight must not make. They report that the value
  could not be measured rather than inventing a kernel default.
- `/var/lib/rancher` being absent is the normal state before an install, so
  `findmnt` exiting non-zero is an answer rather than an error. PF-402 and
  PF-403 measure the parent, and `df -i` cannot be combined with `--output`.
- PF-601 distinguishes refused from dropped. Before an install nothing is
  listening, so a refused connection is the expected result and a timeout is
  the finding.

`TestProbesNeverModifyTheNode` walks every command a full run issues and fails
on anything that would change the node.

## [0.23.0] - 2026-08-07

### Added

- `preflight.CheckCIDRs` (PF-611). The pod and service networks are invisible
  to the customer's routing, so an overlap does not fail at install: it fails
  later and intermittently, when a pod is given an address that also belongs to
  something real. Node addresses, `nodeIP` overrides, the VIP, gateway
  addresses and the load balancer pool are all checked, and the failure names
  which of them collided.
- `preflight.Prober` (PF-603, PF-606, PF-608, PF-701, PF-702, PF-703): the
  probes that run from the machine executing the tool. Everything they touch
  is behind an interface, so each failure path has a test rather than an
  argument.
- PF-701/702/703 are one request rather than three. Reachability, whether the
  certificate verifies and whether the credentials are accepted are three
  answers to the same connection, and a transport error is attributed to the
  probe actually at fault -- an untrusted CA reported as unreachable is the
  misdiagnosis that sends an operator to the firewall for a missing file.
- `preflight.FromCert`, the adapter from the certificate gates. The cert
  package keeps its own finding type so it does not import this one; the two
  would otherwise be a cycle.
- `preflight.ListenerHostnames`, which collects the names a bundle has to cover
  for a given secret. Selection needs them and they live on the listeners, so
  every caller would otherwise walk the gateway tree and get it subtly wrong.

### Changed

- Probes that measure from the wrong vantage point say so in their pass
  message. A registry this machine can reach is not a registry the nodes can
  reach, and silence on a VIP port is not proof the address is free -- a host
  that drops unsolicited packets looks identical. Tests assert the wording,
  because a pass that overstates what it measured is worse than no probe.
- `registry.insecure` produces a warning rather than a silent pass. Nothing
  authenticates the connection every node pulls images over.

## [0.22.0] - 2026-08-06

### Added

- `internal/cert`: the certificate gates of `docs/20-cert.md` §4, PF-901
  through PF-912. These are the preflight checks that need no node and no SSH,
  so they are the part of preflight that can be built and proven now.
- Classification that never reads a file name (§2). Encoding comes from the
  leading bytes -- PEM or DER -- and leaf/intermediate/root from
  BasicConstraints. A certificate with no BasicConstraints extension is an
  end-entity certificate, not a CA.
- Chain assembly by issuer link (§3), ignoring file, name and directory order.
  Customers send fullchain files in the wrong order often enough that trusting
  the order ships a chain some clients accept and others reject.
- Key-to-leaf matching by SubjectPublicKeyInfo bytes (§2.4). A key from another
  domain produces a handshake failure and no other symptom.
- Encrypted key support with no new dependency: PBES2 with PBKDF2 and AES-CBC
  is read directly from the ASN.1, using `crypto/pbkdf2`, which has been
  standard library since Go 1.24. Legacy `Proc-Type` PEM encryption is read
  too; customer files still arrive that way.
- RFC 6125 hostname matching as a table-driven test. `*.acme.co.kr` covers
  `api.acme.co.kr` but neither `acme.co.kr` nor `a.b.acme.co.kr`, and the
  CommonName is not consulted at all.
- Bundle output per §5: `tls.crt` without the root, `tls.key` normalised to
  PKCS#8 whatever came in, `ca.crt` only for a private root, and the five audit
  annotations.
- Fixtures are generated in the test rather than checked in, as CLAUDE.md
  requires. A checked-in certificate expires and teaches people to ignore a
  failing suite.

### Fixed

- A chain that stops at an intermediate is complete when it verifies against
  the host trust store. A public CA does not ship its root, so requiring one
  would have refused every commercially issued certificate.
- The wildcard matcher no longer refuses a wildcard on a single-label private
  TLD. Separating `co.kr` from `acme.co.kr` needs the Public Suffix List, an
  external dependency the certificate path does not take, and the label-count
  substitute refused `*.internal` -- which is exactly what a homelab private CA
  issues.

## [0.21.0] - 2026-08-05

### Added

- A topology panel on the node and summary screens. Which node sits on which
  segment is a relationship, and a list makes the reader reconstruct it: the
  diagram is the difference between an operator checking their own entry and
  hoping they typed it right. Nodes are grouped by /24 because a site's
  segments are a fact about its network, not something the operator should be
  asked to restate. The load balancer range is drawn on the segment it
  addresses, which is how a wrong one becomes visible.
- The join address is drawn outside every box. It is a name all nodes resolve,
  and putting it inside one would say the opposite (ADR-008).
- Addresses that do not parse -- hostnames, typos -- collect in their own
  group rather than being forced onto a segment they do not belong to.
- Focus bars, a smooth progress bar, a spinner, status badges and drawn
  keycaps. Colour and gradients are used only where the terminal reports it
  can show them: `tea.ColorProfileMsg` decides, rather than the tool guessing
  and painting a band of noise onto a sixteen-colour console.

### Changed

- The panel is drawn only when there is vertical room left after the form, so
  an 80x24 console loses the diagram rather than the fields.

### Fixed

- ASCII mode renders the whole panel in plain characters instead of falling
  through to glyphs a legacy console mangles. `TestTopologyPanelIsRectangular`
  asserts every line of the box is the same width in both character sets, at
  three terminal widths and in both languages -- a box that is not a box reads
  as broken, and an operator stops trusting what it says.

## [0.20.0] - 2026-08-04

### Added

- Every run now writes the document it was built from to
  `<runDir>/cluster.yaml`, which `docs/11-execute.md` §1.1 has called for since
  it was written. The finished screen had been naming a file that did not
  exist.
- `spec.Marshal`, `Save`, `Snapshot` and `SnapshotRaw`. Serialisation lives in
  the same package as the loader so the two cannot drift; `TestRoundTrip`
  asserts that what the writer produces parses strictly and validates.
- The generated document carries a header saying where it came from. A
  `cluster.yaml` found on a customer site months later has to explain itself:
  whether it was written by hand or produced by the installer changes what may
  be edited.
- An operator's own file is snapshotted byte for byte rather than
  re-marshalled. The digest the resume check compares is of those bytes, and a
  round trip through the encoder changes them even where nothing that matters
  changed.

### Changed

- `Snapshot` refuses to write a document that would not load. A snapshot that
  cannot be re-read defeats every reason for keeping one — and the rule
  immediately caught the simulated run, which was leaving behind a document
  missing half its required fields. `--demo` now carries the `homelab` profile
  so its snapshot is a real, reloadable document.
- `pki.domain` is no longer demanded when `pki.mode` is empty; the missing mode
  is the error worth reporting.

## [0.19.0] - 2026-08-04

### Added

- `--rail` chooses the layout: the step list down the left (Ubuntu desktop
  installer) or without it (Proxmox, Ubuntu server). `s` toggles it at runtime.
  Operators arrive from both, so both are offered.
- The finished screen now answers what was built, where it went and what to run
  next. It listed a run id and nothing else; a run id alone is not something an
  operator can act on. Elapsed time comes from the run's own events rather than
  a clock here, so it matches the event file beside it.
- A failed run lists the failures and hands back the exact `--resume` command.

### Changed

- Title bar and button row are separated by rules, the exit action sits
  bottom-left away from the actions that move forward (Proxmox's placement),
  and the key help moved below the buttons. Rail entries are numbered.
- **Space selects, Enter continues.** Enter used to select, which meant Tabbing
  to the buttons on all eleven screens. On a form, Enter now commits the field
  and moves to the next, and off the last one onto the buttons.
- The ASCII fallback forces the English catalogue. A console that cannot draw a
  box character cannot draw Hangul either, so a Korean screen there is
  unreadable in a way the glyph fallback cannot fix.

### Fixed

- The Korean catalogue carried `·` in a dozen entries, which the ASCII fallback
  could not strip — the existing test only checked English. Glyphs now come
  from the character set at render time, and a test bars them from the
  catalogue.

## [0.18.0] - 2026-08-04

### Added

- **BREAKING (schema)** `pki.mode: none` — issue nothing at build time, and the
  default for `homelab`. At a first build the service domain is usually not
  decided, DNS is not delegated, and nobody has said whether the customer will
  supply certificates. Forcing a mode and a domain there configures the cluster
  around guesses, and a certificate for a name nobody serves still has to be
  renewed. Gateways come up on HTTP; certificates are added later with
  `platformctl cert apply`, the same command that renews them.
- `registry.mode: embedded` — RKE2's own registry mirror, now the default for
  the online profiles (ADR-011). A registry that is stood up has to be kept
  alive for the life of the cluster, with credentials, certificates and
  backups; the embedded mirror shares what the nodes already hold. It is not a
  registry you push to, so an air-gapped cluster still needs images seeded from
  a bundle, and the validator enforces that.
- `registry.mode: upstream` — pull from the internet directly. Refused in an
  air-gapped network.
- The certificates and registry screens now ask *whether* before *how*. Account
  details appear only after choosing to issue; registry details only for the
  modes that point somewhere.

### Changed

- `pki.domain` and `gateway.domainSuffix` are required only when something is
  being issued. Deferring certificates defers the naming question with them.

## [0.17.0] - 2026-08-04

### Fixed

- **An invalid document could still be installed.** The summary screen
  validated and reported, and then `Install` proceeded anyway. Validating and
  installing regardless makes the check decoration. The button is now absent
  while the document is broken, and the transition refuses even if reached
  directly.
- **Validation messages led nowhere.** They said what was wrong and left the
  operator pressing Back to find the screen that owned it. Each problem now
  carries the step that fixes it, is shown under that screen's name, and the
  primary button becomes "Go and fix", which jumps there. Problems are ordered
  by screen so the operator walks forwards.

### Added

- `ownerOf` maps a field path to the screen that collects it, and a test walks
  eleven real validator messages to assert none of them is a dead end.

## [0.16.0] - 2026-08-04

### Added

- Network, Registry and Certificates screens, and NFS settings on the options
  screen. These are exactly the fields the validator reported missing in
  v0.15.0, so the wizard now produces a `ClusterSpec` that validates — the
  summary screen says so rather than listing gaps.
- Field editing is generalised: any step can declare a list of editable lines.
  It used to be hardcoded to the nodes screen, which is why a screen only
  existed for the group of values that happened to be written first.
- Screens adapt to the profile. An ACME profile is asked for an account email
  and a DNS provider; a private-CA profile for certificate references. An
  online profile is not asked about a proxy, and NFS settings appear only when
  NFS is the chosen driver.
- Secret fields are masked until focused. What is stored is a `SourceRef`
  rather than a secret, but a token typed at a customer site is still read over
  somebody's shoulder — and masking it while it is being corrected would make
  it uncorrectable.

### Changed

- The profile step moved ahead of everything it decides. Network mode, PKI mode
  and storage driver all follow from it, and asking about a proxy before
  knowing whether there is one wastes a question. Eleven steps now.
- The nodes screen gained the join address, RKE2 version and domain.

### Notes

- `TestEveryProfileProducesAValidDocument` checks all six Tier-1 profiles, not
  just the default. It immediately caught `airgap-conservative`, which uses NFS
  and had no screen asking for the server and export path — a dead end an
  operator would have found after answering eight screens.

## [0.15.0] - 2026-08-04

### Changed

- The wizard's profile list now comes from `internal/spec` instead of a second
  list inside the TUI. Two lists of the same six profiles disagree the first
  time one is edited, and the one the engine reads has to win. Each profile's
  summary line is derived from its baseline, so it cannot describe something
  the engine would not do.
- Choosing a profile moves its baseline into the collected values, so the
  options screen shows what will actually be installed rather than whatever was
  selected before.
- The summary screen builds a real `ClusterSpec` and runs the real validator.
  The vague "the engine does not consume this yet" note is gone; the screen now
  names each missing field and says why it matters.
- The nodes screen gained the join address, the RKE2 version and the domain.
  Setting the join address to a node's own address is reported immediately with
  its ADR-008 rationale.

### Notes

- Wiring the validator in exposed exactly which fields the wizard still does
  not ask for: the proxy, the load balancer pool, the registry and the PKI
  material. A test asserts the summary reports them by name, so the gap stays
  visible instead of being found on a customer site.

## [0.14.0] - 2026-08-03

### Added

- `internal/spec`: reads and validates `cluster.yaml`. Unknown keys are an
  error rather than something to ignore — a typo in `registrationAddress` that
  silently becomes nothing is the class of failure that surfaces months later
  as an unexplained rejoin. Validation reports every problem at once, each
  naming its field path.
- `SourceRef` resolution for `file://`, `env://` and `literal://`, with
  `sops://` recognised and refused rather than read as a filename. A
  `literal://` on a secret field is rejected unless `--allow-literal-secrets`:
  cluster.yaml is an audit artifact handed to customers, and a password in it
  outlives the engagement.
- Profile baselines for the six Tier-1 combinations of ADR-003. A profile
  fills only what the document left at its zero value, and `ApplyProfile`
  returns the list of fields it supplied so the audit report can separate what
  the operator chose from what the tool did. `rke2-ingress-nginx` is disabled
  whatever the document says (ADR-005), and that is recorded rather than
  silent.
- `internal/preflight`: `NodeCapability` and `ProbeResult`, the types
  `docs/10-preflight-plan.md` sketches. A probe that never ran is not a pass —
  missing evidence must not read as good news.
- `internal/plan`: the downgrade decision tree as a pure function.
  `(ClusterSpec, []NodeCapability) -> Plan`, no side effects and no clock, so
  the whole tree is table-driven tested without a cluster. Every downgrade
  records its triggering probes and the nodes involved, because "why is this
  running Traefik" has no answer without them.
- `platformctl plan -f cluster.yaml [--validate-only]`, which prints the
  resolved configuration and marks each value the profile supplied.

### Notes

- `plan` without `--validate-only` reports that preflight is not implemented
  instead of planning against assumed capabilities. A plan derived from nodes
  nobody has looked at is worse than no plan.

## [0.13.0] - 2026-08-03

### Changed

- **BREAKING (screen)** `--tui` is now an interactive installer rather than a
  progress display. Eight steps with a rail down the left, a wide content pane
  and primary/secondary buttons bottom right — the layout of a graphical OS
  installer, rendered in cells. Language, nodes, profile, options, checks,
  summary, install, finished.
- Checks and Install really run the engine, through one runner and one state
  file, so resume treats the whole wizard as a single run. Node details entered
  on screen reach the run; profile, dataplane and storage are recorded in the
  summary and marked on screen as not yet consumed, because the cluster.yaml
  loader and profile defaults do not exist yet.
- `lipgloss/v2` and `huh/v2` replace the v1 packages, which pin an `x/cellbuf`
  that does not compile against Bubble Tea v2's `x/ansi`.

### Fixed

- Continue on the Checks screen re-ran the checks instead of moving on, so the
  wizard could never be finished. Continue and Retry are separate actions now.
- Tab then Enter quit the installer: button focus started at index zero, which
  is Quit on the first screen. Every step preselects its primary action, as a
  graphical installer does.
- Space did nothing. Bubble Tea v2 reports the key as `"space"`, not `" "`.
- Help text overflowed the pane. It is prose in a catalogue, and a translator
  cannot know the column count — Korean is about twice as wide per character —
  so it is wrapped at render time by cell width.
- The rail divider drew a horizontal rule; `Glyphs` had no vertical one.

## [0.12.0] - 2026-08-03

### Added

- `internal/tui` and `--tui` on both `apply` and `attach`: the full-screen
  view, in the structured-plain idiom. Phase tree, live progress, retry
  counter, log pane and failure list, all derived from the event stream. The
  engine does not know it exists.
- Two modes, because quitting is not the same question in both places.
  `apply --tui` owns the engine, so `q` aborts the run and the footer says so;
  `attach --tui` observes one elsewhere, so `d` detaches and the engine keeps
  running.
- Screen strings live in `internal/tui/catalogue/{en,ko}.yaml`, embedded in the
  binary. English default with a `g` toggle (ADR-009). A test asserts both
  catalogues define the same keys, so toggling cannot replace words with raw
  key names, and that no entry embeds a diagnostic code.
- ASCII fallback with auto-detection and `--ascii`. Serial consoles, IPMI
  viewers and PuTTY with the wrong codepage are where somebody is sitting when
  an install is going badly, and a status column of mojibake is worse than one
  of plain ASCII. The layout is identical in both sets.

### Changed

- Layout measures terminal cells rather than runes. A Korean glyph is two
  columns wide, so rune-based padding lays out correctly in English and wrecks
  every column in Korean — which would have made the language toggle a promise
  the layout could not keep. Tests assert no line exceeds the terminal width
  and the status column aligns in both languages.
- `lipgloss` was dropped for `x/ansi`, which Bubble Tea v2 already uses.
  lipgloss v1 pins an `x/cellbuf` that does not compile against v2's `x/ansi`.

## [0.11.0] - 2026-08-03

### Added

- `platformctl apply --demo`: a full simulated run through the phase
  catalogue. No node is contacted and nothing is installed, but the runner,
  the state file, the event stream and a renderer all run end to end. It is
  what makes it possible to settle the TUI's layout before writing 67 probes
  against it. `--fail-at` and `--flaky-at` reproduce a hard failure and a
  retry on demand.
- `internal/demo`, the simulated phase set. Scaffolding, marked as such, to be
  deleted once real steps cover the same phases.
- `engine.Log`, a per-step log sink carried in the context. A step needs to
  stream output while it works: the mockup shows log lines under a running
  step, and a three-hour role that says nothing is indistinguishable from one
  that has hung. It travels in the context so a silent step implements
  nothing and the runner can bind phase, step and node to every line.
- `apply --resume <id>` continues an interrupted run, and `-f cluster.yaml`
  reports exactly what is missing rather than appearing to work.

### Changed

- Step progress now counts a finished step as done. Previously the last step
  of a phase reported n-1/n and the bar never filled.
- The text renderer prints the retry counter only once it is actually a
  retry. "retry 1/3" on every first attempt trains the operator to ignore the
  field.

## [0.10.0] - 2026-08-03

### Added

- `internal/engine`, the phase runner: `Step`, `Phase`, `Runner`. It implements
  the observe / apply / re-observe contract from `docs/11-execute.md` §3, resume
  from §4, event emission from §5 and retry policy from §6, and it knows nothing
  about what a step does — Ansible, Helm and kubectl all arrive as `Step`
  implementations later.
- `EX-xxx`, a fifth code family for execution failures. §6 requires a code on
  every failure event, and the four existing families do not cover "the step ran
  and did not take": PF measures before any change, PV verifies the wire
  afterwards, MC is day-2 inspection, DG records a downgrade. Nine codes,
  defined in `internal/codes/execution.go`.
- `TestEngineDoesNotImportTUI` — acceptance criterion D3 and the enforcement
  `CLAUDE.md` promises. A companion test also bars terminal libraries, since
  rendering creeps back in one spinner at a time.
- Every §7 group A and B criterion now runs against the real runner with fake
  steps: second run skips everything, recheck re-observes without applying,
  resume does not repeat completed work, a sequential traversal leaves the nodes
  after a failure untouched, and a one-shot step left failed stops for
  confirmation.

### Changed

- `docs/11-execute.md` §3.1: `Observe` returns an `Observation` carrying
  `Satisfied` rather than a separate `Satisfied(State) bool`. Splitting them
  needs a per-step state type, which makes a heterogeneous `[]Step` impossible
  without `any`; the contract is identical either way. §6 gains the `EX` table.
- `CLAUDE.md` naming rules list the `EX-` prefix.

## [0.9.0] - 2026-08-03

### Added

- `cmd/platformctl` — the first runnable binary, with `attach` as its only
  subcommand. Commands are registered as they are implemented; one that exists
  and does nothing is worse at a customer site than one that is absent.
- `platformctl attach` resolves the event file from `--file`, else the newest
  run under `--bundle`, else the bundle's shared event log. `--follow=false`
  replays a finished run, `--verbose` includes log lines, and `-o json` passes
  the stream through unchanged — the engine already emits JSONL, so that format
  is the stream itself rather than a second serialisation.
- `github.com/spf13/cobra`, the CLI framework fixed by `CLAUDE.md`.

### Changed

- The failure summary lists only originating failures — probe and step events.
  A failed phase or run restates the step that failed, and counting all three
  turned one incident into three entries.

## [0.8.0] - 2026-08-03

### Added

- `internal/attach`: replay an event file, then tail it. Implements
  `docs/11-execute.md` §1.2 and §5.6. It waits for the file to appear, survives
  rotation, never consumes a partial trailing line, and filters to one run when
  a fixed `output.eventLog` holds several.
- `TextRenderer`, the first consumer of the stream, plus `Collector` for tests
  and `--output json`. The progress bar is drawn here — `Bar()` is the only
  place block glyphs exist, and a test asserts that an event carrying its output
  is rejected by `Event.Validate`. That is §5.3 made mechanical instead of
  aspirational.
- `GapReporter`, an optional `Sink` extension for gaps that survive a re-read.
- `LatestRun` and `LatestRunDir` for `attach` with no arguments. Run ids are
  ULIDs, so lexical order is chronological and no clock has to be trusted.

### Changed

- `docs/11-execute.md` §5.6: a gap now triggers at most one re-read. Re-reading
  recovers lines this reader missed but cannot conjure lines the file lacks, so
  a gap that survives is reported and accepted. Unconditional re-reading put the
  follower in a loop it never left — the test caught it.

## [0.7.0] - 2026-08-03

### Added

- `internal/state`, implementing `docs/11-execute.md` §4: the run state file,
  the resume decision, and atomic persistence. Every §7 group B acceptance
  criterion is covered by a test.
- `State.Decide` returns run / skip / **confirm**. The third exists because a
  `oneShot` step left running or failed is neither obviously safe nor obviously
  unsafe to repeat, and deciding automatically goes wrong in one direction or
  the other.
- `Save` writes through a temporary file in the same directory with `fsync`,
  `rename`, then a directory `fsync`. This closes the open question in
  §11-execute about atomic state updates.
- `CheckResumable` refuses a run whose `cluster.yaml` digest has changed, and
  names `--force-resume` in the error rather than just failing.

### Changed

- `docs/11-execute.md` §4.1's state file example now matches the
  implementation: node progress is recorded per step as `step@node` rather than
  as a `nodes` summary, which is the granularity criterion B4 needs. The node
  rollup is derived instead of stored so it cannot drift from the steps it
  summarises. §4.2 gains the three-way resume decision table.
- Step keys split on the first `@`, putting the "no `@`" constraint on step ids
  — constants we author — rather than on node ids, which come from a customer's
  `cluster.yaml` and are whatever they are.

## [0.6.0] - 2026-08-03

### Added

- `internal/event`, the first implementation of the `docs/11-execute.md` §5
  contract: the `Event` schema, a sequence-owning append-only `Writer`, a
  `Scanner` that verifies per-run sequence continuity, and replay helpers for
  `attach`.
- `Event.Validate` enforces the §7 group C acceptance criteria in the engine
  itself rather than only in tests — codes must exist in `internal/codes`, a
  failed or blocked status must carry one, detail must be single-line printable
  ASCII, and no event may contain ANSI escapes or block-drawing glyphs.
- `TestScreenElementsFromADR002` encodes criterion C6: every screen element in
  the ADR-002 mockup is constructed as an event and validated, so a schema too
  thin to draw the TUI fails the build instead of being discovered when the TUI
  reaches into the engine for a missing value.
- `Timestamp` renders RFC3339 at exactly millisecond precision. `time.Time`'s
  variable-width nanoseconds make event files noisy to diff and to read on site.

### Changed

- `internal/codes`' dangling-reference test now skips `_test.go`. Negative tests
  must name unregistered codes to prove the rejection path; the guard exists for
  production code, which is where `PF-908` actually lived.

## [0.5.0] - 2026-08-03

### Added

- `docs/11-execute.md`, the engine↔renderer contract and the last unwritten WP
  document: 13-phase catalogue with grades and ordering constraints, the
  step-level idempotency contract (`Observe`/`Satisfied`/`Apply`), the resume
  model and state file, the JSONL event schema, retry policy, and §7 acceptance
  criteria in five groups.
- `platformctl attach` and `apply --detach` in `docs/00-architecture.md` §4.
  The engine outliving the renderer needs a way back in, and a dropped SSH
  session at a customer site is routine rather than exceptional.

### Changed

- `CLAUDE.md`'s prohibition table said the TUI is "merely a cluster.yaml
  generator". That reading is what would make someone remove the TUI's execute
  button, and implementation sessions read `CLAUDE.md` rather than the ADRs, so
  the row is rewritten and followed by an explicit note: installs run inside the
  TUI; the only forbidden thing is the TUI owning installation logic.
- `api/v1alpha1/types.go` no longer claims the TUI is "never a participant in
  execution". The real invariant — no code path may *require* a terminal — is
  stated separately from who initiates the run.

### Fixed

- `docs/00-architecture.md` §1.1 and §1.2 reverted to `preflight-probes.md`,
  `cert-bundle.md`, `day2-maintenance.md` and `ingress.go` when the ADR-002
  rewrite landed on a pre-`b6ea94a` copy. Restored, with `11-execute.md` added.

## [0.4.2] - 2026-08-03

### Added

- `docs/30-maintenance.md` §2.5, separating the three certificate expiry
  thresholds into named roles: ① apply gate (per bundle, drives `PF-904`),
  ② alert firing (per certificate series, cluster-wide), ③ report grading (per
  contract). It also records why collapsing them into one number breaks
  something either way — a 45-day report threshold lets a quarterly report ship
  past an expiry, and a 90-day alert fires daily for three months until nobody
  reads it. Roles ② and ③ still have no `ClusterSpec` fields; §2.5 marks that
  explicitly, and `maintenance.thresholds` in §4.3 remains documentation rather
  than schema.
- Cross-references to §2.5 from `docs/20-cert.md` (`PF-904`),
  `api/v1alpha1/gateway.go`, `internal/codes/preflight.go` and the DMZ example.

### Changed

- `BYOMaterial.ExpiryWarningDays` default 30 → 45, matching the series A first
  alert step in §2.4. The value had disagreed with both documents since v0.1.
- §2.4 and §4.3 headings now name which role they define.

## [0.4.1] - 2026-08-03

### Fixed

- `docs/00-architecture.md` §1.1 and §1.2 named `preflight-probes.md`,
  `cert-bundle.md` and `day2-maintenance.md`, which had been renamed to the
  numbered scheme before v0.1 was packaged. §1.1 also gains `internal/codes`
  and `99-codes.md`, the two sources of truth it was missing.
- `ProfileCustom` sat inside the const block labelled "Tier-1 profiles" while
  being Tier-3 by its own definition. Moved out, so that "every constant in
  that block has a CI lane" — which the release gate depends on — is true.
- `CLAUDE.md` pointed the JSONL event schema at `docs/90-events.md` while its
  own routing table and the README pointed at `docs/11-execute.md`. Both
  documents were unwritten, so the split was settled by decision:
  `docs/11-execute.md` owns the event schema and `90-events.md` will not exist.

### Changed

- `gofmt` applied to `api/v1alpha1/types.go`: whitespace only, seven
  pre-existing misalignments. The repository is now `gofmt` clean, which
  matters as of v0.3.0 when `go.mod` landed.

## [0.4.0] - 2026-08-03

### Added

- `internal/codes` completed: 8 `PV` post-apply verification codes from
  `docs/20-cert.md` §6.5, 42 `MC` maintenance checks from
  `docs/30-maintenance.md` §3, and 5 `DG` downgrade reasons. 122 codes in total.
- `internal/codes/gen` and `go generate ./internal/codes/`, rendering
  `docs/99-codes.md`. The document is an artifact and carries a do-not-edit
  banner; a test diffs it against the registry so it cannot rot unnoticed.
- Inventory tests pinning every family's exact code set, plus determinism and
  banner tests for the generator.

### Changed

- `CLAUDE.md` now names the generated registry path and states that retired
  numbers are never reused.

## [0.3.0] - 2026-08-03

### Added

- `go.mod` — module `platform.ryxen.dev/platformctl`, Go 1.24. Nothing can live
  under `internal/` without it.
- `internal/codes` — the diagnostic code registry, now the single source of
  truth as CLAUDE.md requires. 67 preflight codes collected from
  `docs/10-preflight-plan.md` and `docs/20-cert.md` §4, each carrying family,
  category, English summary, default message, severity and failure sub-codes.
- `PF-612` "Gateway external address is pinned".
- Collision, inventory and dangling-reference tests. The last one walks the
  repository and fails on any code cited in Go or YAML that the registry does
  not define; it is what would have caught `PF-908` at the time it was written.

### Changed

- `PF-908` is retired and its meaning now lives at `PF-612`. `api/v1alpha1`
  referenced `PF-908` although no document ever defined it, and `PF-9xx` is
  reserved by `docs/20-cert.md` for certificate material — an addressing and
  DNS lead-time check does not belong in that block. The number is not reused.

### Fixed

- `types.go` cited `PF-403/404` for the Longhorn prerequisites. `PF-403` is
  inode headroom; the Longhorn checks are `PF-404` (iscsid), `PF-405` (NFS
  client) and `PF-406` (multipathd).

## [0.2.0] - 2026-08-03

### Added

- `ClusterSpec.Gateway`. The gateway layer had no entry point in the root schema
  at all, so `examples/cluster-dmz.yaml` could not be decoded.

### Changed

- **BREAKING** `cluster.yaml`: the top-level `ingress` key is now `gateway`.
  No released consumers exist — the engine is unimplemented.
- `IngressSpec` renamed to `GatewaySpec`, and `api/v1alpha1/ingress.go` renamed
  to `api/v1alpha1/gateway.go`. The type modelled the Gateway API throughout —
  GatewayClass, Gateway, listeners, TLS, DNS — and never held a Kubernetes
  Ingress field. Ingress itself is excluded outright by ADR-005.
- `GatewaySpec` (the per-gateway list element) renamed to `Gateway`, freeing the
  name for the type above: `Gateways []Gateway`.
- Default contract ConfigMap renamed from `platform-ingress-contract` to
  `platform-gateway-contract`. Nothing consumes it yet.

### Fixed

- `api/v1alpha1` did not compile. `ListenerTLS` was declared twice — once as a
  `ListenerProtocol` constant and once as the listener TLS config struct. The
  constant is now `ListenerTLSPassthrough`; its wire value stays `"TLS"`, so
  `protocol: TLS` in `cluster.yaml` is unaffected.

## [0.1.0] - 2026-08-02

### Added

- Initial design-stage import. No executable code.
- `CLAUDE.md` — session contract: prohibitions, fixed stack, architectural
  invariants, document routing.
- `docs/00-architecture.md` — 10 ADRs, L0–L3 layer split, Tier scheme with six
  T1 profiles, roadmap.
- `docs/10-preflight-plan.md` — probe catalog `PF-1xx`–`PF-8xx`, dataplane
  downgrade decision tree, `DG-xxx` codes.
- `docs/20-cert.md` — certificate classification and chain assembly, `PF-9xx`
  file gates, `PV-xxx` wire verification.
- `docs/30-maintenance.md` — certificate series A–D, `MC-xxx` check catalog,
  maintenance report structure.
- `api/v1alpha1` — `ClusterSpec` and `IngressSpec`.
- `examples/cluster-dmz.yaml` — DMZ site with mixed per-listener TLS sources.
