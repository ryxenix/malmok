"""MkDocs hook: put the version these docs describe on every page.

The site is built from main, not from a release. So it can describe changes no
release carries yet, or lag behind the latest one -- malmok.dev once sat two
days behind -- and a reader had no way to tell which. This reads the nearest
release tag, how many commits main has on top of it, and the commit itself, and
hands them to the template as config.extra.docs_version.

When git cannot answer -- a shallow checkout with no tags, no git at all -- the
line is left out. An absent version is honest; a guessed one is not.
"""

import logging
import subprocess

log = logging.getLogger("mkdocs.hooks.docs_version")


def _git(*args: str) -> str:
    return subprocess.run(["git", *args], capture_output=True, text=True,
                          check=True).stdout.strip()


def on_config(config, **kwargs):
    info = {}
    try:
        release = _git("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*")
        info = {
            "release": release,
            "ahead": int(_git("rev-list", "--count", f"{release}..HEAD")),
            "commit": _git("rev-parse", "--short", "HEAD"),
        }
    except Exception as e:  # noqa: BLE001 -- any failure means: say nothing
        # info, not a warning: the build runs with --strict, and a checkout
        # without tags is not an error in the documentation.
        log.info("docs version left out: %s", e)
    config.extra["docs_version"] = info
    return config
