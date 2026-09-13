"""Decide whether a release builds the image bundle or points at an earlier one.

    python3 scripts/bundle_base.py dist/images.txt v0.97.0 dist/bundle.md

Prints "build" or "reuse", and writes the release notes' section about the
bundle to the third argument either way.

The images change only when a chart version does. Rebuilding them on every tag
pulled twenty-five images twice over from public registries for releases that
changed a line of documentation -- twenty minutes a release, and the step that
broke 0.96.2 when a registry rate-limited the runner. So a release compares
the list its own binary prints with the list of the last release that carried
a bundle, and builds only when they differ.

When it cannot tell -- no network, the API refusing, no earlier bundle -- it
builds. A release that neither carries a bundle nor names one is the outcome
that must not happen; a release that built one it did not need only cost time.
"""

import json
import os
import sys
import urllib.request

REPO = "ryxenix/malmok"
RELEASES = "https://api.github.com/repos/%s/releases?per_page=30" % REPO


def get(url: str) -> bytes:
    """GET a URL. The token goes to the API only: an asset download redirects
    to another host, and a credential has no business following it there."""
    req = urllib.request.Request(url, headers={
        "Accept": "application/vnd.github+json", "User-Agent": "malmok-release"})
    token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")
    if token and url.startswith("https://api.github.com/"):
        req.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(req, timeout=30) as r:
        return r.read()


def image_list(text: str) -> list[str]:
    """The images, without blanks or comments, in one order."""
    return sorted({l.strip() for l in text.splitlines()
                   if l.strip() and not l.lstrip().startswith("#")})


def find_base(tag: str):
    """The newest published release other than this one that carries a bundle,
    with the URLs of what is needed from it -- or None when there is none."""
    for rel in json.loads(get(RELEASES)):
        if rel.get("draft") or rel.get("tag_name") == tag:
            continue
        assets = {a["name"]: a["browser_download_url"] for a in rel.get("assets", [])}
        bundles = {n: u for n, u in assets.items()
                   if n.startswith("malmok-images_") and n.endswith(".tar.zst")}
        if bundles and "images.txt" in assets and "SHA256SUMS" in assets:
            return rel["tag_name"], assets, bundles
    return None


def main() -> int:
    path, tag, out = sys.argv[1:4]
    current = image_list(open(path, encoding="utf-8").read())

    def answer(decision: str, note: str) -> int:
        with open(out, "w", encoding="utf-8") as f:
            f.write("## Image bundle\n\n" + note.strip() + "\n")
        print(decision)
        return 0

    try:
        found = find_base(tag)
        if found is None:
            return answer("build", "Built for this release: no earlier release carries one.")
        base, assets, bundles = found
        previous = image_list(get(assets["images.txt"]).decode())
        sums = {}
        for line in get(assets["SHA256SUMS"]).decode().splitlines():
            parts = line.split()
            if len(parts) == 2:
                sums[parts[1].lstrip("*")] = parts[0]
    except Exception as e:  # noqa: BLE001 -- any failure to look means build
        return answer("build", "Built for this release: the earlier bundle could "
                               "not be looked up (%s)." % e)

    if previous == current:
        rows = "\n".join("| [%s](%s) | `%s` |" % (n, u, sums.get(n, "not listed"))
                         for n, u in sorted(bundles.items()))
        return answer("reuse", """Not rebuilt: this release pulls the same %d images as %s, so the
bundle is that release's. Put it on each node in
`/var/lib/rancher/rke2/agent/images/`, as the air-gap guide describes.

| File | SHA-256 |
|---|---|
%s""" % (len(current), base, rows))

    added = [i for i in current if i not in previous]
    removed = [i for i in previous if i not in current]
    lines = ["- added `%s`" % i for i in added] + ["- removed `%s`" % i for i in removed]
    return answer("build", "Built for this release: the image list changed since %s.\n\n%s"
                  % (base, "\n".join(lines)))


if __name__ == "__main__":
    sys.exit(main())
