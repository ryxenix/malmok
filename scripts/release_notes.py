"""Write the release notes for a tag: a short list, then what it pins.

    python3 scripts/release_notes.py v0.95.0 > dist/notes.md

Two things a person reads a release page for. What changed, at a glance -- not
the reasoning, which is what CHANGELOG.md is and stays. And what this build
fixes the versions of, which the notes did not say at all: an operator deciding
whether to take a release wants the chart and payload versions in front of
them, and until now the only way to find them was to read the source.

So each bullet is cut to its first sentence and its sub-bullets dropped, and
the versions come from the binary and the release script rather than from a
table somebody has to remember to update.
"""

import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent


def section(tag: str) -> list[str]:
    """The CHANGELOG entry for the tag, as its own lines."""
    want = "## [" + tag.lstrip("v") + "]"
    out, on = [], False
    for line in (ROOT / "CHANGELOG.md").read_text(encoding="utf-8").splitlines():
        if line.startswith("## ["):
            if on:
                break
            on = line.startswith(want)
            continue
        if on:
            out.append(line)
    return out


def summarise(lines: list[str]) -> list[str]:
    """Headings, and one sentence per top-level bullet.

    A sub-bullet is the detail behind a change and belongs in the changelog;
    what the release page needs is the change itself.
    """
    out: list[str] = []
    bullet: list[str] = []

    def flush() -> None:
        if not bullet:
            return
        text = " ".join(w.strip() for w in bullet).strip()
        # The first sentence, with the full stop kept. Abbreviations are not a
        # risk here: these are English sentences about software, and a version
        # like v1.36.4 has no space after its dots.
        if m := re.search(r"(?<!\bv\d)\.\s", text + " "):
            text = text[: m.start() + 1]
        out.append("- " + text.lstrip("- "))
        bullet.clear()

    for line in lines:
        if line.startswith("###"):
            flush()
            out.extend(["", line])
        elif line.startswith("- "):
            flush()
            bullet.append(line)
        elif line.startswith("  - "):
            flush()  # a sub-bullet ends the one above and is not carried
        elif line.strip() and bullet:
            bullet.append(line)
        elif not line.strip():
            flush()
    flush()
    return out


def charts() -> list[tuple[str, str]]:
    """The charts this release pins, from the binary that installs them."""
    res = subprocess.run(
        ["go", "run", "./cmd/malmok", "images", "--charts"],
        cwd=ROOT, capture_output=True, text=True, check=True)
    rows = []
    for line in res.stdout.splitlines():
        f = line.split("\t")
        if len(f) >= 2:
            rows.append((f[0], f[1]))
    return sorted(rows)


def grep(path: str, pattern: str) -> str:
    """One pinned value out of a file, so the notes cannot drift from it."""
    text = (ROOT / path).read_text(encoding="utf-8")
    m = re.search(pattern, text, re.MULTILINE)
    return m.group(1) if m else "unknown"


def main() -> int:
    tag = sys.argv[1] if len(sys.argv) > 1 else ""
    if not tag:
        print("usage: release_notes.py <tag>", file=sys.stderr)
        return 2

    body = summarise(section(tag))
    if not body:
        print(f"CHANGELOG.md has no section for {tag}", file=sys.stderr)
        return 1

    print("\n".join(body).strip())

    print("\n## Pinned versions\n")
    print("| Component | Version |")
    print("|---|---|")
    for name, version in charts():
        print(f"| {name} | {version} |")
    print(f"| local-path-provisioner | {grep('internal/storage/storage.go', r'ProvisionerVersion = \"(.+?)\"')} |")
    print(f"| helm (airgap build) | {grep('scripts/release.sh', r'^helm_version=(.+)$')} |")
    print(f"| k9s (airgap build) | {grep('scripts/release.sh', r'^k9s_version=(.+)$')} |")
    print(f"| Go | {grep('go.mod', r'^go (.+)$')} |")

    # Said rather than left out: RKE2 is the one version this tool does not
    # fix, and somebody reading a table of pinned versions will look for it.
    print("\nRKE2 itself is not pinned. The version comes from the channel "
          "server, or from `kubernetes.version` in the document.\n")
    print("Full detail, with the reasoning, is in "
          "[CHANGELOG.md](CHANGELOG.md).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
