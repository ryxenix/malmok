# Changelog

All notable changes to platformctl are recorded here.

Semantic versioning. The project is pre-1.0 and pre-implementation, so breaking
schema changes land in MINOR releases rather than MAJOR ones.

## [0.49.5] - 2026-08-18

### Added

- The version field offers both channel answers. Stable stays the suggestion --
  it is upstream's production judgement, what install.sh defaults to, and it
  lags the newest release on purpose -- and Space on the field flips to the
  latest release and back, the same key every other chooser uses and no extra
  rows on the screen. Typing still overrides both, and on an air-gapped site
  with no channel answer Space does nothing and the field is typed like any
  other. One request now fetches both channels instead of following a redirect
  for one.

## [0.49.4] - 2026-08-18

### Fixed

The empty-field wizard flow was driven end to end through the real TUI (a pty,
keystrokes, no shortcuts) against the live node: menu, remote server, every
field left empty except the address, the account and the version, preflight,
summary valid, install, finished, back to the menu. The run completed; the
cluster survived. Getting there surfaced one deep defect:

- `cilium-applied` was satisfied by the previous configuration. It checked two
  keys that are true on every build, so the phase moved on -- and restarted
  cilium -- before the helm controller had applied the new values. The pods
  came back running the old configuration under a config that said otherwise:
  the operator started with hostnetwork=false, generated addressless Envoy
  listeners, and the gateway never answered. It now waits for cilium-config to
  carry the values this document implies, including the ones that differ
  between documents.
- `cilium-current` measures the sentence it always meant: every running,
  non-terminating cilium pod started after cilium-config was last written (the
  API server's own managedFields stamp). The rollout-restart annotation it
  used before knows nothing about the rollouts the helm controller performs,
  so a pod rolled before the values landed looked current while running the
  old configuration. Third observable for this step; the first two each
  survived until the live cluster found the case they missed.

## [0.49.3] - 2026-08-18

### Fixed

- Every example value is gone from the wizard: the placeholder nodes, the proxy
  at acme.local, the Harbor host, the CA references, the ACME account. An
  example in an editable field reads as a real value, gets accepted by habit,
  and produces a document that names machines nobody owns. An empty field is a
  question, and the validator asks it at the summary if it goes unanswered.
  What still arrives filled genuinely is known: the server address from the
  machine itself, the RKE2 version from the channel server, root/22 as the SSH
  convention, and the axis defaults whose profile match is derived.
- DG-010 fires only when a gateway will actually request a pool address. With
  no gateways nothing asks, and demanding a pool anyway made the emptied
  default flow un-completable -- requiring exactly the segment IPs a first
  build may not have.
- The profile-composition test was asserting that fake data validates: it
  passed on the seeded CA references and the seeded Harbor. It now fills the
  material the way an operator would, per baseline, which also documents what
  each baseline genuinely demands.

## [0.49.2] - 2026-08-18

### Fixed

Two defects one screenshot showed: a development box put thirty rows of docker
bridges between the operator and the node fields.

- The local address chooser filters virtual interfaces -- docker, br-*, veth,
  cni, cilium, virbr, wireguard and the rest -- because a bridge gateway is not
  an address the LAN routes to this machine, and a chooser listing thirty of
  them around the one real NIC is a chooser nobody can use. The filter is about
  what to offer: `IsLocal` still accepts a bridge address, since a document
  that names one still means this machine.
- The chooser moved below the node fields. The fields are the work; the chooser
  is one confirmation of an address the machine already knows, and a list long
  enough to scroll must not stand between the operator and the work.

## [0.49.1] - 2026-08-18

### Fixed

- The wizard suggested RKE2 v1.34.5 -- a version three minors old, hardcoded
  when the wizard was written. No seeded version survives upstream's release
  cadence, so none is seeded: the current stable is asked from RKE2's own
  channel server when the program starts (the same authority the install
  script consults -- whose `stable` currently lags its newest release, a
  judgement no hardcoded string carries). An air-gapped or proxied site gets no
  answer, an empty field, and a validator that asks for it -- which is honest,
  where a stale default is a suggestion that looks like knowledge. A version
  the operator already typed is never overwritten by the network.

## [0.49.0] - 2026-08-18

### Changed

A visual pass over the chrome, in the direction terminal tools have settled on
(k9s, lazygit, btop) without copying their table-first layout -- this is an
installer, not a browser.

- The header is a product chip, a breadcrumb and a badge instead of a solid
  colour band: `Malmok · 클러스터 설치 ▸ 노드 · 2/10`. A full-width bar spends
  the strongest colour on the screen saying nothing; the crumb answers the
  glance ("which flow, which screen") and the screen's own heading answers the
  read. The rule below it takes a horizontal gradient where the terminal is
  truecolor.
- The rail marks the current step with the same focus bar every list uses,
  instead of a third marker style of its own.
- The menu entries carry icons (shape-only, with ASCII fallbacks decided where
  every other glyph decides them), the selected entry's icon takes the accent,
  and the wordmark gains a centred tagline.

Everything still degrades: mono keeps weight and reverse video, ASCII keeps
plain characters, sixteen colours get no gradients.

## [0.48.5] - 2026-08-18

### Added

- The start menu opens under a block-letter MALMOK wordmark, centred, with a
  vertical gradient where the terminal is truecolor -- the accent colour where
  it is not, because sixteen colours cannot blend and a gradient there is a
  band of noise. A bare list reads as a fragment of something; the wordmark is
  how a terminal tool says it is a product (k9s, lazygit, btop -- the
  convention is old enough to be an expectation).
- It knows when to leave: a plain-letter form for the ASCII charset (a serial
  console rendering U+2588 as mojibake is worse than plain letters), and
  nothing at all on a short or narrow window -- those are the terminals where
  every row is spoken for, and they get the menu entries instead. The working
  screens never carry it; they spend their rows on work.

## [0.48.4] - 2026-08-17

### Fixed

- The final screens quit the program. Left over from when the installer was the
  whole program: pressing the button on Finished -- or on a saved document --
  exited, throwing the operator out at exactly the moment they want the run
  list, the settings, or a second cluster. Both return to the start menu now,
  and Quit stays on the bottom-left exit, where leaving is a choice rather than
  the only way forward.
- Returning clears the run's view state -- the phase list, the log tail, the
  failures -- because a second install folding its events on top of the last
  run's would draw two runs as one. The collected configuration stays: walking
  the flow again with the same answers is the common case, not an accident to
  be wiped.

## [0.48.3] - 2026-08-16

### Changed

- The product has a working name: **Malmok (말목)** -- a stake driven into the
  ground, which is what this tool does to a cluster. The TUI title bar, README
  and CLAUDE.md carry it. The CLI, the binary and the module path stay
  `platformctl`: a rename there touches every import and every document that
  says `platformctl apply`, and a working title is not the moment for that.

## [0.48.2] - 2026-08-16

### Added

- A regression test pinning the address-first rule: a document with no
  domainSuffix, HTTP-only listeners and `pki.mode: none` validates, and the
  moment certificates are issued the suffix becomes required. The validator
  already behaved this way; the test keeps it that way, because a first build
  is reached by address until DNS exists and the wizard's own "decide later"
  answer must not produce a file the validator refuses.

## [0.48.1] - 2026-08-16

### Fixed

- RKE2 v1.36 replaced the EOL'd ingress-nginx with a bundled Traefik -- the
  succession ADR-005 predicted, under a new name the disable list did not
  cover. After the 1.36 upgrade its two install jobs sat crash-looping: the CRD
  chart tried to take Helm ownership of the Gateway API CRDs this tool had
  already installed ("exists and cannot be imported"), and the main chart then
  failed for the missing CRD release.

  On the preset whose gateway is Cilium both components are now disabled --
  `rke2-traefik` and `rke2-traefik-crd`, because the CRD chart is its own
  component and disabling only the consumer left the CRD installer
  crash-looping alone, verified live. The `*-traefik` presets keep the bundle:
  there, it is the gateway. On versions that ship no such component the entries
  match nothing, which is what makes them safe to state for the preset.
  Verified live: a re-apply rewrote config.yaml, restarted the server, and RKE2
  removed both charts and their jobs; the Gateway API CRDs stayed ours.

## [0.48.0] - 2026-08-16

### Added

- `gateway.gateways[].exposure: node-ips`. A pool address is one more IP
  answering on the segment, and IDC and air-gapped network policy frequently
  allows only the addresses the nodes already hold -- the same site
  `acceptNodeRegistration` exists for. The gateway answers on the nodes' own
  addresses instead: every node by default, or the subset `nodeIPs` names.
  Verified live -- the test cluster's gateway serves on both node IPs at port
  80 with no LB pool anywhere.
- Implemented as Cilium's host-networked gateway, because the obvious spelling
  does not work: the generated Service is owned by the gateway controller,
  which strips a patched `externalIPs` on the next reconcile (verified live),
  and its endpoints are eBPF-wired rather than selector-backed, so no parallel
  Service can reach them. Envoy binds the listener ports in the host namespace
  instead, which is the thing itself.
- The verification is the thing itself too: every named address must answer on
  the listener port. Cilium never calls a host-networked gateway Programmed --
  there is no pool address to program -- so waiting on that would wait forever
  on a gateway that works. Any HTTP status counts; a 404 from a gateway with no
  routes is the gateway working.
- The contract ConfigMap and the DNS record sheet carry the node addresses: one
  A record per node under the same name, which is how DNS spreads traffic
  across them and how a node that leaves takes exactly its record with it.
- Mixed exposures are refused. Cilium's host networking is cluster-wide, so one
  node-ips gateway moves every gateway's envoy into the host namespace; a
  load-balanced gateway beside it would silently stop being what the document
  says.

### Fixed

Five defects, four of them found live and each invisible until the packet had
to actually arrive.

- Cilium's agents were never restarted after a values change. RKE2's helm
  controller upgrades the chart, but a change that lands in cilium-config is
  read at agent start and nothing rolls the agents -- host networking enabled,
  port 80 bound nowhere, agents five days old. The dataplane phase now compares
  the config file's change time against the DaemonSet's restartedAt stamp and
  rolls when the config is newer: the same defect, and the same fix, as the
  rke2 service step.
- The first version of that check listed pods, and a listing taken during a
  rollout still contains terminating pods with the old start time -- a finished
  restart read as unfinished. The DaemonSet's own record replaced it.
- The operator restarts too, and comparing only the agents' stamp skipped it.
  The operator is what turns a Gateway into Envoy configuration; a stale one
  keeps regenerating the old listeners, so the agents were current, the
  configuration was current, and the listener was still on the old proxy port.
- Binding a privileged port in the host namespace takes both halves of a
  capability: NET_BIND_SERVICE granted to the container, and
  `keepCapNetBindService` telling cilium-envoy-starter to retain it. With only
  the first, CapBnd holds the bit, CapEff is zero, and envoy NACKs the listener
  forever with "cannot bind: Permission denied".
- The DNS sheet deduplicated records by name and type, which silently dropped
  every A record after the first for the one case that needs several.

## [0.47.0] - 2026-08-13

### Added

- The operator gets a kubeconfig. RKE2 writes /etc/rancher/rke2/rke2.yaml
  root-only, which is correct for the file and useless for the person: the
  account this tool logged in as -- the account somebody runs kubectl or k9s
  from five minutes later -- could not read it, and the cluster looked broken
  from the very machine it was built on. Found on the live server, where k9s
  could not connect.

  Bootstrap and every joining server now install a copy at ~/.kube/config,
  owned by the login account, mode 600. A copy rather than a symlink or a
  group, because those change the security of the original. On a local node the
  account is whoever sudo elevated, resolved at run time; root gets no copy
  because the original is already root's to read. Verified live: the k8s
  account on 192.168.88.241 runs kubectl with no elevation.

- `topology.acceptNodeRegistration`. A registration address on a node's own IP
  is the trap ADR-008 closes -- and it is also the only address some sites are
  allowed: an ARP VIP is a second IP answering on the segment, which IDC and
  air-gapped network policy frequently forbids, and a DNS name needs a zone
  somebody may write to. The trade -- promoting to HA later means re-joining
  every node -- can now be taken by stating it in the document, never by
  default, and never alongside a VIP that makes it pointless.

- The wizard spells the same statement as an empty join address: leave it blank
  and the first server's own address is used with the trade recorded.

## [0.46.1] - 2026-08-13

### Fixed

The registry and certificate screens, audited the same way the profile was:
every value their validators can demand is now enterable, and every decision
their phases read is now on screen.

- `byo-cert` fell through to the private-CA fields, asking for an issuing CA's
  key on a build that issues nothing -- and producing a document the validator
  refuses with no screen able to fix it. It asks for the certificate, the key
  and the issuing CA now.
- An air-gapped build must name a bundle or a registry address, and only the
  address had a field: the build that carried a Hauler bundle instead was
  unbuildable from the wizard. The bundle path appears when the network mode is
  airgap.
- The ACME server was not asked, so staging could not be told apart from
  production -- and a mistake against production spends a rate limit that
  resets in a week.
- Trust distribution is a choice on the private-CA screen. l2-pki reads it, and
  a cluster built without it has a CA the nodes trust and the pods do not; the
  failure is an opaque x509 error from inside a container.
- Accepting an unverified registry certificate is a visible two-row decision on
  the modes that point at a registry. Written into the document only when
  chosen, so the file that means it is distinguishable from the ones that never
  thought about it.

## [0.46.0] - 2026-08-13

### Changed

- The wizard composes; it no longer asks which of six canned combinations to
  start from. The profile chooser is gone, and every axis it used to fix is a
  choice on the screen that owns it: the operating system with the nodes, pod
  routing and reachability and node encryption on the network screen, the
  dataplane with its fallback and the downgrade policy on the options screen.
  v0.45.0 made the profile overridable; this removes the question itself,
  because choosing a combination first made every later screen an override of a
  decision nobody wanted to make.
- The profile is derived, not chosen. The summary and the document name the
  validated baseline the composition equals -- `custom` when it equals none,
  which is a true statement: Tier-3 means the combination is not one CI
  exercises. Composing a baseline axis by axis is recognised as that baseline,
  and the audit trail keeps its vocabulary.
- Editing a document resolves its profile's values into the axes first, so a
  file that relied on a baseline is shown as what it effectively is and is
  written back meaning the same thing.

## [0.45.0] - 2026-08-13

### Fixed

- A profile was a lock, not a starting point. The network mode came from the
  baseline and from nowhere else, so a combination no profile happens to contain
  -- a homelab behind a proxy, an air-gapped site that refuses to downgrade --
  could not be produced from the wizard at all. The profile screen has always
  claimed the opposite: "the profile fixes the validated baseline; every other
  setting is an override."
- Three settings a profile fixed now have screens. Reachability
  (online / proxy / airgap) and node-to-node encryption on the network screen,
  and what to do when a node cannot run the dataplane (confirm / auto / forbid)
  beside the dataplane it is about.
- The proxy fields follow the chosen mode rather than the profile's, so
  selecting `proxy` reveals them wherever the build started from.
- A test walks `spec.Baseline` and fails on any field that is neither offered on
  a screen nor listed as deliberately document-only. Adding a baseline field
  without deciding which side it falls on is what produced this.

## [0.44.2] - 2026-08-13

### Fixed

- The checks screen was blank and would not move on. Work that fails before the
  engine emits anything leaves nothing in the stream to draw, so the screen was
  a progress bar at 0% with no explanation -- and the wizard was holding the
  reason the whole time, using it only to decide which button to show.

  That is the shape of every credential and connection failure, which is the
  most common way a first run stops. The reason is now on the screen. It is
  rendered from the return value rather than from an event because the failure
  happened before any phase started: there is no phase to attach it to and no
  run for the engine to have written it into.
- With nothing in the stream, Back is the primary action rather than Retry.
  Nothing ran, so the checks found no problem -- something stopped them from
  starting, retrying repeats it exactly, and the way forward is the screen that
  holds whatever was missing. Retry stays primary once probes have reported,
  because then there are findings and running them again is the useful thing.

## [0.44.1] - 2026-08-13

### Fixed

- Changing the character set changed the language. Switching to ASCII forced the
  catalogue to English, so on the settings screen choosing ASCII moved the row
  above the cursor and left the operator reading English they had not asked for.

  The reasoning behind it was that a terminal which cannot draw a box character
  cannot draw Hangul either. That is sometimes true and it was never this
  switch's business: somebody who asked for Korean asked for Korean, and
  somebody who cannot read the result can change it on the screen that now
  exists for the purpose. English is still the default, which is what covers the
  terminal that genuinely cannot render it.

## [0.44.0] - 2026-08-12

### Added

- A Settings entry, for what the word means: how the screen looks. Language,
  character set and the step list, kept between runs in the user's config
  directory. They are the three things `g`, `a` and `s` already toggled; the
  screen exists so they can be found without knowing them, and so the answer
  survives the session.
- A flag still wins over a saved preference, but only when it was actually
  passed. A default that silently overruled a choice made on screen would make
  the setting look broken.

### Changed

- The language is no longer the first question of an install. It is a property
  of the person reading the screen, not of the cluster being built, and asking
  it there meant it could only be changed by starting a build. The install flow
  now opens on the profile.
- The old Settings entry is called Edit a document, which is what it does: it
  opens an existing `cluster.yaml` and changes it. Calling that Settings was
  what left the actual settings with nowhere to live.

## [0.43.2] - 2026-08-12

### Fixed

A pass over every Korean screen, not only the new ones.

- `에이전트` was used for an SSH agent in the credential hint, and an agent is a
  worker node everywhere else in this tool -- the worst place for that
  collision. It is `ssh-agent` now.
- Writing a document was `기록`, which is what the event log does. Saving a file
  is `저장`; one word for both made the save screen sound like it had appended
  to a log.
- `폐쇄망 번들에서 서빙` is not Korean. `누르기`, not `실행`, for activating a
  button -- `실행` is what this tool calls a run. `공유 경로`, not
  `내보내기 경로`, for an NFS export.
- The final screen asked three questions where Korean wants noun phrases:
  `무엇이 구축되었나` / `어디에 남았나` / `무엇이 실패했나` are `구축 결과` /
  `산출물 위치` / `실패한 항목`.
- `run` was left in English inside Korean sentences on the same screen that
  calls it `실행 기록`, which made one concept look like two.
- Spacing: `진행중` -> `진행 중`, `단계목록` -> `단계 목록`.

### Changed

- The summary screen follows the same rule the node screen does: it lists an
  SSH user only when something is dialled. A build that opens no connection was
  being summarised with a credential for a step that is not going to happen.
- The profile row on the summary and final screens is labelled from the rail
  rather than from the profile screen's title. A heading reused as a row label
  reads as a heading -- in Korean that row said `프로파일 선택`, "choose a
  profile", beside the profile that had already been chosen.

## [0.43.1] - 2026-08-12

### Fixed

- The Korean screens called a server a "기계". Nobody working with servers says
  that, and the catalogue already says 서버 and 노드 everywhere else -- the new
  strings were translated word by word from the English instead of written in
  Korean, which is how a line comes out grammatical and still not something
  anybody would say. `이 기계` / `다른 기계` are now `이 서버` / `원격 서버`,
  and the same pass corrected `마법사` back to `설치기`, which is what the rest
  of the catalogue calls this tool.

## [0.43.0] - 2026-08-12

### Added

- The wizard asks which machine it is building on, before it asks for any
  address. It is the first thing an operator knows and it decides what every
  screen after it has to ask; skipping it meant somebody installing on the
  machine in front of them had to read their own IP off `ip addr` and type it
  back -- a value the tool was sitting on the whole time.
- This machine is the default, because an installer is normally run on the
  machine being installed. A host with no routable address is not offered it:
  the address is what the cluster advertises.
- The address is chosen from the ones the machine reports, never typed. A
  multi-homed host is a real case where which address the cluster advertises
  matters (PF-609), and typing is not what should decide it.
- Choosing another machine clears the address that was right for here, because
  leaving it would be the wizard suggesting a node that does not exist.
- The branch is not written into the document. The address is this machine's, so
  a document read back here routes locally on its own -- and the same file
  copied to another machine falls back to SSH rather than silently installing
  onto the wrong box.

### Fixed

- The failure list on the final screen truncated the node off. It read
  `l1-bootstrap 10.10....`, and the machine a failure happened on is the one
  thing an operator acts on. The node comes first now and the phase gives way.

## [0.42.1] - 2026-08-12

### Changed

- The wizard's node screen asks for SSH credentials only when something is
  dialled. An SSH user and port on a screen where nothing is connected to are
  two questions with no answer, and worse, they read as though the tool were
  about to log in somewhere. One node that is not this machine is enough to
  bring them back: the credentials are asked once and used for every
  connection.
- The password stays either way and says which it is. Over SSH it logs in and
  then elevates; locally it only elevates, and it is still needed, because an
  account that cannot elevate answers "no" to every privileged question
  (`v0.42.0`). It is labelled `sudo password` there, and the hint says to leave
  it empty when platformctl is already running as root.
- The topology panel marks the node that is this machine. Which address the
  operator is sitting at is the one thing a diagram of addresses cannot show,
  and it decides whether a connection is opened at all.
- A cursor left past the end of a screen that shrank is pulled back. The node
  screen loses two rows the moment every address turns out to be local, and a
  cursor beyond the last row highlights nothing while Enter does something other
  than what the screen says.

## [0.42.0] - 2026-08-12

### Added

- A node can be the machine platformctl is running on. This is the ordinary
  case, not the special one: an installer is normally run on the machine being
  installed, and reaching another machine over SSH is the addition. Until now
  installing onto the host you were sitting at needed an sshd, an account and a
  credential for your own box -- a round trip through the network stack to reach
  a filesystem that was already open.
- The routing is by address, not by a flag: an address either is or is not
  assigned to an interface on this machine, and that is a fact nothing has to be
  told. `local` and `localhost` also work for a document written before anybody
  knows the address.
- `local` on a node overrides it in both directions, because both are real. True
  where the address is not one this machine holds -- a node behind NAT, or one
  named by the VIP it will carry once the cluster is up. False where it is: a
  tool running in a container with host networking sees the host's addresses and
  is not the host, and the address alone cannot tell the two apart.
- `sudo platformctl` needs no second sudo. The local runner reports that it is
  already root and the elevation wrapper leaves it alone, which matters on an
  image that has no sudo binary and an account in no sudoers file.
- `examples/cluster-local.yaml`.
- A node named only by a loopback address is refused unless it also sets
  `nodeIP`. That address says how to reach the node, not what the cluster calls
  it, and the registration address, the certificate SANs and the address
  advertised at join all come from the latter.

### Fixed

- An account that cannot elevate was reported as a machine that cannot run
  Kubernetes. `sudo -n` without a password exits non-zero having never run the
  command, and a non-zero exit is how every probe spells "no" -- so a host whose
  sudo wanted a password reported no BTF, no bpf filesystem and no module
  support. Verified on a machine where `/sys/kernel/btf/vmlinux` exists and
  `bpf` is in `/proc/filesystems`: the tool said neither did. Those are
  measurements of the account presented as measurements of the kernel, and
  `PF-202` and `PF-203` both feed a downgrade decision.

  Elevation is now proved once, at connect time, and a failure says so in one
  sentence naming what to do about it instead of arriving as three dozen wrong
  answers.

## [0.41.0] - 2026-08-12

### Added

- `platformctl upgrade --to <version>`, and the start menu's Upgrade entry that
  drives it. Servers first, then agents, one node at a time: a kubelet must
  never lead its API server, and a control plane that restarts two members at
  once is a restore rather than a retry.
- A new code family, `UP`, for upgrade preconditions. They are measured before
  anything is touched, which makes them preflight in character, but they answer
  a different question -- a `PF` code says whether an install will work here, a
  `UP` code says whether this step is legal from where the cluster already is.
  Filing them together would make every preflight inventory a mix of the two.
- `UP-001` to `UP-005` are the skew rules and each one is somebody's outage: a
  malformed version, a downgrade (etcd has none), a skipped minor, a node
  already past the target, and an agent that leads its servers. `UP-101` to
  `UP-103` are the readiness ones: every node Ready before the first goes down,
  a single-node cluster told its workloads restart in place, and a warning when
  there is no recent etcd snapshot to go back to.
- Each node is drained before its kubelet restarts and uncordoned once the
  cluster agrees it is back. A disruption budget is honoured unless `--force`
  says otherwise: a budget is somebody's statement about how much of their
  service may be down, and the tool is not the one to overrule it.
- Verified by upgrading the live two-node cluster from v1.34.10+rke2r1 to
  v1.35.7+rke2r1.

### Fixed

Three defects the live upgrade found, all of them the same shape as before -- an
observable that was not the thing that had to be true.

- The drain counted the pods it had just failed to move. It excluded
  `kube-system/etcd-` by name and left the API server, the scheduler, the
  controller manager and the cloud controller manager counted, so a server that
  drained perfectly reported five pods still to go. Counted by owner now:
  a drain does not evict DaemonSet pods or mirror pods, which is a property, not
  a naming convention.
- The restart step compared modification time. The installer unpacks a tarball
  and tar restores the archive's timestamps, so a freshly installed binary
  carries the mtime of the day upstream built it -- eight days older than the
  running unit. The check called the service current, nothing restarted, and the
  node went on serving v1.34.10 with v1.35.7 sitting beside it. Change time is
  set by the filesystem when the inode is written and no archive can carry it.
- The skew rules read the binary on disk as though it were the running version.
  Those differ exactly when an upgrade is half done, so the tool refused to
  finish the upgrade it had itself started: "the servers run v1.35.7 and the
  target is v1.35.7". The rules are about the kubelet version the control plane
  reports; the installed version is carried alongside and shown, so a node
  between the two is described rather than mistaken for a finished one.
- The event stream's step field carried the whole step id -- fixed in 0.39.1 and
  the reason every line in this release reads `upgrade-server/drain/<node>`.

## [0.40.0] - 2026-08-12

### Added

- The start menu's Settings entry works. It opens a `cluster.yaml`, walks the
  same screens the install flow uses with the values already in them, and writes
  it back. Nothing is applied to a cluster -- the screen says so, because an
  operator who wrote a file and believes a node changed is the failure that
  sentence exists to prevent.
- The loaded document is kept whole and the wizard writes only over the fields
  it owns (`Config.ApplyTo`). This is the whole feature. The wizard has no
  screen for gateway listeners, etcd snapshots to S3 or the GitOps repository,
  and a document rebuilt from the answers would have deleted every one of them:
  what the operator was never shown, they did not agree to delete. Verified
  against the live homelab document -- VIP, trust distribution, gateway,
  bootstrap apps and the SSH password reference all survive an edit untouched.
- Nodes are matched to their old selves by host, and failing that by position,
  so a node renamed in place keeps its SSH key reference. A node carries more
  than its address.
- The document list marks a run's snapshot as one, because editing it is how an
  operator loses the ability to resume that run.
- Validation on the save screen is run against the document that will be
  written, not against a reconstruction of it. A listener with no port lives in
  the part no screen shows, which is exactly the part a reconstruction lacks.

### Changed

- The order of the wizard's screens is a list per flow rather than arithmetic on
  the step numbers. Two flows cannot both be expressed by one run of consecutive
  values, and stepping by `+1` is how a screen inserted in the middle silently
  renumbers the rail.

### Fixed

- `pki.mode: none` dropped every agent. `ToSpec` returned early to clear the
  domain, and the agent list was built after that point -- so choosing "decide
  later" quietly produced a single-node cluster.
- A validation message named the wrong screen. Problems are labelled with the
  step that owns them, and the label was read out of a list indexed by the
  step's own number, which the step numbers were never positions in.
- Switching PKI mode left the old material behind. A private-ca document that
  still carries an acme block is not inert: the loader resolves every SourceRef
  it finds, so a leftover token reference fails the run before it starts.

## [0.39.1] - 2026-08-12

### Fixed

- Every rendered step line repeated the phase, and node steps repeated the node
  too: `l0-node-prep/l0-node-prep/modules@192.168.88.241/192.168.88.241`.

  The engine emitted the step *id* into the event's `step` field. The id has to
  carry the phase and the host, because state.json keys on it and a resume must
  find the same step again on the same node -- but the event schema gives
  `phase`, `step` and `node` fields of their own, and its own example writes the
  step as `rke2-server-ready` (docs/11-execute.md §5.1, §5.5). A renderer that
  joins the three fields it was given cannot know that one of them already
  contains the other two.

  Fixed where it was wrong, in the engine, rather than by teaching the renderer
  to un-pick a string. The id is unchanged, so resume still matches.

## [0.39.0] - 2026-08-12

### Added

- `l2-platform`, as GitOps only. The catalogue files four things under this
  phase -- ArgoCD, observability, secrets and the upgrade controller -- and only
  the first is implemented. That is a boundary rather than an unfinished list:
  ArgoCD is what the other three should be installed *by*. A platform tool that
  installs every add-on itself has to be re-run to change any of them, which is
  the coupling ADR-002 and ADR-006 both exist to prevent. This tool builds the
  cluster up to the point where GitOps can take over, and hands over there.
- The phase ends by waiting for a bootstrap Application to reach Synced and
  Healthy. A HelmChart that deployed proves nothing; an Application that
  reconciled proves the API accepted it, the repository was reachable, the
  credential worked, the manifests rendered and the cluster ran them.
- Verified against the live cluster: ArgoCD pulled `guestbook` from a Git
  repository it had never seen and deployed it, and reports it as Synced.
- `fullnameOverride: argocd`. The chart names objects `<release>-<chart>-<part>`,
  which would give `argocd-argo-cd-server` -- a name in no ArgoCD runbook. The
  release name alone was enough for cert-manager and is not enough here.
- The registry credential reaches a chart repository only when the repository is
  on that registry. ADR-007 puts the charts and the images in the same Harbor,
  so reusing the credential is right there and nowhere else -- sending Harbor's
  password to github.com because both appear in one document is how a credential
  leaves the place it was meant for.
- An OCI bootstrap app must name its chart version. The version is not defaulted
  to the newest, because an unpinned chart means the bundle that crossed the air
  gap and the cluster disagree about what is installed.
- Dex is not installed. This tool has no way to configure SSO, so it would be a
  pod that can never do anything and an image an airgap bundle would carry for
  it.

### Changed

- `platform.gitops.source` alone no longer installs anything. The profile
  baselines set it for every profile, so treating it as the ask would put a
  GitOps controller on every cluster this tool builds, including the ones whose
  operator never wanted one. `enabled` decides; a configured repository is taken
  as the ask when it is unset.

### Notes

- ArgoCD gets no HTTPRoute. ADR-006 keeps routes with the application that owns
  them, and enabling the chart's own route would be the same act at one remove.
  The UI is reachable by `kubectl port-forward` until somebody routes it.
- Bootstrap Applications carry no `resources-finalizer`. Removing an entry from
  the document says this tool should stop managing the app, which is not the
  same sentence as "delete the workload".

## [0.38.0] - 2026-08-11

### Added

- `l2-pki`: cert-manager, the issuer `pki.mode` implies, and the trust
  distribution that makes a private CA usable. Verified end to end against the
  live cluster -- a Certificate issued in three seconds, signed by
  `CN=Ryxen Homelab Issuing CA`, and the CA bundle synced into seven
  namespaces.
- A ClusterIssuer rather than a namespaced one: applications live in namespaces
  this tool does not know about, and an Issuer they cannot reference is one
  nobody uses.
- The issuing CA's key is applied from a temporary file under `umask 077`,
  never written into the manifest directory, and the step compares the
  certificate's fingerprint so a rotated CA is noticed without the key ever
  being read back.
- An unknown DNS-01 provider is rendered as a webhook solver rather than
  guessed at. Inventing a configuration for a provider this tool has never seen
  would produce an issuer that is Ready and cannot solve.
- `rke2.AcceptsStep` and `rke2.ManifestStep`, extracted from three copies each.

### Fixed

Four defects the live run found, all of them the same shape -- an observable
that was not the thing that had to be true.

- Ready replicas are not a usable controller. cert-manager's webhook gets its
  serving certificate after the pods start and its CA bundle is injected later
  still; in between everything reports ready and the first object created fails
  with `x509: certificate signed by unknown authority`. The wait is now a
  server-side dry run of the very object about to be created, which goes
  through the API server, the CRD registration and the webhook -- the whole
  path that has to work -- and changes nothing.
- Writing a manifest is not applying it. RKE2's deploy controller is
  content-hash driven, so rewriting an identical file does not restore an
  object somebody deleted: the record still says "applied". Every manifest step
  now applies as well as writes -- the directory is what the cluster reconciles
  from on restart, the apply is what makes this run true.
- A step whose Apply is itself a bounded wait is no longer retried. Three
  attempts at a ten-minute wait took thirty minutes to say what was wrong.
- The Helm release name is the upstream one. RKE2 uses the resource name as the
  release name and it prefixes every object the chart creates, so a
  `platformctl-` prefix produced deployments whose names appear in no
  cert-manager runbook. It also cannot be changed afterwards: Helm stamps the
  release onto cluster-scoped CRDs, and a rename leaves them owned by a release
  that no longer exists -- deleting the namespace does not help, because the
  CRDs are not in it.

## [0.37.1] - 2026-08-11

### Fixed

- PF-502 reported the tool's own latency as clock drift. The nodes are read one
  after another over SSH, and subtracting raw times counts the gap between two
  reads as skew -- which blocked a build on a live cluster whose two nodes were
  both NTP-synchronised and 1ms apart. It now measures each node's offset from
  this machine at the midpoint of the round trip, and refuses to call a
  difference drift when it is smaller than what the measurement could resolve.
  Blaming the cluster for the measurement is worse than saying it could not be
  measured.
- A failing step's message was truncated from the front, so a failure opened
  mid-way through a pod listing with the sentence saying what happened cut off.
  Every step here prints the explanation first and the diagnostics after it, so
  the head is what has to survive.
- `engine.FailedStep` replaces two copies of the same idea and one that did not
  work: a step meaning "this cannot be satisfied" was written as a shell command
  exiting non-zero, which a fake runner answered `0` to. A contract of "never
  satisfied" must not depend on a shell, a fake, or anything else that could
  answer differently.

## [0.37.0] - 2026-08-11

### Added

- The wizard installs. Choosing Install and pressing through the screens now
  runs the same pipeline `apply -f` runs, on the same engine, writing the same
  events -- the wizard is a way of producing a document, not a second installer
  (ADR-002). It used to reach the last screen and tell the operator to go and
  use the command line.
- `internal/build`, which owns that path. It lives outside `cmd/` so it can be
  exercised against real nodes without a terminal: verified end to end against
  the live two-node cluster, four phases and the artifacts written, using
  exactly the two functions the wizard's screens call.
- The checks and the install share one session. They are separate screens but
  one visit to the same nodes, and opening a second set of connections would
  let the checks pass on a connection the install then fails to make. The
  measurements are kept for the same reason: measuring twice would let the two
  disagree about one cluster.
- A downgrade is decided by the document's own `downgradePolicy`. The wizard
  cannot ask, so `auto` proceeds and `confirm` -- the default, and what the
  airgapped profiles set -- stops and says what it would have done and how to
  accept it. Deciding silently would be the tool choosing on the operator's
  behalf at the one moment they are watching.
- Downgrades are emitted as `kind=decision` events, so they reach the screen
  and the audit report rather than only the return value.

### Removed

- `cmd/platformctl/apply_preflight.go`. Both of its functions were superseded:
  `apply -f` runs the whole pipeline, and the wizard no longer stops before
  installing.

## [0.36.0] - 2026-08-11

### Added

- A start menu. The installer used to be the whole program: opening the tool
  put an operator on the first question of a new build with no way to reach
  anything else, which is wrong for a tool whose work is mostly not first
  installs -- a cluster is built once and then upgraded, reconfigured and
  looked at for years.
- Entries for install, upgrade, settings, runs and quit. Upgrade and settings
  are listed and say they are not built yet, with a line naming what each still
  needs. Hiding them would make the tool look finished; letting them open an
  empty screen would send an operator hunting for something that is not there.
- The run list reads what previous builds recorded and replays one into the
  install screen. That screen is already a renderer of the event stream, so
  replaying is the same code path as watching a run live -- ADR-002 showing up
  as a feature rather than as a diagram. Reaching it from the menu means
  somebody who opened the tool to find out what happened last week does not
  have to know `attach` exists.
- A finished run is folded in one go rather than paced: an animation of
  something that already happened is not progress.

### Changed

- The title bar says `platformctl` rather than `platformctl installer`, which
  stopped being true the moment the menu offered anything else. The rail, the
  step counter and the rail toggle appear only inside the install flow --
  numbering the menu would make arriving at the tool look like step one of a
  build, and a footer that advertises a key doing nothing stops being read.
- Back from the first install step or from the run list returns to the menu
  rather than walking backwards into it. An operator who chose the wrong entry
  wants the menu, not the previous question of a flow they are leaving.

### Fixed

- The menu's Open button rendered, took focus and did nothing: `activate` did
  not know its label. `TestEveryButtonIsWired` now asserts that every button any
  screen draws maps to an action. A button that looks live and is not is worse
  than a missing one -- the operator concludes the tool is stuck rather than
  that the feature is absent.

## [0.35.0] - 2026-08-10

### Added

- `internal/report` and `platformctl report`: the audit report of
  `docs/00-architecture.md` §7 and the DNS record sheet, written into the run's
  `artifacts/` directory and produced automatically at the end of `apply`.
- Built from the run directory and nothing else. A report generated from live
  cluster state would say something different every time it ran, and what a
  customer receives has to be the record of what happened -- which is also why
  it can be produced again from an old run, on a machine that never touched the
  cluster.
- The plan is now saved as `plan.json` beside the document. The downgrade
  history is the section §7 singles out, and it cannot be reported from a plan
  that only ever existed in memory.
- Every section is rendered whether or not there is data for it, and the
  security section names what it did not produce. A reader who cannot tell
  "scanned and clean" from "never scanned" has been misled by the report rather
  than informed by it.
- The certificate section leads with PV-002 rather than burying it in a table:
  it is the strict profile, and passing it is what says Java, Go, curl and
  Python accept the chain.
- The DNS record sheet derives what the customer's DNS team has to create --
  the join address pointing at the VIP, each listener hostname, a wildcard for
  the domain suffix, and whatever the document adds. A literal registration
  address produces no record, and a gateway with no pinned address produces no
  sheet: asking for a record that points nowhere is worse than asking for none.

### Fixed

- Every agent was reported as a server. A node's entry usually leaves the role
  empty and says what it is by which list it appears in, so flattening the two
  lists without filling it in mislabelled the whole worker pool.
- The security section repeated one row per node with no way to tell which node
  it was about.

## [0.34.0] - 2026-08-10

### Added

- `platformctl apply -f cluster.yaml` builds a cluster. Every phase existed and
  none was reachable from the command line; a document now runs measure →
  decide → build → verify end to end. Confirmed against the live two-node
  cluster: 107 checks, five phases, every step satisfied on a re-run.
- `internal/catalogue`, which owns order and nothing else. The phase packages
  each know one thing and nothing about sequence, and order is most of the
  correctness here -- the trust store before L1 pulls an image, the first
  server Ready before anything joins it, the dataplane reconfigured before a
  Gateway is created against it.
- kube-vip belongs to `l1-bootstrap` rather than a phase of its own: the
  address it serves is what every later join uses, so the phase that brings up
  the first server has to finish with it answering.
- Connections are opened for every node before anything runs and shared with
  preflight. A build that discovers on its third phase that it cannot reach a
  node has already changed the first two, and preflight passing on a connection
  the build then fails to make is worse than either.
- The phase grades are printed before anything runs, and a `disruptive` phase
  needs `--approve`. That a dataplane change replaces kube-proxy and rolls
  every pod is not something to discover during a maintenance window.
- Preflight findings and the wire checks are written into the run's event file,
  not only to the terminal. The audit report is built from that file, and a
  finding that scrolled past is one nobody can produce six months later.
- A step that cannot be built -- an unreadable token, a VIP interface that will
  not resolve -- becomes a step that fails rather than a phase that quietly has
  no work. A run that reports success for work nobody did is the worst
  available outcome.

### Fixed

- A completed run reported "4 failure(s)" for four advisories. A probe that did
  not pass and did not block is worth reading; a step that failed stopped its
  phase. The summary now counts them separately, and the split is on what the
  event is rather than on its status alone -- a failed step is not an advisory.

## [0.33.0] - 2026-08-09

### Added

- `internal/verify`: the wire checks of `docs/20-cert.md` §6.5, PV-001 through
  PV-008. All eight were defined and none implemented; the structural test that
  catches that drift for the preflight codes did not cover the verification
  family, so it had already happened.
- The verifier is the strictest client by construction. Go's `crypto/x509`
  never fetches an issuer over AIA, which is exactly the profile Java, curl,
  Go and Python present -- so passing PV-002 here means passing for all of
  them. Nothing shells out to `openssl`; the handshakes are native.
- PV-002 additionally retries with AIA supplementation and says so when that is
  what makes the difference. That is the incident the document records: a
  bundle browsers accept and a Spring application rejects. The message names it
  rather than leaving somebody to discover it in production. It is reported as
  context only -- ADR-010 forbids browser behaviour as evidence.
- PV-003 checks the whole served chain, not the leaf. A cross-signed
  intermediate left behind after its own expiry has a current leaf and only
  strict clients notice.
- PV-004 treats unreachable revocation points as a warning with a note, because
  a disconnected site cannot reach an OCSP responder and that is expected --
  what matters is that the handover says a client configured to hard-fail will
  refuse the connection.
- PV-007 probes each listener hostname separately. A gateway can serve the
  right certificate for one listener and the default for the rest, and nothing
  in its status says so. A wildcard listener is probed with a concrete label,
  since `*.acme.internal` as SNI matches nothing.

### Fixed

- PV-002 reported every correctly assembled chain as missing a certificate. The
  root is deliberately not served (§3.1), so counting the anchor above the
  topmost certificate as a gap made a complete chain look broken. It now
  separates a gap *inside* what the server sent -- fix by adding the
  intermediate -- from an anchor nothing trusts -- fix by distributing the root.
  The two wear the same x509 error and have opposite fixes, and sending an
  operator after an intermediate they already have is worse than saying nothing.

### Verified

Against the live gateway at `192.168.88.216:443`: without the private CA,
`ROOT_NOT_TRUSTED` naming the root and how to distribute it; with it supplied,
PV-002 passes. PV-005 found the gateway refuses a handshake with no SNI, which
is real and worth knowing before a health checker meets it. PV-008 recorded
TLS 1.2 and 1.3 accepted, 1.0 and 1.1 refused.

## [0.32.0] - 2026-08-09

### Added

- `l2-gateway`: Gateways, their listeners, the TLS Secrets those listeners
  serve, and the contract ConfigMap application charts read to find them.
  Verified end to end -- a Gateway Programmed at `192.168.88.216` with both
  listeners `ResolvedRefs/Accepted/Programmed=True`, and `openssl s_client`
  against `:443` returning the certificate `internal/cert` assembled.
- The TLS Secret is applied from a temporary file under `umask 077` with a
  trap, never written into RKE2's manifest directory. A manifest there holds
  the private key on the node's disk for the life of the cluster; applying it
  once puts the key in etcd, where it was going anyway, and leaves nothing
  behind. The cost is that RKE2 does not reconcile it, which is the right
  trade: a Secret does not drift, and re-running the phase reinstates it.
- The Secret's check compares a fingerprint annotation, so a renewed
  certificate is detected without the key ever being read back and the step's
  evidence stays safe to put in an audit report.
- The contract ConfigMap (ADR-006). This tool creates no routes, so a chart has
  to be told the gateway's name, namespace, address and domain -- otherwise
  every chart hardcodes them and moving a gateway means editing every chart.
- `allowedRoutes` follows `routeNamespaces`, and a document that says nothing
  gets `Same` rather than `All`. Which namespaces may attach a route is the
  multi-tenant boundary, and the closed answer is the one to default to.
- Secrets are installed before Gateways. A listener referencing a Secret that
  does not exist reports `Programmed=False` with a reason naming the reference
  rather than the missing material.
- The phase waits for `Programmed`, not `Accepted`. Accepted means the
  controller agreed to implement the Gateway; Programmed means it has an
  address and a bound listener, and the gap between them is where a missing
  load balancer pool or an unusable certificate shows up.

## [0.31.0] - 2026-08-08

### Added

- `l2-dataplane`. ADR-004 binds the CNI, the Gateway controller and the source
  of load balancer addresses into one choice; this is where that becomes true
  on a cluster. Verified end to end: a Gateway created against the finished
  cluster took `192.168.88.216` from the pool, reported `Programmed=True`, and
  answered ARP from the other node with the control-plane's MAC.
- Gateway API CRDs pinned to `v1.4.1` -- what Cilium 1.19 documents, not the
  newest release. A newer bundle means CRDs whose fields the controller ignores,
  which presents as a Gateway that accepts a configuration and never implements
  it. The observable is the bundle-version annotation, so a cluster carrying an
  older bundle is detected rather than assumed correct.
- The experimental channel is installed only when a listener asks for TLS
  passthrough, which is the one thing needing TLSRoute. Otherwise alpha types
  end up in a customer's cluster for no reason.
- `k8sServiceHost: 127.0.0.1`. RKE2 runs a load balancer on every node, so this
  bakes in no peer -- and it avoids the circle the VIP would create, where
  Cilium needs the API to start and kube-vip needs Cilium to route to it.
  Confirmed on both a server and an agent before relying on it.
- `disable-kube-proxy` follows the preset, in the L1 configuration rather than
  here: RKE2 reads it at startup, which is before any of L2 exists. Setting it
  on servers and agents both, because one node still running kube-proxy
  programs service rules the others do not have.

### Fixed

Two defects the live run exposed, both of the same shape -- a step that
finished before its work had taken effect.

- Writing a manifest is not installing it. RKE2 applies its manifest directory
  on its own schedule, so every step here now waits for the cluster to hold the
  object rather than for the file to exist. Without it the Gateway API step
  reported `STUCK` on a cluster that was seconds from being correct, and a load
  balancer pool could have been written and never created -- which leaves
  Services Pending forever with nothing saying why.
- The join phase's service step had no stale-configuration check, so the agent
  kept running with kube-proxy enabled after its `config.yaml` had been
  rewritten, while every step reported success. It is the same check the first
  server already had, and one node running kube-proxy while the rest replaced
  it is exactly the intermittent fault that check exists to prevent.

## [0.30.0] - 2026-08-08

### Added

- kube-vip, so the control plane has an address that is not any node's.
  ADR-008 has required that since it was written, and until now nothing served
  it. The DaemonSet is written into RKE2's auto-deploying manifest directory,
  which means the cluster reapplies it on restart without this tool present.
  The ordering only looks circular: the first server is the node being joined,
  not one joining, so it comes up without the VIP and kube-vip claims the
  address afterwards.
- `ResolveVIPInterface` reads the interface from the node rather than assuming
  it. A node with two NICs has exactly one that can answer ARP for the address,
  and picking the other produces a VIP reachable from nowhere anybody cares
  about.
- The VIP step waits for a TLS listener on 6443, not for a DaemonSet to exist.
  What the join needs is an address that answers.
- `svc_enable` is off: handing addresses to Services is the dataplane's job
  (ADR-004), and two components allocating from one pool is a conflict nobody
  can debug from the symptom.

### Fixed

- The service step called a running unit satisfied while it ran with a
  configuration older than the file on disk. RKE2 reads `config.yaml` once, at
  startup, so the config step wrote a change, the service step saw a Ready node
  and the change never took effect. Reproduced live: the cluster ended up with
  an API certificate that did not cover the VIP the document asked for, while
  every step reported success. The check now compares the file's mtime against
  the unit's start time, and Apply restarts rather than starting.
- PF-607 reported `enp6s18, enp6s18` as an ambiguous choice once the VIP was
  assigned, because one interface with two addresses on the subnet was counted
  twice. A VIP already assigned is the answer, not an ambiguity.
- PF-609 counted the VIP itself as evidence of multi-homing. A floating address
  the document declares -- a VIP, a pinned gateway address, anything in the load
  balancer pool -- is nobody's identity: the node holding it today may not hold
  it tomorrow.
- PF-606 blocked on the VIP this tool had just brought up. It now compares the
  address against what the nodes report carrying, which means it runs after the
  node probes: whether a VIP is free cannot be answered from outside alone.

### Verified

A two-node cluster, built end to end and re-measured at 73 pass / 0 fail.
The agent joined through the VIP, and the API certificate carries
`IP Address:192.168.88.210` after the restart the service step forced. Both
`l1-bootstrap` and `l1-join-agent` report every step satisfied on a second run.

## [0.29.0] - 2026-08-07

### Added

- `l1-join-server` and `l1-join-agent`. The cluster token is read from the first
  server's `node-token` rather than carried in the document: `cluster.yaml` is
  an audit artifact handed to customers, and a plaintext credential in one is
  what the SourceRef indirection exists to prevent. No schema field was added --
  pinning a token has no demonstrated need yet, and adding it later as a
  SourceRef would not break this default.
- The readiness step runs on a node already in the cluster, not on the one
  joining. An agent has no kubeconfig; asking it whether its own kubelet is
  running answers a much weaker question than whether the control plane
  accepted it.
- Starting the unit and registering with the cluster are separate steps. A unit
  that will not start is a different problem from a node the control plane has
  not accepted, and reporting them as one sends people to the wrong logs.
- The joining node is matched by address, not hostname. A hostname depends on
  what the node calls itself and on whether `node-name` was set; the address is
  what the document says.

### Fixed

- PF-603 checked that the registration address resolves, not that it resolves
  to this cluster. The homelab document used for testing pointed at a public
  address: it passed every DNS check and would have failed every join, because
  a joining node dials the supervisor at whatever came back. It now compares
  against the document's nodes, VIP, gateway addresses and load balancer pool --
  the pool counts because it does not exist yet at preflight time, so a record
  pointing into it is correct and simply early.

## [0.28.1] - 2026-08-07

### Fixed

Two defects a second node and a built cluster exposed, neither of which could
have been found without both.

- PF-802, PF-803, PF-804 and PF-805 blocked every run after the first. They
  fired on the cluster this tool had just built, correctly identifying an
  installation and wrongly calling it a leftover -- which made resume (§4) and
  adding a node impossible, with the tool refusing its own work. They now ask
  who wrote it: a `config.yaml` carrying the managed marker is ours, and a
  marker is evidence on the node rather than a mode flag the caller can get
  wrong. `TestManagedMarkerMatchesWhatTheInstallPhasesWrite` ties the string
  PF-8xx looks for to the string the install phases write, because drift there
  fails silently.
- PF-609 called every built node multi-homed. Cilium gives the node a
  `cilium_host` address out of the pod CIDR, and counting a dataplane's own
  interface as a second network warns about every cluster the tool has
  installed. The interface list PF-804 already maintained is now shared, so the
  two cannot disagree about what a dataplane interface is.

### Verified

Both nodes, 107 checks in 6s. The peer probes ran for the first time against
real hardware in both directions: PF-601 found connections refused rather than
dropped on all five control-plane ports, PF-602 carried a 1500-byte frame
unfragmented, PF-502 measured the clocks agreeing within a second. PF-208's
"could not be measured" path was confirmed on the second node, where nothing
had loaded nf_conntrack.

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
