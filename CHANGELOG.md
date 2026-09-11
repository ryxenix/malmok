# Changelog

## [Unreleased]

### Added
- A three-server case that loses a server. `ha-failover` builds three servers
  under a VIP, then takes the control plane away from the one answering for
  the address -- chosen, not assumed, because losing a node nobody was routed
  to proves nothing. It passes only if the address moves, a write lands
  through it with two of three etcd members, and the lost server rejoins.
  `MALMOK_LAB_THIRD` names the third machine; without it the case skips.

### Fixed
- On a cluster with a VIP, the operator's kubeconfig named the machine it was
  on. RKE2 writes 127.0.0.1 -- that server's own API server -- and the copy
  handed to the operator kept it. The failover case measured the cost: with
  the first server's control plane taken away and brought back, the VIP had
  moved and was accepting writes, and kubectl on that server was refused while
  its API server finished starting. Every restart does the same, and an
  upgrade restarts every server in turn. The copy names the VIP now, which the
  API server's certificate already carries.
- The lab's node checks were cut off at two minutes by the connection that ran
  them, so the pod-settle loop that says it waits five minutes waited two.
  Harmless while pods settled quickly; not after a server has been taken away
  and every pod on it has to come back.

## [0.96.1] - 2026-09-10

### Fixed
- `malmok --help` cited a document the repository does not contain, and so did
  the help for `apply`, `attach` and `report`. The source comments were fixed
  before publication and the command output was not, which is the half a user
  actually reads: `--help` is the first thing anybody runs. The sentences stand
  on their own now.

## [0.96.0] - 2026-09-10

### Changed
- README and the documentation landing pages say what 1.0 means. 0.95 read as
  "nearly done" beside a status banner that says alpha and a table with
  unverified rows in it -- the number and the sentences were making different
  claims. The number is a compatibility promise: `0.x` says the schema can
  still move. 1.0 is when it becomes `v1` and the unverified rows say
  otherwise.
- The lab harness has no default addresses. `scripts/matrix.sh` has said since
  it was written that a default here is a loaded gun pointed at whatever lives
  at that address on somebody else's network -- and the harness carried
  192.168.88.x defaults anyway, so `go test -tags lab` went around the safety
  the script documented. Every case wipes and reboots both machines. The suite
  now skips and says what to name; the cases that pin a VIP or a load-balancer
  pool skip separately.

### Fixed
- CI failed on `cache: pip` the same way the documentation workflow did, and
  0.95.0 fixed only the one it was looking at. Both name
  `requirements-docs.txt` now. Finding one instance of a fault and not grepping
  for the rest is how the same fix gets made twice.

## [0.95.0] - 2026-09-09

### Fixed
- `ca-trust` installed the private CA correctly and could never confirm it. The
  0.88.0 quoting fix reached `Do` and not `Check`, which put the certificate's
  second line where the shell expected another command -- so every private-CA
  run halted on EX-003, applied and the target state not reached. It named the
  trust store; the fault was the quoting. Found on hardware two releases later.
  The newline guard passed throughout, because raw material carries real
  newlines and that is all it looked for; the new guard checks where they land.
- The documentation workflow failed before it built anything. `cache: pip`
  keys the cache off a dependency file and looks for `requirements.txt` or
  `pyproject.toml`; this repository has `requirements-docs.txt` and neither of
  those, so the step failed outright rather than skipping the cache it could
  not key.
- The carry list was missing five images, and only a closed site could tell.
  The metrics stack templates VMSingle, VMAgent, VMAlert and VMAlertmanager as
  custom resources, and the VictoriaMetrics operator fills their images in at
  run time: the chart states a tag and puts the repository in a renovate
  comment, so rendering it -- which is how the rest of the list is generated --
  found nothing. Online the node simply fetched them. Air-gapped, the install
  laid down RKE2, Cilium, the gateway and local-path from carried files and
  then sat for fifteen minutes on a metrics database that could not pull its
  own image. They are named in Go now, read from a cluster that ran the stack
  rather than guessed out of a comment, and a chart bump fails the build until
  somebody has read them again.
- PF-802 blocked the air-gap procedure this repository documents. The guide
  says to seed the image bundle into `/var/lib/rancher/rke2/agent/images/`
  before RKE2 starts; the check then found a data directory and refused to
  install, on the grounds of inherited certificates and etcd that a directory
  of image archives does not have. A data directory holding nothing else is
  not an installation now.
- PF-802 blocked a resume on this tool's own work. Ownership was read from
  `/etc/rancher/rke2/config.yaml`, which the bootstrap phase writes; node prep
  writes `registries.yaml` first and creates the directory. A run interrupted
  between them left a node malmok had written and could not recognise, and the
  resume then stopped on "an installation this tool did not write" -- the one
  case resume exists for. Any managed file under the directory counts now.
- `MALMOK_SKIP_IMAGES=1` skipped the image bundle and then failed on the
  checksums. The list named `malmok-images_*` unconditionally, an unmatched
  glob is passed through literally, and `sha256sum` exits non-zero on it -- so
  the escape hatch worked only for the case that did not need one.

### Added
- The release carries the images. `malmok-images_<tag>_linux_<arch>.tar.zst` is
  every image the platform pulls, built in CI from the list the binary itself
  prints, the way k3s and RKE2 ship theirs. A closed site downloads three
  things -- the binary, RKE2's artifacts and this -- instead of reading chart
  values and pulling twenty images by hand. It goes in
  `/var/lib/rancher/rke2/agent/images/` on each node, where containerd already
  looks. An image with no build for an architecture fails the release rather
  than producing a bundle with a hole in it, and a bundle over the 2 GiB a
  release asset can be fails with what to split.

### Changed
- The air-gapped lab case stages the platform image bundle. RKE2's artifacts
  carry RKE2; the charts pull twenty images more, and on a closed node they
  have to be there first -- the storage provisioner soonest, which is where
  the case failed as soon as storage existed at all. `MALMOK_IMAGE_ARCHES`
  builds one architecture, for a lab that does not need both.
- The lab harness supplies the VIP and the load-balancer pool. The matrix
  defaulted to the documentation ranges, which is right for a public repository
  and impossible to run: kube-vip claims the VIP on an interface and nothing is
  on 192.0.2.0/24, so every case with a VIP failed at `vip-interface` saying
  exactly that. `MALMOK_LAB_VIP` and `MALMOK_LAB_LB_POOL` name real ones.
- The upgrade case builds at the previous minor instead of at stable. Stable
  and latest name the same release for weeks at a time, and the case skipped
  every run of the matrix so far -- while the README cited that matrix as
  evidence for the row. An upgrade from the previous minor is always available
  and is the upgrade an operator actually performs.
- A killed run that ends on its own says so. When `ca-trust` halted every
  private-CA run in l0, the resume case spent its full fifteen minutes watching
  a process that had already failed and then reported that a later step never
  started -- naming the wrong thing twice over.

## [0.94.0] - 2026-09-08

### Added
- An `l2-storage` phase, because `storage.driver` was a field nothing read. The
  document named a driver, `malmok plan` printed it, the audit report told the
  customer "Storage | local-path", and no phase installed anything -- so the
  cluster came up with no StorageClass at all and nothing said so. It surfaced
  two layers away, as the metrics database sitting Pending on a claim that
  could never bind. Five of the ten verification cases died there, including
  the air-gapped one, which had otherwise got as far as an Accepted
  GatewayClass with its egress dropped.
  - `local-path` installs Rancher's local-path-provisioner, rendered from the
    binary rather than fetched: a closed site carries two images and nothing
    else. Both are pinned, including the helper image upstream leaves untagged
    -- an untagged image means `:latest`, which no bundle can carry, and the
    helper pod runs on every volume create and delete.
  - `byo-csi` installs nothing and says whether the site's own CSI produced a
    StorageClass, so a missing one is named here rather than landing on
    whichever workload asks for a volume first.
  - `longhorn` and `nfs` are **not installed by this release** and now stop the
    run and say what to do instead. They are in the schema, in four profiles
    and in the wizard, and carrying on would leave a cluster with no
    StorageClass while the report printed the driver that was asked for.
- The carry list covers images that do not come from a chart. `malmok images`
  and the release's `images.txt` are generated from the binary now, so the
  storage provisioner appears in both.

### Fixed
- `metrics-ready` waited the full fifteen minutes for a claim that could never
  bind, then retried and waited again, and reported a deadline against the
  metrics database. A cluster with no StorageClass is decidable in one second,
  and the step now says so and names the field that decides it.

## [0.93.0] - 2026-09-07

### Fixed
- `registry.mirrors` never reached a node. The field is in the schema, is
  documented, and the network preflight dials the endpoints and reports them
  reachable -- and then the renderer returned before reading it. Three ways to
  hit that: `mode: embedded` returned early with the wildcard entry, an empty
  mode returned nothing, and any mode without `systemDefaultRegistry` also
  returned nothing, so a field whose purpose is to name somebody else's cache
  required naming a private registry first. A check that reports on something
  the machine is never told is worse than no check.
- An https mirror endpoint was described nowhere in `configs:`, so a cache
  behind a private CA got no `ca_file` and every pull through it failed with an
  opaque x509 error. `TrustSpec`'s own comment calls that the single most
  common private-CA misinstall.
- Two mirrors rendered a different file each run, because the map was walked in
  its own order. The step writes this file and then compares what it finds
  against what it meant to write, which made it report drift it had caused.

- No step reached over SSH has ever streamed its progress. `Elevate` wraps every
  remote runner in `Sudo`, `Sudo` embeds the `Runner` interface, and embedding
  an interface promotes that interface's methods and no others -- so
  `ShellStep`'s `Runner.(exec.Streamer)` failed for every step on every node
  and each one silently fell back to running mute. The waits print where they
  have got to precisely so an operator can tell waiting from hung; none of it
  was carried out, and a wait that failed after fifteen minutes left no record
  in the bundle of what it had seen. Only a local install running as root ever
  streamed, because that path is not wrapped. The unit tests passed throughout:
  they hand `ShellStep` a runner that nothing has wrapped.

### Added
- `test/lab/cache/compose.yaml`, a pull-through image cache for the lab, and
  `MALMOK_LAB_MIRROR` to point the matrix at one. Every case wipes both nodes,
  which takes containerd's image store with it, so the platform's images were
  fetched from the internet again for every case -- 438 pull and import lines
  on one node in one run, and a `metrics-ready` step that timed out waiting for
  them in two of the first four cases. Off by default: a run that passes
  through a cache has not shown that a customer without one installs.

## [0.92.0] - 2026-09-07

### Fixed
- `registry.chartDir` did not work on a node at all. It shipped in 0.90.0
  verified by rendering, which proved the manifest was right and not that it
  could be delivered. Adding an air-gapped row to the verification matrix found
  three reasons in a row, each hidden behind the one before it.
  - A manifest travelled inside the command that wrote it. Measured against a
    node: a command of 128KB arrives and dies on the argument-length limit, and
    at 256KB the connection is dropped before anything runs. A chart embedded
    as `chartContent` is 200KB for cert-manager and 435KB for the metrics
    stack, and the failure read as an EOF fifteen milliseconds in -- the node
    appearing to go away. Manifests now travel on standard input, which carries
    a megabyte without complaint.
  - `Sudo` fed the password through a pipe, which consumed the whole of stdin
    before the command saw any of it. `sudo -S` takes the password as the first
    line of its own input and leaves the rest to the command, so the two now
    travel together.
  - `kubectl apply` keeps a copy of the whole object in the
    last-applied-configuration annotation, and annotations are capped at 256KB.
    The cluster answered "metadata.annotations: Too long" about a manifest with
    no annotations of its own. Applied server-side now, which keeps no such
    copy.
- `FetchChannels` retries. The channel server answered 404 twice in half an
  hour of lab runs while the same URL answered from a shell seconds later, and
  a single attempt makes that an operator opening the wizard to a version field
  that could not be filled for a reason that has nothing to do with them.

### Added
- `airgap-pair` in the verification matrix, and a `network` dimension for it to
  occupy. Every row until now was online; the air-gapped path was verified by
  hand, which found nine defects in a day and then had nothing to keep them
  fixed. The harness cuts the nodes off with DROP rather than REJECT -- a
  rejected connection fails at once and a dropped one fails at the connect
  timeout, and the second is what a site firewall does -- and restores them
  even when the case fails.
- `exec.Feeder`, for a runner that can send a command its standard input, and
  `ShellStep.Input` for a step whose subject is too large to put in a command.

### Changed
- The air-gap payload is pinned and verified. `release.sh` fetched helm and k9s
  at whatever "latest" meant that morning and checked nothing, so the same tag
  built twice produced different binaries and a download nobody verified went
  inside a tool that runs as root on a customer's nodes. Versions are now
  named and checked against upstream's published sums. `SHA256SUMS` over the
  finished artifacts never covered this: it records what was built, not what
  went into it.

## [0.91.0] - 2026-09-07

### Added
- `malmok images` prints the container images an air-gapped site has to carry.
  Until now an operator had to work them out by reading charts at the
  customer's site, which is where the answer is hardest to get and most
  expensive to get wrong.
  - The chart versions are pinned in this release, so the answer is fixed and
    is worked out once -- by `scripts/images.sh`, on a machine with a network
    -- rather than by everyone who installs. The result is committed and
    embedded, so the command answers on a node with no network and no helm,
    which is the only place the question is really asked.
  - With `-f cluster.yaml`, only the charts that document installs.
  - `TestTheListMatchesThePinnedVersions` fails when a chart version is bumped
    in Go without regenerating the list. Shipping last release's carry list is
    a mistake discovered at the customer's site, and it needs no network to
    catch here.
  - The extractor walks the rendered manifests and the chart values, because
    neither alone is enough: a chart states some images only in its values,
    where an operator reads them at run time. A grep was tried first and was
    quietly wrong -- it saw 7 of the metrics stack's images and missed its data
    plane, because a nested key has no value on its own line.

### Known limits
- The list is not complete, and says so. `victoria-metrics-k8s-stack` templates
  VMSingle, VMAgent, VMAlert and VMAlertmanager resources whose images the
  VictoriaMetrics operator fills in at run time; the chart carries only their
  tags, with the repository in a renovate comment. Reading a comment would be a
  guess, and a guess in a carry list is discovered in a room with no way to
  fetch what is missing. Three more images have no tag in their chart at all --
  it comes from the chart's appVersion -- and are listed separately rather than
  dropped. `malmok images --help` gives the kubectl one-liner that reads the
  authoritative set off a cluster that has run the stack.
- It is a superset in the other direction: rendered from default values, so a
  component the document disables still appears. Carrying an image nobody pulls
  costs bytes.

## [0.90.0] - 2026-09-07

### Added
- `registry.chartDir`: a closed site can carry the Helm charts across instead
  of standing up a registry to mirror them into. Until now the air-gapped path
  required `registry.chartRepo`, which means a customer without a Harbor was
  told to build one before they could have a cluster -- and the tool's value
  halves at that sentence.
  - The archives are read from the directory the document names, relative to
    the document, and embedded in the HelmChart as `chartContent`. helm-
    controller takes a base64 `.tgz` there and it overrides `chart` and
    `version`, so neither is written beside it: a version line next to bytes
    that decide the version is a document disagreeing with itself.
  - Bytes win over an address. A document that set both gets the archive for
    the charts it carried and the mirror for the rest.
  - The three charts this tool installs are self-contained and small enough for
    it: cert-manager 150 KB, argo-cd 226 KB with redis-ha vendored,
    victoria-metrics-k8s-stack 327 KB with all five of its subcharts vendored.
    Base64 costs a third on top, against a Kubernetes object limit near 1.5
    MiB. A chart packaged without its dependencies would send helm to the
    network at install time, which is the one thing this exists to avoid.
  - PF-710 reads the directory before anything is installed. A missing archive
    is not a failure at install time, it is a silent change of plan: the chart
    falls back to its upstream repository, and on a closed node that is a
    HelmChart job retrying against the internet until it gives up. The versions
    are pinned in code, so the check names the exact files to stage -- and
    names a wrong version separately, because a directory that looks right is
    the mistake worth calling out.
  - `catalogue.Charts` is where "which charts does this document install"
    lives, so the loader, the check and the wizard cannot drift from each
    other.
  - The wizard offers the directory beside the mirror, air-gapped builds only.

## [0.89.0] - 2026-09-07

### Removed
- `registry.bundle`, and PF-707 with it. The field was in the schema, passed
  validation, and had a preflight check that verified the artifact's checksum
  -- and nothing anywhere loaded it. An operator could name a Hauler bundle,
  watch the tool confirm it byte for byte, and reach a node that had never seen
  it. That is the worst shape a defect can take on an air-gapped site: it is
  not wrong until the cluster is half up in a room with no network.
  - A document that sets it now fails to load, naming the field and the line.
  - Nothing is left uncovered. PF-701 to PF-703 measure a registry that is
    named, PF-709 reads the artifact path on the node, and validation refuses
    an air-gapped document that names no image source at all.
  - PF-707 is retired rather than reused. Audit reports and customer tickets
    outlive releases, so the number stays spent.
  - The wizard's bundle field is gone; the air-gapped registry screen still
    reaches both answers that exist, and its test now says so.
  - `registry.mode: internal` no longer describes itself as seeded from a
    bundle. It is a registry inside the network, seeded by whoever runs it.

## [0.88.0] - 2026-09-04

### Fixed
- Every multi-line file node preparation writes arrived as one line of literal
  `\n`. The content was quoted with Go's `%q` and handed to a shell `printf
  '%s'`: the shell strips the quotes and leaves backslash-n as two characters,
  and printf writes them. Two files were affected and neither failure was
  visible, because the check that compares the file against the document was
  quoted the same wrong way and therefore agreed with it.
  - `registries.yaml`, where RKE2 answered `no registries configured for
    distributed mirroring` and started no mirror at all.
  - The private CA in the node trust store, which is worse: a PEM on one line
    is not a certificate, so a private CA has never actually been installed on
    a node by this tool. Anything on the node that had to trust it -- a
    registry, an internal endpoint -- was relying on a file that could not
    parse.
  - `TestWrittenFilesKeepTheirNewlines` fails on the shape rather than on
    either instance of it.

### Verified
- The air-gapped install was re-run against DROP rather than REJECT, which is
  what a site firewall does: a rejected connection fails at once, a dropped one
  fails at the connect timeout, and code that looks fine against the first can
  sit for minutes against the second. It does not: two nodes, five phases,
  9m50s, nothing failed.
- Every outbound attempt during the run was logged and accounted for rather
  than counted. `_apt` and `fwupd-refresh` are the operating system. The rest
  was containerd reaching for `registry-1.docker.io`, and with the embedded
  mirror now actually enabled the agent's share of that went from 96 packets to
  none -- it takes the images from its peer instead.
- What remains is RKE2's own cold start: the first server has no peer to mirror
  from and needs the runtime image before its image archives have finished
  importing, so it tries Docker Hub for about thirty seconds. It costs nothing
  -- the install completes -- but a site watching egress will see it, and it is
  better to be able to say why than to be asked.

## [0.87.0] - 2026-09-04

### Fixed
- `registry.mode: embedded` now enables RKE2's embedded registry mirror. It is
  the default for the homelab and company-prod profiles and its schema comment
  has always said it "shares images peer-to-peer between nodes that already
  hold them" -- and nothing wrote `embedded-registry: true`, so every cluster
  built so far ran the documented default with the feature off.
  - `embedded-registry: true` goes in the server config, which enables the
    mirror cluster-wide. `ServerConfig` serves both the first server and the
    ones that join, so HA is covered.
  - Node preparation writes a `registries.yaml` naming `"*"` under `mirrors:`
    with no endpoint. That entry is what makes a registry take part; without
    one the mirror is enabled and mirrors nothing, which is what the previous
    test asserted was correct.
  - PF-601 and PF-803 measure 5001, and the peer listeners bind it. A closed
    5001 does not fail: containerd falls back to the upstream registry, which
    on an air-gapped node is a pull that hangs rather than an error.
  - This matters for air-gapped sites specifically. RKE2 pins images loaded
    from a tarball and shares them under whatever registry they are tagged for,
    even one that does not exist -- so a bundle seeded onto one node reaches
    the rest without being copied to each.
  - The trade is stated rather than hidden: the mirror assumes every node in
    the cluster is equally trusted, because a peer can fetch any image another
    peer holds without the credentials it was originally pulled with. That
    holds for the profiles this is the default for; the modes that name a
    private registry do not use it.

## [0.86.0] - 2026-09-04

### Changed
- The schema group is `malmok.dev`. `platform.ryxen.dev` was a reverse-DNS
  namespace carved out of a domain that was never registered -- a namespace
  anyone could take, in the `apiVersion` of every document and in annotation
  keys on the objects of running clusters. `malmok.dev` is registered to this
  project.
  - `apiVersion: malmok.dev/v1alpha1`, and the handoff document is
    `malmok.dev/handoff/v1alpha1`.
  - Annotation keys move with it: `malmok.dev/fingerprint`, `/san`,
    `/chain-depth`, `/not-after`, `/source`, `/zone`, `/exposure`,
    `/matrix-case`, `/proxmox-vmid`, `/simulated`.
  - The `platform.` prefix is gone. It existed to carve a namespace out of a
    personal domain; the domain is the project's now, so the prefix said
    nothing. This matches how projects that own their name do it --
    `cert-manager.io`, `argoproj.io`.
  - A document on the retired group is refused with the migration rather than
    with two strings to diff: the group moved and nothing else changed, so the
    error prints the `sed` that does the whole thing.
  - An existing cluster carries the old annotation keys. The next apply writes
    the new ones; the steps are idempotent, so this costs one re-application of
    a few annotated objects and nothing else.
  - The CHANGELOG is not rewritten. Entries below record what the module path
    and the group were at the time, and editing them to match today would make
    the record wrong rather than current.

## [0.85.0] - 2026-09-04

### Fixed
- `os.disableSwap` is read. It was declared and ignored, so a document that
  said `false` -- a site stating that it keeps its swap -- had swap turned off
  and its `/etc/fstab` rewritten anyway, silently. A field that looks like an
  opt-out and is not is worse than no field: the operator has no way to learn
  that the thing they declined happened.
  - Unset still means off, because that is the only default that produces a
    cluster the kubelet will join.
  - `false` skips node preparation's swap step. It does not turn swap back on:
    the tool stops changing the setting, it does not undo an earlier run.
  - `false` now requires `fail-swap-on=false` in `kubernetes.kubeletArgs`.
    Keeping swap without telling the kubelet produces a cluster that does not
    come up, and the kubelet's own failure names a flag rather than the swap
    device. The flag is required rather than injected: `cluster.yaml` is an
    audit artifact, and an argument nobody asked for is one nobody can account
    for later.
  - PF-105 no longer reports active swap as a finding when the document keeps
    it. Telling an operator to disable swap they deliberately kept is advice to
    undo their own decision.
  - What "unset" means now lives in `v1alpha1.OSSpec.SwapDisabled`, because
    three packages read the field and a pointer that means different things in
    each of them is how this happened.

## [0.84.0] - 2026-09-04

### Removed
- `os.varLibRancherDevice` and `os.manageFirewall`. Both were declared in the
  schema and read by nothing. A field that does nothing is worse than an
  absent one: the document makes a promise on the tool's behalf and the
  operator has no way to tell the difference. `manageFirewall: false` in
  particular looked like an opt-out from something the tool never did.
  Documents that set either now fail to load, naming the field and the line --
  which is the point. This is what `v1alpha1` means.

### Fixed
- PF-304's registry entry said "apply must add the cluster port rules". No
  step has ever added a firewall rule, and the registry is what a customer
  ticket quotes. It now says what is true: the ports have to be opened in the
  firewall that is running, and PF-601 measures whether they are. The probe's
  own detail was already correct; only the registry promised the work.

## [0.83.0] - 2026-09-03

### Changed
- The module path is `github.com/ryxenix/malmok`. In Go the module path is the
  import path, so it has to be the repository's real location or
  `go install github.com/ryxenix/malmok@latest` cannot resolve. Settled before
  the first tag rather than after: moving it later breaks the `go.sum` of
  anyone who has already fetched it.
- The Kubernetes API group is untouched. `platform.ryxen.dev/v1alpha1` and the
  `*.ryxen.dev` annotation keys are not repository references -- they are in
  every `cluster.yaml` and on the objects of clusters already running, and
  renaming them would be a breaking change to documents rather than a rename.

## [0.82.0] - 2026-09-03

### Changed
- Both READMEs open with a recording of the wizard rather than a still of its
  last screen: where, nodes, preflight, install, result, in 32 seconds. The
  still showed where a run ends and not what using the tool is like -- the
  address being validated as it is typed, the phases turning over, the log
  filling in.
- `scripts/record.sh` takes it, in either language. `scripts/screenshot.sh`
  and the stills are gone; nothing referenced them once the recordings landed,
  and a still is one `magick out.gif[0]` away from the recording anyway.
- The caption says the recording is a `--demo` run. The install finishing in
  seconds is a property of contacting no node, not a claim about a real one.

## [0.81.0] - 2026-09-03

### Changed
- The README shows a screenshot of the wizard instead of a paste of one. The
  block it replaces was a terminal capture pretending to be an image: it went
  stale silently, and it read as a drawing of a program rather than the
  program.
- `scripts/screenshot.sh` regenerates both, so the next UI change can. It goes
  through agg, the asciinema renderer, rather than the obvious tool: freeze
  places glyphs by counting characters, so every Hangul syllable on the Korean
  screens claims one cell where the terminal gives it two, and the layout
  shears. agg draws into a real terminal grid.

## [0.80.0] - 2026-09-03

### Fixed
- The gateway never got its nodes. `gateway-nodes` read node addresses with a
  jsonpath whose `{"..."}` held a real newline instead of `\n`; kubectl answers
  that with "unterminated quoted string", and the read was behind
  `2>/dev/null`. So the loop ran over an empty list, labelled nothing, exited
  0, and the step retried three times reporting EX-003 -- applied without error
  but the target state was not reached -- without once printing the reason.
  Fixed on a `node-ips` gateway that could not come up in the air-gapped lab.
  - The jsonpath now uses the escape, and its stderr is no longer discarded.
  - `Do` reads into a variable before looping, so a failing kubectl stops the
    step under `set -e` rather than leaving a loop with nothing to iterate.
  - `TestNoRawNewlineInsideJSONPath` scans the whole tree for the same shape;
    the idiom is copied between phases and the next copy is the one that
    matters.

## [0.79.0] - 2026-09-03

### Fixed
Three defects between a valid air-gapped document and a running cluster, each
found by installing one on the lab nodes with egress blocked.

- The install demanded an executable bit on `install.sh` and then ran it with
  `sh`. The way anyone obtains that file -- `curl -o install.sh
  https://get.rke2.io` -- leaves it mode 644, so every air-gapped install
  stopped on a file that was present, correct, and about to be run by an
  interpreter that does not care about its mode. It is now checked for being
  readable, which is what running it requires.
- PF-709 accepted any file beginning `rke2-images` as the image archive. Two
  different mistakes passed it, and both are silent for minutes:
  - Only the per-CNI archives (`rke2-images-core`, `rke2-images-cilium`). The
    installer stages `rke2-images.linux-<arch>.tar.zst` first and copies the
    others only afterwards, so nothing is loaded at all and `rke2-server` dies
    looking for its runtime image.
  - Only the combined archive, with a `cilium-*` preset. It carries Calico and
    Flannel, not Cilium, so the node registers and then every Cilium pod sits
    in `ImagePullBackOff` against a registry the node cannot reach.
  PF-709 now knows which dataplane the document asked for and names the archive
  that is missing.
- PF-709 did not look for the Gateway API bundle either, so a `cilium-gw`
  document reached `l2-dataplane` and stopped there, with the cluster up and
  the CRDs it needs unreachable. It is the fourth thing that has to cross an
  air gap, and the check now asks for it by the name the step reads --
  `dataplane.GatewayAPIBundle`, so the two cannot drift apart.

## [0.78.0] - 2026-09-02

### Fixed
- An air-gapped install could not pass its own checks. Three separate places
  held the premise that a Hauler bundle is the only source of images an
  air-gapped cluster can have, and `registry.bundle` is the one image source
  this tool does not load anything from. The result was that the only
  configuration preflight accepted was the one that does not work, and the two
  that do -- RKE2's own `rke2-images-*.tar.zst` in `kubernetes.artifactPath`,
  and a mirrored `registry.systemDefaultRegistry` -- were refused before
  reaching a node. Found by staging real artifacts on the lab nodes and running
  `plan` against them.
  - `spec.Validate` now accepts `kubernetes.artifactPath` as an image source.
    Naming a registry to satisfy the old rule was worse than useless:
    `system-default-registry` rewrites every system image reference, and the
    preloaded images carry their original names.
  - PF-707 no longer fails on an empty `registry.bundle` when another source is
    named; it says which one it deferred to. PF-709 reads the artifact path on
    the node and reports what is actually in it.
- SSH now offers the host key types `known_hosts` already holds, the way
  OpenSSH does. Unset, the server chose its own -- ecdsa on a stock Ubuntu --
  and a `known_hosts` holding that host's ed25519 key answered `key mismatch`.
  That sentence means "the machine changed", so it sends an operator to look
  for an attacker instead of at the key type. A genuine mismatch now says which
  types the file holds and that a rebuilt machine is the other explanation.

## [0.77.0] - 2026-09-02

### Added
- `kubernetes.artifactPath` says where RKE2's own release artifacts sit on
  each node.

  `rke2.Options.ArtifactPath` and `dataplane.Options.ArtifactPath` have existed
  since the tarball decision (ADR-013) and nothing ever set them, so an
  air-gapped install still went to GitHub for the binary it was carrying in a
  bag. The document now fills both, in `catalogue.Build` rather than at each
  caller: apply, upgrade and the headless builder each construct their own
  options, and a value threaded through three of them is a value missing from
  the fourth.

- PF-709 measures that directory on the node. The installer's answer to a
  path that is absent, empty or half-staged is a failed download -- the least
  useful sentence available on a machine with no route -- so the files are
  listed and named here instead: the tarball, its checksum, install.sh, and
  the images archive whose absence means the node will try to pull.

- Validation refuses an air-gapped document that names no artifact path, for
  the reason it now refuses one with no chart mirror. Three things cross an
  air gap -- images, charts, and RKE2 itself -- and until this release the
  document could only name the first.

- The node screen asks for the path when the network is air-gapped, since the
  wizard's own test refuses a baseline that cannot be composed on a screen.

## [0.76.0] - 2026-09-01

### Added
- `registry.chartRepo` points the platform's Helm charts at a mirror.

  The three phases that install a chart -- cert-manager, the metrics stack and
  ArgoCD -- each carried a `ChartRepo` override in their options, and nothing
  in the schema or the command line ever set one. They always fetched from
  charts.jetstack.io, victoriametrics.github.io and argoproj.github.io. An
  air-gapped site could mirror every image through
  `registry.systemDefaultRegistry` and still fail at `l2-pki`, because images
  and charts are two mirrors and only one of them could be named.

  Both kinds of mirror are understood, since the two are not the same shape to
  the helm controller:

      https://charts.acme.internal        repo: plus a bare chart name
      oci://harbor.acme.internal/charts   chart: carrying the whole reference

  ADR-007 already puts an air-gapped site's charts in the OCI registry beside
  its images, so supporting only the Helm-repository form would have meant
  supporting the form that site does not have.

- Validation refuses an air-gapped document that installs a chart and names no
  mirror. Without it the run reaches `l2-pki` and stops there, twenty minutes
  past the point where a file could have been read. A document that installs
  no chart is not asked -- RKE2 carries Cilium's chart in its own artifacts,
  so such a cluster still comes up.

- The registry screen asks for the mirror, because the wizard's own test
  refuses a baseline that cannot be composed on a screen -- and every airgap
  profile had just become one.

## [0.75.2] - 2026-08-28

### Changed
- Malmok is the name, not a working title. The tentative marker is gone from
  the engineering rules; publishing a repository under a name settles it either way, and
  a "(tentative)" beside a name nobody intends to change reads as indecision.
- The coverage tables said the observability stack was schema-only. It was
  implemented two releases ago and has since run on the production cluster,
  scraping 19 targets and storing samples, so both READMEs and
  `docs/00-architecture.md` now say so. The architecture document also credits
  ACME HTTP-01, which the same cluster proved, and moves DNS-01 to
  "implemented, never run end to end" -- which is where it actually is.
- The READMEs describe what an install produces, so they now mention the
  metrics stack alongside the dataplane and the gateway.

## [0.75.1] - 2026-08-27

### Fixed
- The metrics stack could not install. Helm names its objects after the
  release and the chart together -- "victoria-metrics" plus
  "victoria-metrics-k8s-stack" -- and the Service the chart creates for the
  controller-manager scrape target came to 66 characters against Kubernetes'
  limit of 63. The install failed, RKE2's helm controller reinstalls on
  failure, and the cluster spent its time creating and deleting the same pods:

      Service "victoria-metrics-victoria-metrics-k8s-stack-kube-controller-manager"
      is invalid: metadata.name: must be no more than 63 characters

  `fullnameOverride` now pins every generated name to a two-character prefix,
  and a test does the arithmetic for the longest suffixes the chart is known
  to append -- including a StatefulSet's revision hash, which lands in a pod
  label where the limit is 63 bytes rather than characters. It also requires
  headroom, because a prefix that exactly fits today is one chart release away
  from this returning.

  Found by installing it on a live cluster; no test would have caught it,
  because the length only exists once Helm has both names.
- The readiness step waited on a label selector this package guessed at. It
  now reads the Deployment the operator creates, whose name follows from the
  prefix the phase pins -- a fact about the document rather than a guess about
  the operator. A timeout also prints the install job's log, which is where a
  chart that never rendered says why.

## [0.75.0] - 2026-08-27

### Added
- `l2-observability`: the platform's metrics stack, which until now existed in
  the schema and nowhere else. A cluster built by this tool reported only what
  `kubectl` shows, and the first capacity problem on it was found by a user.

  Observability is platform work, not application work -- the same boundary
  ADR-006 draws for routes, read from the other side. Every workload needs the
  same answer to "is this node out of memory"; what an application owns is
  which of its own series to expose.

  **VictoriaMetrics**, not Prometheus: `victoria-metrics-k8s-stack` 0.91.2
  (VM v1.150.0) brings the operator, a single-node database, the scraper, rule
  evaluation, alerting, kube-state-metrics and node-exporter. PromQL and the
  same scrape configuration for an order of magnitude less memory, and
  Apache-2.0 throughout.

  **Grafana is off.** The chart ships it on and Grafana OSS is AGPL-3.0 -- a
  licence arriving in a customer's cluster because an upstream default said so
  is not a decision anybody made. VictoriaMetrics answers ad-hoc queries
  through its own UI. `platform.observability.grafana: true` adds it.

  **Seven days on ten gigabytes**, against the chart's month on twenty. Metrics
  land on whatever the default StorageClass gives them, which on a single node
  is the filesystem holding etcd and the image store (PF-401). A full disk
  there is not a lost dashboard, it is a stopped cluster. `retention` and
  `storageSize` raise it.

  On by default for the profiles with a network, off for the airgap ones,
  where every image has to be seeded into the registry before a chart can pull
  it. The options screen carries the choice and the summary states what will
  be installed.

  The phase waits for the database to serve rather than for the release to
  exist: a HelmChart that reports installed while vmsingle crash-loops on a
  volume it cannot bind is the state an operator finds weeks later, the first
  time they go looking for a graph. A timeout prints the pods and the PVCs,
  which is where the answer usually is.

## [0.74.0] - 2026-08-27

### Added
- `gateway.http2` offers HTTP/2 on the TLS listeners, with a two-row choice on
  the gateway screen. Cilium calls the setting ALPN and ships it off, which is
  why a certificate issued through this tool served HTTP/1.1: the listener
  advertised no protocols, so a browser asking for h2 got no answer and fell
  back.

  It stays off by default here too, and not out of deference. Enabling ALPN
  also enables Backend Protocol selection (GEP-1911), so a Service that
  already declares an `appProtocol` changes how the gateway speaks to it --
  a decision about somebody's running workload, not a performance knob to
  flip on their behalf. Downstream it is pure negotiation: a client that
  speaks only HTTP/1.1 is served HTTP/1.1, and nothing that works stops
  working. gRPC needs it; a GRPCRoute on a TLS listener cannot work without
  h2.

  The `cilium-applied` check reads `enable-gateway-api-alpn` alongside the
  keys it already compared. Writing a chart value the check does not read
  would let a cluster without HTTP/2 satisfy a document that asks for it --
  the same shape as a probe that measures something other than what it
  reports.

## [0.73.2] - 2026-08-26

### Changed
- The README's coverage table said ACME certificates were unverified. HTTP-01
  now has evidence: a Let's Encrypt certificate issued on a production cluster
  through the gateway this tool built, with TLS 1.3, the chain and the
  hostname checked from another machine. DNS-01 stays unverified -- the
  route53 credential path was written the same day and has never been run end
  to end, and a table that treats "implemented" as "verified" is the thing
  this table exists to prevent.

## [0.73.1] - 2026-08-26

### Fixed
- PF-612 warned that a `node-ips` gateway had no pinned address. Nothing
  allocates one: the gateway answers on the addresses the nodes already hold,
  so the DNS record can be written the day the machines are racked, which is
  exactly the condition the warning asks about. Same false premise as PF-708
  in the previous release, in the plan rather than in preflight -- and a
  warning that fires on the configuration where it is least likely to be right
  teaches operators to ignore it.

## [0.73.0] - 2026-08-26

### Changed
- HTTP-01 is the default way to get a public certificate. The wizard offers
  `acme-http01` above `acme-dns01`, and the company-prod profile picks it.

  It asks the operator for nothing they do not already have: a gateway that
  answers on port 80 and a DNS record pointing at it, both of which exist
  before anyone thinks about certificates. DNS-01 needs an API credential for
  the zone, which is a separate request to a separate team at most sites --
  worth it for a wildcard, and a poor first thing to require.

  Per-listener issuance stays the default. A wildcard is what
  `hostname: "*.example.com"` on an HTTPS listener now means in practice, and
  it needs DNS-01: an ACME wildcard cannot be proven over HTTP.

## [0.72.1] - 2026-08-26

### Fixed
- PF-708 blocked `acme-http01` on a gateway with `exposure: node-ips`,
  demanding a pinned address. Such a gateway is never given one -- Envoy binds
  the port in the node's own network namespace, so its addresses are the ones
  the nodes already hold, which is precisely the condition the check exists to
  establish. The only value that would have satisfied it, the node's public
  address, would have landed in the Gateway's `spec.addresses`, which is not
  how a host-networked gateway works. Found on a live single-node install
  whose challenge would have been delivered correctly.

  A gateway that takes an allocated address still has to name one: a DNS
  record cannot be made for an address LB-IPAM has not handed out yet.

## [0.72.0] - 2026-08-26

### Fixed
- `pki.mode: acme-http01` could never issue a certificate. The solver names a
  Gateway, and cert-manager reads Gateway API objects only when its
  configuration says to -- the chart passes `--config` to the controller
  solely when a `config` block exists, and there was none. The Certificate sat
  pending, the Order never produced a challenge, and no object in the cluster
  said why. The chart now sets `config.gatewayAPI.enabled` for that mode, and
  only that mode: the extra watches cost something and a private CA has no use
  for them.
- `pki.mode: acme-dns01` with `dnsProvider: route53` rendered a solver with no
  credential at all. Inside AWS that is correct -- an instance profile or an
  IRSA role supplies one -- but this tool mostly installs on bare metal, where
  there is no ambient credential and the ClusterIssuer reports Ready while
  every challenge fails on AWS authentication.

  `pki.acme.accessKeyID` (plain; a key ID is not a secret) now pairs with
  `pki.acme.apiToken` (a SourceRef, like every other credential) and the
  solver carries both. `region` and `hostedZoneID` are settable; the region
  defaults to us-east-1. Naming neither field keeps the old behaviour for AWS.

  Validation rejects one without the other, because that combination produces
  an issuer that is Ready and cannot solve.

### Changed
- The credential Secret's key is now the provider's own word --
  `secret-access-key` for route53, `api-token` for Cloudflare -- and a test
  pins the solver and the Secret to the same key, since they are written in
  different functions and a mismatch fails on a value that is present.
- A comment promised `malmok cert apply`, which is not a command.

## [0.71.0] - 2026-08-26

### Added
- `internal/tools` carries helm and k9s inside the binary for airgapped sites,
  and `scripts/release.sh` produces `malmok-airgap_*` alongside the ordinary
  builds: 121MB with the payload, against 10MB without it. The payload is
  downloaded at release time and is not in the repository -- it is 108MB of
  somebody else's binaries, which belongs in a release artifact and not in git
  history.

  Both Linux architectures are carried, not the build's own: the operator's
  machine and the customer's node are regularly not the same architecture.

  `malmok --version` reports what a binary holds. The two builds are otherwise
  identical and told apart by filename, which lasts exactly as long as nobody
  renames the file -- and being handed the wrong one at a customer site means
  the tools are simply absent.

  A first attempt produced an airgap binary byte-for-byte the size of the
  ordinary one. Nothing imported the package, so the linker dropped it and the
  embed never happened. An embedded payload with no reference in the program
  is not in the program.

## [0.70.0] - 2026-08-26

### Added
- The operator-tools step installs `helm` alongside kubectl and k9s. RKE2
  bundles the helm *controller*, which reconciles HelmChart resources, and
  nothing else -- so an operator who wanted to see what was installed, or add
  a chart by hand, had no CLI to do it with. Like k9s it is fetched from the
  vendor's release page and therefore skipped off-line, where the check does
  not ask for it either: a step that reports a missing tool an airgapped site
  cannot fetch never settles.

  The version comes from helm's own pointer file rather than a number written
  into the step, which would quietly age into installing something years old.
  That lookup is an assignment with a fallback, because a command substitution
  that fails under `set -e` ends the script -- the defect that once left a
  CRD wait loop in this package running zero times. A test pins both.

  Note that helm's current release is now the 4.x line. Malmok itself never
  invokes the CLI; charts are installed by RKE2's helm controller, so what is
  on the PATH is for the human.

## [0.69.0] - 2026-08-26

### Changed
- Removed things that belong to the author rather than to the tool, ahead of
  making the repository public:
  - The author's own domain was the default gateway domain in the
    verification matrix and the lab harness. A real domain as a default in a
    public repository is a default aimed at a real host -- and the ACME case
    would have asked a certificate authority for it. Now `lab.example.com`,
    with the address constants moved to the documentation ranges
    (RFC 5737, 2606).
  - A production public IP sat in a preflight fixture, from the day PF-806 was
    written against the machine that showed the fault. Replaced with the
    documentation range.
  - `docs/00-architecture.md` named three unrelated services of the author's
    in its layer table. They are not what L3 means; the row now describes the
    layer.
- `docs/00-architecture.md` §6 was a week-by-week plan that stopped matching
  reality some time ago -- it still listed Ansible for L0/L1, which ADR-012
  later forbade, and the layer table said the same. A stale plan in a public
  repository misleads more effectively than no plan, so the section is now
  what is verified on hardware, what is unverified, and what is unimplemented.

### Added
- README opens with what the tool looks like: a verbatim capture of the
  finished screen, English and Korean, plus CI, release, Go and licence
  badges. It described a terminal program without showing one.

## [0.68.0] - 2026-08-26

### Fixed
- `MALMOK_ASCII` was still read under the tool's former name, so the
  documented way to force the fallback character set did nothing: an operator
  on a serial console or an IPMI viewer set the variable, saw box characters
  anyway, and had no reason to suspect the variable rather than the terminal.
  A test now walks the whole tree for the old name -- a rename that misses one
  place has usually missed others, and the changelog is the only file allowed
  to remember what the tool used to be called.
- The run list assembled its summary from English literals -- "2 phases,
  1 failed" -- while every other line on the screen came from a catalogue.
  Switching to Korean translated the whole wizard except the one screen an
  operator opens after something has already gone wrong. The three words are
  now keys, and the run's own outcome renders through the status catalogue
  that already existed.
- README claimed the wizard opens by running `malmok` with no arguments. It
  prints help; the wizard is `malmok apply --tui`. Both language editions were
  wrong in the same way, which is what happens when one is translated from the
  other rather than checked against the program.

### Added
- `TestTheRunSummarySpeaksTheChosenLanguage` renders a real run against both
  catalogues. A test that merely asserted the keys exist would have passed
  throughout the period the screen ignored them.

### Changed
- Test environment variables in `internal/spec` carry the current name.

## [0.67.0] - 2026-08-26

### Added
- `scripts/release.sh` and `.github/workflows/release.yml`. Pushing a `v*` tag
  now produces static binaries for linux and darwin on amd64 and arm64, with
  `SHA256SUMS`, and release notes taken from the matching CHANGELOG section
  rather than generated commit titles -- an operator deciding whether to
  upgrade needs the why, not the list. The script runs identically on a
  workstation, so a maintainer can inspect a release before tagging and a
  user who distrusts the published binary can rebuild and compare hashes.
  It refuses to build when the tag and the newest CHANGELOG entry disagree;
  a binary that reports a version it was not released under costs an hour
  during an incident.
- `SECURITY.md`. This tool holds SSH credentials, writes kubeconfigs and
  installs software as root, so it needs a private reporting path and an
  explicit scope. `--insecure-host-key` accepting any host key is named as
  out of scope, because that is what the flag exists to do.
- `CONTRIBUTING.md`, recording the rules that are decisions rather than
  preferences, and the one lesson this project keeps relearning: observe the
  thing itself. Nearly every defect found so far inferred a state instead of
  measuring it.
- `README.ko.md`, and issue templates that ask for `events.jsonl` -- the event
  stream answers a bug report faster than prose does.

### Changed
- `README.md` is now English, with the Korean text moved to `README.ko.md` and
  the two cross-linked. English is what a public Go repository is read in;
  the design documents stay Korean.
- `scripts/matrix.sh` no longer defaults to the author's lab addresses. The
  script wipes both machines before every case, so a default address is a
  loaded gun pointed at whatever happens to sit at that address on a
  contributor's network. It now refuses to run until both are named.
- `examples/cluster-local.yaml` uses the documentation range (RFC 5737)
  instead of real lab addresses, since examples get copied.
- `examples/cluster-dmz.yaml` no longer references `sops://`. The document
  passed validation but could never run: `sops://` is recognised and rejected
  at resolution time because it is not implemented.

### Fixed
- The issue template quoted `EX-204`, a code that does not exist. The
  registry's own `TestNoDanglingReferences` caught it -- the test scans the
  whole repository, including files added in this change.

## [0.66.0] - 2026-08-26

### Added
- `LICENSE` and `NOTICE`. Apache-2.0, copied verbatim from the canonical text
  with only the appendix placeholder filled in. Until now the repository had
  no licence at all, which under copyright law means nobody but the author
  could use it -- the one thing that has to exist before anything is public.
  Apache-2.0 over MIT for the patent grant: this tool is adopted by companies,
  and a legal review that finds no patent clause stops there.
- `.github/workflows/ci.yml`. The project has claimed for months that "CI blocks
  an engine -> tui import". No CI existed; the sentence described a guard
  nobody ran. The architecture test was already written
  (`internal/engine/arch_test.go`) -- it just was never executed anywhere but
  a developer's terminal. The workflow runs build, vet, `go test -race`,
  gofmt, and checks that `docs/99-codes.md` still matches what
  `internal/codes` generates, since a stale registry is what a customer ticket
  ends up quoting.

### Changed
- Module path `platform.ryxen.dev/malmok` -> `github.com/ryxenix/malmok` across
  272 import sites. A module path is how the world fetches the code; pointing
  it at a personal domain that serves no Go metadata means `go get` fails for
  everyone who is not the author.

  The API group is deliberately NOT renamed. `platform.ryxen.dev/v1alpha1` and
  `platform.ryxen.dev/handoff/v1alpha1` are schema identifiers already written
  into every existing cluster.yaml and into the handoff document downstream tooling
  consumes. They have nothing to do with `go get`, and changing them would
  break documents in the field to no benefit.
- `README.md` rewritten. It opened with "current state: design phase, no
  executable code" -- for a tool that had by then installed a production
  cluster in an IDC. The new text states what is measured on real hardware
  and what is not: single node and two nodes are verified, three-server HA,
  airgap and external registries are not. An infrastructure tool that
  overstates its coverage breaks somebody else's cluster, so the untested
  rows are in the same table as the tested ones rather than omitted.

  Three claims in the first draft of that README were wrong and were caught by
  running them: there is no `malmok build` (it is `apply`), `report` takes
  `--run <id>` and defaults to the newest rather than `--run latest`, and the
  minimal cluster.yaml used field names that do not exist in the schema. The
  example now in the README is one that `malmok plan --validate-only`
  accepts.
- Screenshots moved out of the repository root into `docs/img/`, which is
  gitignored.

All notable changes to malmok are recorded here.

Semantic versioning. The project is pre-1.0 and pre-implementation, so breaking
schema changes land in MINOR releases rather than MAJOR ones.

## [0.65.2] - 2026-08-25

### Fixed

- A local build left the operator with no kubeconfig. The wizard wrote the
  seeded SSH account onto a node this machine *is* -- a node it never dials
  -- and the kubeconfig step reads that account to decide whose copy to make:
  it saw "root", concluded the operator could already read the original, and
  copied nothing. So an IDC build finished with the cluster up, kubectl
  installed and `kubectl get nodes` refusing to connect. A local node now
  carries no login, which is both true and what makes the step fall back to
  the account sudo elevated. Remote nodes are unchanged.

## [0.65.1] - 2026-08-25

### Fixed

- **A finished build was announced as a stopped one.** The finished screen
  called a run failed whenever the findings list held anything, and preflight
  emits its warnings as failed events -- severity is what separates a warning
  from a block. So an IDC build that came up, with its gateway answering on
  the node's address, told the operator the installation had stopped and
  offered a resume command. A run now fails when the run says so: a step
  failure, a blocking check, or an error that stopped the process. Warnings
  are still listed, under a heading that says that is what they are.
- The finished screen shows the run's own error. A step failure has a row in
  the list; a run that stopped for anything else -- a dropped connection, a
  deadline, a phase that never started -- had no row anywhere, and the screen
  said "stopped" with nothing to say why.

## [0.65.0] - 2026-08-25

Both entries come from one IDC build that spent forty-six minutes waiting for
something that could never happen, and said nothing while it did.

### Added

- Waits report where they have got to. A step that waits for a node to be
  Ready, for Cilium to take a configuration, or for a certificate to be
  signed used to print nothing until it finished, so a screen with a spinner
  and an empty log could not be told from a hung one. The waits now print a
  line every few seconds -- containers started and what the node says, the
  agents rolled and what cilium-config carries, which certificates are still
  unsigned -- and the runners carry those lines out while the step is still
  running rather than when it ends.
- **PF-806**: an existing etcd datastore must advertise the address this
  document pins. A node built once and built again keeps its datastore, and
  that datastore records the address the member advertises; pinning node-ip
  afterwards -- which is what this tool now does for a multi-homed node --
  leaves rke2 refusing to start and retrying every five seconds, silently,
  forever. That is what the forty-six minutes were. The state is readable in
  twenty seconds, so preflight reads it and says which addresses disagree and
  what to run.

## [0.64.2] - 2026-08-25

### Changed

- The node-ips note says the cost only where there is one. An endpoint that
  leaves with its node is a real trade when traffic is spread over several;
  on a single node the node leaving takes the cluster with it, so the warning
  was about nothing -- and a screen that warns about nothing teaches the
  operator to skim the warnings that matter. One node now reads that this is
  the only way to be reached without a new address, which is what the site it
  is written for actually faces.

### Fixed

- The schema said node-ips works through the Service's externalIPs. It does
  not, and cannot: Cilium owns that Service and strips a patched externalIPs
  on its next reconcile, which is why the implementation binds the listener
  port in the node's own network namespace instead. The comment now says what
  the code does, including why the observable is "every named node answers"
  rather than the Programmed condition.

## [0.64.1] - 2026-08-25

### Fixed

- The gateway screen wrote over what no screen shows. It rebuilt the gateway
  from its three fields, which deleted the listeners, TLS references and
  namespaces a loaded document may carry -- the one thing the edit flow
  exists not to do. It now edits the gateway in place, creates one only when
  the document has none, and leaves a gateway that names no listeners alone
  so the validator still reports it rather than the wizard quietly repairing
  it. A document that already names a gateway opens on the answer it holds.
- What failed moved above what was built on the finished screen. It sat under
  two record sections, and an eleventh step in the rail was enough to push
  the reason for the failure off a short window.

## [0.64.0] - 2026-08-24

### Added

- **A gateway screen.** The wizard had none, so every document it wrote named
  no gateway: the cluster came up with no way in, a LoadBalancer Service a
  workload created later sat Pending forever, and the operator found out from
  the workload. The screen asks the one question that decides it -- how the
  cluster is reached -- with three answers and what each costs:
  - `none` (the default): nothing is exposed. An address the network did not
    assign is exactly what IDC and air-gapped policy refuse, and at a first
    build there is frequently no name to serve yet.
  - `node-ips`: the nodes' own addresses answer, so nothing new appears on
    the segment. A node that leaves takes its endpoint with it.
  - `lb-pool`: an address from the pool, pinned rather than allocated,
    because the DNS record is requested before the install (PF-612).
  The HTTPS listener appears only where a certificate can exist; the summary
  now states the exposure, so a build that exposes nothing says so before it
  runs rather than after.

## [0.63.0] - 2026-08-24

### Changed

- The RKE2 version leads the node screen. It decides what every node runs and
  it sat below the SSH credentials -- the most consequential answer on the
  screen was the last one an operator reached, after four that rarely change.
  The channel server's answers are now a chooser at the top (stable, latest,
  each with what it means), the version field is the first field under it,
  and typing a version by hand is still the override an air-gapped site
  needs.

## [0.62.1] - 2026-08-24

### Added

- The pointer shows what it is over. Menu entries, choices, fields, settings
  rows, run entries, rail steps and footer buttons light up under the mouse
  -- distinctly from the row the cursor is on, because "what you would get"
  and "what you have" are different claims and drawing them alike makes a
  pointer crossing the screen look like a selection changing by itself.
  Hovering never moves the cursor, so a selection made with the keyboard
  survives a mouse that wanders.

## [0.62.0] - 2026-08-24

### Added

- **The mouse works.** Terminals have reported clicks for years and this tool
  ignored them: menu entries, radio choices, fields, settings rows, run list
  entries, the step rail and the footer buttons are all clickable now, and
  the wheel moves the cursor. Clicking a list entry opens it, the way it does
  everywhere else; clicking a choice selects it, exactly as Space does. The
  rail goes back to a step already answered and never forward, because
  stepping forward past unanswered screens would submit blanks.
  - A click arrives as a row and a column and nothing else, so the renderer
    records what it draws where -- rebuilt every frame, since a stale map
    sends the click to whatever used to be under the pointer. Screens mark
    their rows by the cursor index they stand for, so a click and a keypress
    reach the same code.
  - Keyboard-only use is unchanged: nothing was moved or re-keyed, and a
    terminal that reports no mouse behaves exactly as before.

## [0.61.1] - 2026-08-24

### Changed

- `scripts/build.sh` keeps the five newest binaries and removes the rest
  (`MALMOK_KEEP_BUILDS` to change it). Every code change rebuilds, and a few
  days of that left forty binaries at 16MB each; a handful is enough to go
  back to the build that was running when something was observed. Whatever
  `bin/malmok` points at is never removed.

## [0.61.0] - 2026-08-24

### Added

- **The cluster issues certificates for its own listeners.** An HTTPS
  listener that names no Secret and supplies no material inherits the
  cluster's `pki.mode` -- the schema has always said so, and until now
  nothing did it: the Gateway came up referencing a Secret nobody created,
  the controller reported it Programmed, and every handshake was reset. With
  private-ca (and the ACME modes) the gateway phase now requests a
  cert-manager Certificate per listener, named for the Secret the Gateway
  references, and waits for it to be signed before creating the Gateway.
  Verified on the wire: issuer Ready, certificate Ready, both listeners
  Programmed.
- The matrix covers it: the private-ca case gained an HTTPS listener and
  `private-ca with node-ips` joined the required pairs, so an issuer that
  signs nothing can no longer pass for a working one.

### Changed

- The gateway namespaces are their own manifest, applied before anything
  that lives in them. They used to be the first object of the gateway file,
  which was invisible until something in the same phase needed the namespace
  first -- the listener certificates were rejected outright for a namespace
  nobody had created yet.

### Fixed

- The lab harness keeps a failed case's run directory instead of deleting
  it. Two diagnoses in a row were guesswork because the evidence the tool
  had carefully written went out with `t.TempDir()`.

## [0.60.0] - 2026-08-22

### Added

- The upgrade is a matrix case. `operation` gains `upgrade`, and
  `upgrade-two` builds at the channel's stable release and moves the cluster
  to latest -- two nodes under a VIP, because servers and agents move by
  different paths and the address every node joins through has to keep
  answering while the node serving it restarts. The observable is the version
  each kubelet reports, not the version the document asks for. When the
  channels have converged there is nowhere to upgrade to and the case skips
  rather than passing on a run that did nothing.

## [0.59.1] - 2026-08-22

The matrix's first full run: seven cases passed, one failed, and the one that
failed was the preset nobody had ever built.

### Fixed

- cilium-traefik never finished. The applied-check demanded
  `enable-gateway-api=true` whatever the document said -- right for cilium-gw
  and impossible for a preset that installs no Gateway controller, so the
  step waited out its whole 900-second timeout for a value that was never
  coming. Every expectation now comes from the document, and an absent key
  means the same as a key written false.
- The Gateway API CRDs and the GatewayClass wait ran for every Cilium preset.
  cilium-traefik has no controller to accept a GatewayClass, so even with the
  check fixed the next step would have waited forever. Both now belong to
  presets that run a Gateway controller.
- The Cilium presets never waited for CoreDNS. The canal preset did from the
  start; everything after the dataplane resolves names, so a build that
  reported success while cluster DNS was still starting handed over a cluster
  that could not run anything yet. Both presets share the step now.
- A single-node cluster no longer carries a permanently Pending pod. The
  Cilium operator asks for two replicas that will not share a node, so on one
  node the second could never be scheduled -- a pod this tool created,
  teaching operators that Pending is normal. One node, one operator.

## [0.59.0] - 2026-08-22

### Added

- **A verification matrix, and a harness that runs it.** Six hand-written
  scenarios found thirteen defects in an afternoon, one of which failed every
  from-scratch build -- not because the code was exotic but because nobody
  had walked those paths. `internal/matrix` states the paths as data: seven
  dimensions (nodes, dataplane, pki, exposure, registry, gitops, and the
  operation done to the cluster), eight cases chosen so that every value
  appears and every combination that has actually broken appears together,
  each case carrying the reason it exists.
  - Coverage is proven offline, on every commit: adding a value to a
    dimension fails the build until some case exercises it, and the pairs
    that have broken before (one node with the Gateway API, byo-cert with a
    pool address, resume with issued certificates) are named and checked.
    Per-value coverage would have caught none of them.
  - `test/lab` (build tag `lab`) executes the matrix against real machines:
    every case wipes both nodes, builds with the operator's own binary, and
    then does what its operation says -- grow, resume from a killed run, or
    re-apply and prove nothing changed. Certificate material is generated per
    run and the RKE2 version comes from the channel server, because fixtures
    expire and pinned versions stop testing what people install.
  - `scripts/matrix.sh` runs it; `docs/40-verification-matrix.md` says how to
    read it, how to widen it, and -- honestly -- what is still outside it.

## [0.58.0] - 2026-08-22

Found by a scenario that had never been built: byo-cert PKI, a load-balancer
pool gateway, an HTTPS listener and an upstream registry.

### Fixed

- **Every from-scratch cilium-gw build failed at the Gateway API CRDs, and
  the reason was one missing `|| true`.** Under `set -e`, a command
  substitution that exits non-zero takes the script with it, and
  `have=$(kubectl get crd ... 2>/dev/null)` exits non-zero precisely when
  the CRD is absent -- which is what the wait loop exists to wait for. The
  loop never ran once on a new cluster: the script died silently, and a
  re-run passed only because RKE2 had applied the bundle meanwhile. That
  flakiness cost an IDC build and three scenario runs. Every assignment of
  the same shape is fixed and a test rejects new ones.
- `pki.mode: byo-cert` rendered a ClusterIssuer with an empty spec, which
  the cluster refused with "spec: Required value" after a ten-minute wait.
  byo-cert issues nothing: the document supplied the certificate, so the
  phase now installs nothing at all.
- An HTTPS listener that omits its tls block is documented to inherit the
  cluster's pki.mode -- and the Secret that inheritance lands in had no
  name, so the listener got no certificateRefs, Cilium reported the gateway
  Programmed anyway, and every handshake was reset. Listeners now always
  name their Secret, and the cluster's byo-cert material is assembled into
  it. Verified on the wire: the gateway serves the supplied wildcard.
- PF-502 (clock skew) measures twice before calling it drift. Nodes come up
  from a reboot seconds apart, each reporting itself synchronised while its
  time daemon steps; one sample cannot tell that from clocks that disagree.
  A gap that is closing reports as converging, a gap that is not still
  blocks.

## [0.57.2] - 2026-08-21

### Changed

- PF-502 (clock skew) says how to fix it. The finding named the drift and
  the etcd tolerance and stopped there, which sends an operator to read
  about etcd rather than to the one thing that resolves it: nodes following
  different time servers drift apart while each reports itself synchronised
  (a lab pair sat 1.1s apart that way). The message now names the remedy for
  both systemd-timesyncd and chrony.

## [0.57.1] - 2026-08-21

### Fixed

- A clipped failure told the operator "the full output is in the run's event
  file" and attached nothing, so the one place they were sent to look was the
  one place it was not. Step failures now carry their whole output as the
  event's evidence, and the detail line stays clipped for reading.

## [0.57.0] - 2026-08-21

Found by building three scenarios from bare nodes: an IDC-shaped single node
(no VIP, no certificates), a canal-traefik pair, and the full cilium-gw build.

### Fixed

- Restarting the Cilium operator deadlocked a single-node cluster.
  `rollout restart` surges a new pod, the operator Deployment asks for two
  replicas that will not share a node, and on one node the surge pods stay
  Pending while the old pod is never replaced -- a twenty-minute wait for
  something that could never happen. The operator's pods are deleted
  instead, so the ReplicaSet refills what the cluster can actually place.
- The GatewayClass wait now returns a verdict in two minutes instead of
  spending the whole timeout: if no operator pod is Running by then, nothing
  can ever accept the class, and the step says so with the pod states and
  the scheduling events.
- A step that failed silently reported nothing at all -- "gateway-api-crds
  failed (exit 1):" with an empty message and no evidence, twice, on two
  different steps. Apply now traces its script, so a script that dies under
  `set -e` on a line that prints nothing still names the command that died;
  an empty result reads "the command printed nothing" rather than trailing
  off.
- A profile's baseline could contradict an explicit choice and the document
  was blamed: choosing canal-traefik on a profile whose baseline falls back
  to canal-traefik was refused for "fallback is the same as preset", a value
  the operator never wrote. Inheritance now skips a fallback the preset
  already is; an explicitly written collision is still an error.
- "could not reach 192.168.88.241" now carries the reason (a host key, a
  password, a missing account) instead of sending the operator to the
  network with the answer already in hand.

### Added

- The canal-traefik preset is observed rather than assumed. The dataplane
  phase used to be skipped whole for it -- nothing this tool configures --
  so a canal build never checked the dataplane its workloads depend on. It
  now waits for every canal pod to be Ready and CoreDNS to be available.

## [0.56.2] - 2026-08-21

### Fixed

- A from-scratch build hung at the GatewayClass, for an ordering reason that
  used to be papered over. Cilium's chart renders the GatewayClass only when
  the Gateway API CRDs exist at render time, and its operator checks for
  those CRDs once, at startup -- both happen during bootstrap, before this
  phase installs the CRDs. Until 0.56.0 the dataplane phase wrote the Cilium
  values for the first time here, which changed the HelmChartConfig and
  forced a chart re-run; prestaging those values at bootstrap (the fix for
  the kube-proxy-less deadlock) removed the accident and left the dependency
  showing. The step now repairs instead of waiting: it deletes the completed
  install job so the chart re-renders, and restarts cilium-operator so it
  re-runs its CRD check. A live rebuild went from a 15-minute timeout to 13
  seconds.
- `curl -sfL` hid why a download failed: the Gateway API bundle fetch failed
  on a transient GitHub error and the step reported "exit 1" with nothing
  after the colon. Both fetches (Gateway API bundle, k9s) now retry three
  times and name the URL and exit code when they still fail.
- PF-601 bound its probe listeners without regard to address, so an RKE2
  agent holding 6443 on loopback for its own API load balancer made the
  check skip that port, leave nothing listening for peers, and report the
  resulting failure as a firewall. Listeners now bind the address peers
  actually use, and a port is skipped only when something already serves
  that address.

## [0.56.1] - 2026-08-21

### Fixed

- PF-601 (inter-node port matrix) is now measured against a live listener
  instead of inferred from RST behaviour. The old rule -- "refused means
  reachable, timeout means filtered" -- collapses on a stateful firewall
  that eats the RST a closed port sends back: every pre-install node read
  as filtered on a segment that was open all along, while the working
  production cluster beside it was the disproof. Preflight now binds the
  control-plane ports on every node (systemd-socket-activate, existing
  services left alone), waits until each is up, and connects across: a
  landed connection is proof, anything else is a finding. When a listener
  cannot be arranged the check reports "not measured" rather than guessing
  -- the guess is the bug this replaces.

## [0.56.0] - 2026-08-19

### Fixed

- Bootstrap deadlocked on every fresh Cilium install with kube-proxy
  disabled, found on the first real IDC build: the bundled Cilium came up
  with default values and waited on the in-cluster service IP
  (10.43.0.1:443) that only a running kube-proxy -- or a running Cilium --
  would route, while the values that point it at the API server directly
  (kubeProxyReplacement, k8sServiceHost) sat in the next phase. The
  HelmChartConfig is now prestaged into the manifest directory before
  rke2-server first starts; the dataplane phase keeps maintaining the same
  file afterwards.
- A multi-homed node advertised whichever address holds the default route
  -- on the IDC node, the public interface -- while the operator had named
  the internal address in the document all along. When nodeIP is unset and
  the node's host is an IP literal, it is pinned as node-ip; an explicit
  nodeIP still wins.

### Added

- Operator tools land with the cluster: kubectl (RKE2's own, linked onto
  the PATH from /var/lib/rancher/rke2/bin) and k9s (fetched from its release
  page -- online sites only; an airgapped site is not asked to download what
  it cannot reach, nor checked for it forever).

## [0.55.3] - 2026-08-19

### Fixed

- A real install showed an empty log pane. The install narrates itself
  through its step verdicts ("swap is off and /etc/fstab has no entry",
  "rke2-server is active") and those already arrive in every step event --
  but only the demo ever emitted kind=log, which was all the pane rendered.
  Terminal step verdicts now flow into the log tail, prefixed with their
  step's name.
- The Logs button on the progress screen did nothing. It now widens the log
  tail from a glance (4 lines) to a page (18) and back.

## [0.55.2] - 2026-08-19

### Fixed

- The findings section on a finished progress screen said "what stopped it"
  even when nothing had: four advisory warnings under that heading read as
  four reasons the install cannot happen, while the Continue button was
  live. The heading now tells the truth about weight -- "Warnings, the
  install can proceed" unless something actually blocked or errored.

### Added

- `scripts/build.sh` builds the binary as `bin/malmok-v<version>-<timestamp>`
  (version stamped into `--version` too) and points the `bin/malmok` symlink
  at it, so a binary copied to a server answers "which build is this?" from
  its filename alone.

## [0.55.1] - 2026-08-19

### Fixed

- A failed probe now carries the severity its code is registered with,
  instead of a hardcoded "block". Found on a real IDC install: swap being on
  blocked preflight and told the operator to disable it by hand -- which is
  exactly what l0-node-prep automates, and exactly what PF-105's registry
  entry ("apply will disable it", warn) already said. The same hardcoding
  sat on 20 more probes, including every degrade-registered eBPF check, so a
  node without eBPF was refused outright instead of reaching the plan's
  designed fallback-to-canal path. warnResult stays a deliberate softening
  for sub-cases (an unreadable NTP daemon is not an unreachable NTP server).
- The progress screen now names what stopped the run. The findings were
  collected all along but rendered only on the done screen, so a blocked
  preflight read "1 checks block the install" with the one check nowhere in
  sight, and the operator went digging in events.jsonl for a sentence the
  screen already had. Blocked and failed findings render with their code,
  node and detail once the run stops.

## [0.55.0] - 2026-08-19

### Changed

- The product's working name is the name: `platformctl` is now `malmok`
  everywhere -- the binary (`bin/malmok`), the CLI command, the Go module
  (`platform.ryxen.dev/malmok`), the docs and every message that says its
  own name. The display name stays Malmok. Schema identifiers
  (`platform.ryxen.dev/v1alpha1`, the handoff's
  `platform.ryxen.dev/handoff/v1alpha1`) are domain-based and unchanged, so
  documents and handoff consumers keep working. Preferences move to
  `~/.config/malmok/`; a prefs.yaml under the old name is not migrated.

## [0.54.0] - 2026-08-19

### Changed

- The layout fills the window at fixed proportions, the way k9s sizes its
  panes. The 84-cell content cap is gone -- fields, section rules and the
  form take everything the pane has -- and the rail scales with the window
  (an eighth of the width, clamped 18..28) instead of staying 18 cells on a
  full monitor. A half-screen and a full monitor now show the same shape,
  rather than a fixed column with growing emptiness beside it. Prose is the
  one exception: the explanation strip still wraps at 120 cells, because a
  170-cell line is one the eye loses on the way back.

## [0.53.1] - 2026-08-19

### Changed

- The content column hugs the left of its pane, the way the Ubuntu
  installer's does, instead of floating in the middle of a wide window --
  where it read as small and far away. The column keeps its width cap and
  its vertical centring; what changed is where reading starts. (The glyph
  size itself is the terminal's font setting, which no TUI can change.)

## [0.53.0] - 2026-08-18

### Changed

- The explanations moved from the right-hand pane to a strip above the
  footer. The side pane read as clutter -- a second body competing with the
  first. The strip is a fixed place the eye learns once, full width so two
  lines hold what the pane needed a column for, and outside the content pane
  so the controls keep their rows. The focused field's hint leads, the
  screen's help follows; windows below the chrome's minimum keep the old
  inline layout, and the progress screens still keep every row for the log
  tail.

## [0.52.1] - 2026-08-18

### Fixed

- The start screen carried the explanation pane on wide windows -- a wordmark,
  six choices and an explanation column beside them reads as clutter, not
  help. The menu keeps its inline one-liner under the selected item; the pane
  belongs to the screens with real fields.
- The version field's Space toggle flipped between two version strings nobody
  can tell apart. The value now wears its channel's name -- `v1.35.7+rke2r1
  · stable` / `v1.36.3+rke2r1 · latest`, the channel server's own words.
  Display only, never written to the document, and absent on a hand-typed
  version because that belongs to no channel.

## [0.52.0] - 2026-08-18

### Added

- The machine-readable handoff: every run now writes `artifacts/handoff.json`
  (schema `platform.ryxen.dev/handoff/v1alpha1`) naming the cluster, its
  nodes, the run's verdict, the kubeconfig's location and the dataplane that
  actually carries traffic -- so an inventory tool can bind what was built to
  the machines it runs on without parsing markdown. Written for failed runs
  too, with `run.result: "failed"`: an inventory needs the failure at least
  as much as the success. Nothing secret leaves through it (pinned by test),
  and nothing unmeasured is invented -- absent is absent.
- `apply --output <path>` additionally copies the handoff to a path of the
  caller's choosing; the run directory stays the single source of truth.
- PF-109 (info): the DMI product UUID, measured once per node during
  preflight. On a VM it is the identity the hypervisor stamped in
  (Proxmox `smbios1.uuid`), which makes it the durable fingerprint that
  survives reinstalls; the handoff carries it per node.
- `NodeSpec.Annotations`: an opaque map, like `Metadata.Annotations`, for a
  caller that knows the machine by another name (a Proxmox VMID, an asset
  tag). Passed through to the handoff untouched; nothing interprets it.

### Fixed

- A failed build now still writes its artifacts. Both the headless and the
  wizard path returned before `report.Write`, so the runs with questions to
  answer were exactly the ones without an audit report.
- Simulated runs (`--demo`) now leave the same artifacts a real run does,
  headless and wizard alike, so a consumer can be developed against `--demo`
  output.

## [0.52.0] - 2026-08-18

### Added

- The machine-readable handoff: every run writes `artifacts/handoff.json`
  (schema `platform.ryxen.dev/handoff/v1alpha1`), the document an inventory
  tool reads to learn what cluster now exists, on which machines, built by
  which run -- without parsing markdown. `apply --output <path>` copies it to
  a caller-chosen path additionally; the run directory stays the single
  source of truth. Nothing secret is in it (no passwords, no resolved
  SourceRefs, no kubeconfig contents -- the kubeconfig is named by server
  and path only), and nothing is invented: what was not measured is omitted.
- PF-109 measures each node's DMI product UUID
  (`/sys/class/dmi/id/product_uuid`) -- on a Proxmox VM, the `smbios1.uuid`
  the hypervisor stamped in, which makes it the one identifier that binds a
  node to the VM an inventory knows, across reinstalls and renames.
  Lower-cased on collection; recorded, not judged; a machine without DMI
  skips rather than fails and the handoff omits the field.
- `NodeSpec.Annotations`: an opaque map like `Metadata.Annotations`. A
  caller that knows a node by another name (a Proxmox VMID, an asset tag)
  writes it into the document and reads it back out of the handoff; nothing
  in the engine interprets it.

### Changed

- Artifacts are now written for failed runs too, on every path (headless,
  TUI, `--demo`): the handoff's `run.result` -- succeeded / failed /
  incomplete -- is how the next tool hears that a build stopped, and
  suppressing the record because the build failed left the machine-readable
  trail only for runs nobody has questions about.

### Fixed

- The `--fail-at` example in `apply --help` named a step id format
  (`l1-bootstrap/rke2-server-ready`) that validation rejects; step ids are
  bare (`rke2-server-ready`).

## [0.51.1] - 2026-08-18

### Fixed

- The upgrade progress screen was headed "Installing" -- the screen an
  operator watches for twenty minutes now names the thing that is happening.

### Changed

- The progress screens' log tail sits under its own "Logs" section header,
  so the eye can tell where the verdicts end and the narration begins.
- The run list gained dim column captions (run / when / what happened);
  three unlabelled columns made the reader work out from the values what
  each one was.

## [0.51.0] - 2026-08-18

### Added

- Explanations move to a right-hand pane on wide windows. Screen help,
  mode notes and the focused field's hint used to open every screen and trail
  every field inline, spending the column's vertical rows on prose. When the
  window affords it (pane >= content cap + 38 + rule), the chrome carves an
  "About" pane on the right: the screen's help, the mode-specific notes and
  the hint for whatever the cursor is on render there, and the inline copies
  disappear -- the same sentence twice on one screen is noise. Narrow windows
  keep the old inline layout untouched; the pane is a use of spare width, not
  a requirement. Progress screens (preflight, install, upgrade) never get a
  pane: the log tail is the explanation there.

### Fixed

- The topology sketch drew an empty server address as a node on an
  unresolvable segment ("not an address"). An empty host is a question not
  yet answered, not a bad answer, so it is skipped.

## [0.50.2] - 2026-08-18

### Fixed

- 0.50.1 shrank the whole frame into a floating block, which reads as a dialog
  somebody forgot to maximise -- wrong, and said so. The chrome fills the
  terminal again: header, rules, rail and footer go edge to edge like every
  full-screen tool's. What is centred is the content -- the capped column sits
  in the middle of its pane horizontally, and in the middle of the window
  vertically. Both, not either: full-bleed with a left-hugging column wastes a
  wide window on emptiness, and a shrunken frame wastes the window itself.

## [0.50.1] - 2026-08-18

### Fixed

- The frame is centred. Capping the content column without moving it (0.50.0)
  left everything hugging the top-left of a large window, with the primary
  button stranded at the far right of a 180-column footer -- the cap and the
  position are one decision, not two. The whole block -- header, rail, content,
  footer -- now sits centred both ways, its height capped so the buttons stay
  within reach of the content they act on. The offsets come from the frame's
  fixed capacity rather than from what a screen happens to contain, so the
  block does not jump when a cursor movement grows a help line; a small window
  keeps every cell, exactly as before.

## [0.50.0] - 2026-08-18

### Changed

A layout pass over the content pane.

- The content column is capped at 84 cells. A form is not a table: fields that
  stretch across a wide terminal put the value a head-turn from its label, and
  a 160-column window now gets a calmer column instead of longer brackets.
- Every group of choices sits under a section header -- an accent tick, the
  title, a rule to the edge. Screens that stack four radio groups read as one
  undifferentiated column without it; the rule is what makes a group scannable
  without reading it. The tick is deliberately thinner than the cursor bar,
  because the two share a column and a section wearing the cursor's bar reads
  as a row somebody selected.
- The summary is grouped into Cluster and Platform sections and drops empty
  rows; the final screen's result and artifact groups take the same shape.

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
  and the engineering rules carry it. The CLI, the binary and the module path stay
  `malmok`: a rename there touches every import and every document that
  says `malmok apply`, and a working title is not the moment for that.

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
  it empty when malmok is already running as root.
- The topology panel marks the node that is this machine. Which address the
  operator is sitting at is the one thing a diagram of addresses cannot show,
  and it decides whether a connection is opened at all.
- A cursor left past the end of a screen that shrank is pulled back. The node
  screen loses two rows the moment every address turns out to be local, and a
  cursor beyond the last row highlights nothing while Enter does something other
  than what the screen says.

## [0.42.0] - 2026-08-12

### Added

- A node can be the machine malmok is running on. This is the ordinary
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
- `sudo malmok` needs no second sudo. The local runner reports that it is
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

- `malmok upgrade --to <version>`, and the start menu's Upgrade entry that
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
  `malmok-` prefix produced deployments whose names appear in no
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

- `cmd/malmok/apply_preflight.go`. Both of its functions were superseded:
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

- The title bar says `malmok` rather than `malmok installer`, which
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

- `internal/report` and `malmok report`: the audit report of
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

- `malmok apply -f cluster.yaml` builds a cluster. Every phase existed and
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

- `malmok preflight -f cluster.yaml` runs every check the document calls
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
- Fixtures are generated in the test rather than checked in. A checked-in
  certificate expires and teaches people to ignore a
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
  `malmok cert apply`, the same command that renews them.
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
- `malmok plan -f cluster.yaml [--validate-only]`, which prints the
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

- `malmok apply --demo`: a full simulated run through the phase
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
  of the rule itself. A companion test also bars terminal libraries, since
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
- The naming rules list the `EX-` prefix.

## [0.9.0] - 2026-08-03

### Added

- `cmd/malmok` — the first runnable binary, with `attach` as its only
  subcommand. Commands are registered as they are implemented; one that exists
  and does nothing is worse at a customer site than one that is absent.
- `malmok attach` resolves the event file from `--file`, else the newest
  run under `--bundle`, else the bundle's shared event log. `--follow=false`
  replays a finished run, `--verbose` includes log lines, and `-o json` passes
  the stream through unchanged — the engine already emits JSONL, so that format
  is the stream itself rather than a second serialisation.
- `github.com/spf13/cobra`, the CLI framework the project fixes on.

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
- `malmok attach` and `apply --detach` in `docs/00-architecture.md` §4.
  The engine outliving the renderer needs a way back in, and a dropped SSH
  session at a customer site is routine rather than exceptional.

### Changed

- The engineering rules' prohibition table said the TUI is "merely a cluster.yaml
  generator". That reading is what would make someone remove the TUI's execute
  button, and implementation sessions read those rules rather than the ADRs, so
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
- The engineering rules pointed the JSONL event schema at `docs/90-events.md`
  while their own routing table and the README pointed at `docs/11-execute.md`. Both
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

- The engineering rules now name the generated registry path and state that retired
  numbers are never reused.

## [0.3.0] - 2026-08-03

### Added

- `go.mod` — module `platform.ryxen.dev/malmok`, Go 1.24. Nothing can live
  under `internal/` without it.
- `internal/codes` — the diagnostic code registry, now the single source of
  truth the project requires. 67 preflight codes collected from
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
- The engineering rules — prohibitions, fixed stack, architectural
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
