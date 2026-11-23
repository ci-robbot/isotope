#!/usr/bin/env python3
"""Check a built Isotope site (the Pages artifact).

    python3 site/check.py DIR [--catalog] [--external]

- Every local link, image, script, stylesheet and srcset entry in every HTML
  file resolves to a file in DIR, and every #fragment to an id on its page.
- Every download button points at a release asset of the repository or at
  its release page, never at another host.
- With --catalog: DIR/extensions.json exists, at the path installed systems
  read (https://<owner>.github.io/<repo>/extensions.json), and is a catalog.
- With --external: every external link answers (HEAD, then GET) with < 400.

Only the Python standard library. Exits 1 and lists every problem.
"""

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from html.parser import HTMLParser

REPO = os.environ.get("GITHUB_REPOSITORY", "kairos-community/isotope")
DOWNLOAD = re.compile(
    rf"^https://github\.com/{re.escape(REPO)}/(releases/latest|releases/download/v[^/]+/isotope-[a-z0-9]+(-vm|-uki)?-v[^/]+-amd64\.iso(\.sha256)?)$"
)


class Page(HTMLParser):
    def __init__(self):
        super().__init__()
        self.ids = set()
        self.refs = []  # (attribute, value, classes)
        self.images_without_alt = []

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if "id" in a:
            self.ids.add(a["id"])
        if tag == "a" and "name" in a:
            self.ids.add(a["name"])
        if tag == "img" and "alt" not in a:
            self.images_without_alt.append(a.get("src", "?"))
        cls = a.get("class", "")
        for attr in ("href", "src"):
            if a.get(attr):
                self.refs.append((tag, attr, a[attr], cls))
        if a.get("srcset"):
            for part in a["srcset"].split(","):
                url = part.strip().split()[0]
                self.refs.append((tag, "srcset", url, cls))


def check(root, catalog, external):
    problems = []
    if not os.path.isfile(os.path.join(root, "index.html")):
        problems.append("index.html is missing")
    pages = {}
    for dirpath, _, files in os.walk(root):
        for f in files:
            if f.endswith(".html"):
                path = os.path.join(dirpath, f)
                p = Page()
                with open(path, encoding="utf-8") as fh:
                    p.feed(fh.read())
                pages[path] = p

    externals = set()
    buttons = 0
    for path, page in pages.items():
        rel = os.path.relpath(path, root)
        for src in page.images_without_alt:
            problems.append(f"{rel}: <img src={src}> has no alt")
        for tag, attr, url, cls in page.refs:
            u = urllib.parse.urlsplit(url)
            if "dl-button" in cls.split() or "pick-button" in cls.split():
                buttons += 1
                if not DOWNLOAD.match(url):
                    problems.append(f"{rel}: download link {url} is not a release asset of {REPO}")
            if u.scheme in ("http", "https"):
                if u.scheme == "http":
                    problems.append(f"{rel}: {url} is not https")
                externals.add(url)
                continue
            if u.scheme in ("mailto",):
                continue
            if u.scheme or url.startswith("//"):
                problems.append(f"{rel}: {attr}={url}: unexpected scheme")
                continue
            target = path
            if u.path:
                if u.path.startswith("/"):
                    problems.append(f"{rel}: {url} is absolute; Pages serves the site under /<repo>/")
                    continue
                target = os.path.normpath(os.path.join(os.path.dirname(path), urllib.parse.unquote(u.path)))
                if os.path.isdir(target):
                    target = os.path.join(target, "index.html")
                if not os.path.exists(target):
                    problems.append(f"{rel}: {attr}={url}: no such file")
                    continue
            if u.fragment and target in pages and u.fragment not in pages[target].ids:
                problems.append(f"{rel}: {url}: no element with id '{u.fragment}'")
    if buttons == 0:
        problems.append("no download buttons found")

    if catalog:
        cat = os.path.join(root, "extensions.json")
        try:
            with open(cat) as f:
                data = json.load(f)
            if not isinstance(data.get("repo"), str) or not isinstance(data.get("layers"), list):
                raise ValueError("want an object with 'repo' and 'layers'")
            for layer in data["layers"]:
                if not layer.get("name") or not isinstance(layer.get("tags"), list):
                    raise ValueError(f"layer without a name or tags: {layer}")
        except (OSError, ValueError) as e:
            problems.append(f"extensions.json at the site root: {e}")

    if external:
        for url in sorted(externals):
            if DOWNLOAD.match(url) and "/releases/download/" in url:
                continue  # the build read these from the API a moment ago
            if not reachable(url):
                problems.append(f"external link does not answer: {url}")

    return problems, len(pages), len(externals)


def reachable(url):
    for method in ("HEAD", "GET"):
        req = urllib.request.Request(url, method=method, headers={"User-Agent": "isotope-site-check"})
        try:
            with urllib.request.urlopen(req, timeout=20) as r:
                if r.status < 400:
                    return True
        except urllib.error.HTTPError as e:
            if e.code < 400:
                return True
        except (urllib.error.URLError, OSError):
            pass
    return False


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("dir")
    ap.add_argument("--catalog", action="store_true", help="require extensions.json at the root")
    ap.add_argument("--external", action="store_true", help="also request every external link")
    args = ap.parse_args()
    problems, npages, next_ = check(args.dir, args.catalog, args.external)
    for p in problems:
        print(f"check-site: {p}", file=sys.stderr)
    if problems:
        sys.exit(1)
    print(f"check-site: {npages} page(s), {next_} external link(s), all local links resolve"
          + (", extensions.json is a catalog" if args.catalog else ""))


if __name__ == "__main__":
    main()
