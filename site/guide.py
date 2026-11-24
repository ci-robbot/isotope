"""The user guide: site/guide/*.md rendered to HTML pages under guide/.

build.py calls build(). The Markdown is a small subset, enough for
task-oriented pages, and it reads well on GitHub too:

- `#` to `####` headings; each gets an id from its text (`## Turn it on` is
  `#turn-it-on`).
- Paragraphs, `-` lists, `1.` lists (one level; a line indented under an item
  continues it), `|` tables with a `|---|` line under the header, ``` code
  blocks, and `>` notes.
- Inline: `code`, **bold**, [text](target), and [[Super+Escape]] for keys.
- An image alone on its line, `![alt](path)`. The path is relative to the
  Markdown file and may point anywhere in the repository (the screenshot
  goldens in cmd/*/testdata); the build copies it to guide/img/.

Links: `page.md` and `page.md#id` become `page.html`; a relative path to
another file of the repository becomes its page on GitHub. The order of the
pages is the order of the links in index.md, and every page must be listed
there. A broken link or image stops the build; site/check.py checks the
fragments in the built pages.
"""

import html
import os
import re
import shutil
import struct

SITE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(SITE)
GUIDE = os.path.join(SITE, "guide")

e = html.escape


class GuideError(Exception):
    pass


def slug(text):
    s = re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")
    return s or "section"


def png_size(path):
    """(width, height) of a PNG, or None."""
    with open(path, "rb") as f:
        head = f.read(24)
    if head[:8] != b"\x89PNG\r\n\x1a\n" or head[12:16] != b"IHDR":
        return None
    return struct.unpack(">II", head[16:24])


# --- inline --------------------------------------------------------------

INLINE = re.compile(
    r"`(?P<code>[^`]+)`"
    r"|\[\[(?P<keys>[^\]]+)\]\]"
    r"|\*\*(?P<bold>.+?)\*\*"
    r"|\[(?P<text>[^\]]+)\]\((?P<href>[^)\s]+)\)"
)


def inline(text, link):
    """Inline Markdown to HTML. link(target) gives the href of a link."""
    out, pos = [], 0
    for m in INLINE.finditer(text):
        out.append(e(text[pos:m.start()], quote=False))
        pos = m.end()
        if m.group("code") is not None:
            out.append(f"<code>{e(m.group('code'), quote=False)}</code>")
        elif m.group("keys") is not None:
            keys = [k.strip() for k in m.group("keys").split("+")]
            out.append('<span class="keys">'
                       + "+".join(f"<kbd>{e(k, quote=False)}</kbd>" for k in keys)
                       + "</span>")
        elif m.group("bold") is not None:
            out.append(f"<strong>{inline(m.group('bold'), link)}</strong>")
        else:
            out.append(f'<a href="{e(link(m.group("href")))}">{inline(m.group("text"), link)}</a>')
    out.append(e(text[pos:], quote=False))
    return "".join(out)


# --- blocks --------------------------------------------------------------

HEADING = re.compile(r"^(#{1,4})\s+(.*?)\s*#*$")
ULIST = re.compile(r"^[-*]\s+(.*)$")
OLIST = re.compile(r"^\d+\.\s+(.*)$")
IMAGE = re.compile(r"^!\[(?P<alt>[^\]]*)\]\((?P<src>[^)\s]+)\)$")


class Page:
    def __init__(self, name):
        self.name = name  # "install" for install.md
        self.title = ""
        self.headings = []  # (level, id, text)
        self.html = ""
        self.links = []  # the .md targets, in order


def render(name, text, link, image):
    """Render one Markdown page. link(target) and image(src) map the targets;
    both raise GuideError for a broken one."""
    page = Page(name)
    ids = set()
    out = []
    lines = text.splitlines()
    i = 0

    def lnk(target):
        href = link(target)
        page.links.append(target)
        return href

    def para(block):
        return " ".join(s.strip() for s in block)

    while i < len(lines):
        line = lines[i]
        if not line.strip():
            i += 1
            continue
        m = HEADING.match(line)
        if m:
            level, words = len(m.group(1)), m.group(2)
            if level == 1:
                if page.title:
                    raise GuideError(f"{name}.md: a second # heading: {words}")
                page.title = words
                out.append(f"<h1>{inline(words, lnk)}</h1>")
            else:
                hid = base = slug(re.sub(r"[`*\[\]]", "", words))
                n = 2
                while hid in ids:
                    hid, n = f"{base}-{n}", n + 1
                ids.add(hid)
                page.headings.append((level, hid, words))
                out.append(f'<h{level} id="{hid}">{inline(words, lnk)}</h{level}>')
            i += 1
            continue
        if line.startswith("```"):
            j = i + 1
            while j < len(lines) and not lines[j].startswith("```"):
                j += 1
            if j == len(lines):
                raise GuideError(f"{name}.md: a code block that does not end")
            code = "\n".join(lines[i + 1:j])
            out.append(f'<pre class="term"><code>{e(code, quote=False)}</code></pre>')
            i = j + 1
            continue
        if line.startswith(">"):
            block = []
            while i < len(lines) and lines[i].startswith(">"):
                block.append(lines[i][1:].lstrip())
                i += 1
            paras = "\n".join(block).split("\n\n")
            body = "".join(f"<p>{inline(para(p.splitlines()), lnk)}</p>" for p in paras if p.strip())
            out.append(f'<aside class="note">{body}</aside>')
            continue
        if line.startswith("|"):
            rows = []
            while i < len(lines) and lines[i].startswith("|"):
                rows.append([c.strip() for c in lines[i].strip().strip("|").split("|")])
                i += 1
            if len(rows) < 2 or not all(re.fullmatch(r":?-+:?", c) for c in rows[1]):
                raise GuideError(f"{name}.md: a table needs a |---| line under its header")
            head = "".join(f'<th scope="col">{inline(c, lnk)}</th>' for c in rows[0])
            body = "".join("<tr>" + "".join(f"<td>{inline(c, lnk)}</td>" for c in r) + "</tr>"
                           for r in rows[2:])
            out.append(f'<div class="table"><table><thead><tr>{head}</tr></thead>'
                       f"<tbody>{body}</tbody></table></div>")
            continue
        if ULIST.match(line) or OLIST.match(line):
            ordered = bool(OLIST.match(line))
            marker = OLIST if ordered else ULIST
            items = []
            while i < len(lines):
                m = marker.match(lines[i])
                if m:
                    items.append([m.group(1)])
                elif lines[i].startswith("  ") and lines[i].strip() and items:
                    items[-1].append(lines[i])
                else:
                    break
                i += 1
            tag = "ol" if ordered else "ul"
            lis = "".join(f"<li>{inline(para(it), lnk)}</li>" for it in items)
            out.append(f"<{tag}>{lis}</{tag}>")
            continue
        m = IMAGE.match(line.strip())
        if m:
            src, w, h = image(m.group("src"))
            size = f' width="{w}" height="{h}"' if w else ""
            out.append(f'<figure><img class="shot" src="{e(src)}" alt="{e(m.group("alt"))}"'
                       f'{size} loading="lazy"></figure>')
            i += 1
            continue
        block = []
        while i < len(lines) and lines[i].strip() and not (
                HEADING.match(lines[i]) or lines[i].startswith(("```", ">", "|"))
                or ULIST.match(lines[i]) or OLIST.match(lines[i])):
            block.append(lines[i])
            i += 1
        out.append(f"<p>{inline(para(block), lnk)}</p>")

    if not page.title:
        raise GuideError(f"{name}.md: no # title")
    page.html = "\n".join(out)
    return page


# --- the pages -----------------------------------------------------------

def order(index_text):
    """The page names in the order index.md links them, index first."""
    names = ["index"]
    for target in re.findall(r"\]\(([^)#\s]+\.md)(?:#[^)]*)?\)", index_text):
        n = target[:-3]
        if "/" not in n and n not in names:
            names.append(n)
    return names


def build(out, repo_url, guide_dir=GUIDE, template=None):
    """Write out/guide/*.html. Returns the pages in order."""
    sources = sorted(f[:-3] for f in os.listdir(guide_dir) if f.endswith(".md"))
    if "index" not in sources:
        raise GuideError("guide/index.md is missing")
    with open(os.path.join(guide_dir, "index.md"), encoding="utf-8") as f:
        names = order(f.read())
    missing = [n for n in names if n not in sources]
    if missing:
        raise GuideError(f"index.md links pages that do not exist: {missing}")
    orphans = [n for n in sources if n not in names]
    if orphans:
        raise GuideError(f"pages not linked from index.md: {orphans}")

    dst = os.path.join(out, "guide")
    os.makedirs(os.path.join(dst, "img"), exist_ok=True)
    copied = {}

    def link(target):
        if re.match(r"^[a-z]+:", target) or target.startswith("#"):
            return target
        path, _, frag = target.partition("#")
        frag = "#" + frag if frag else ""
        if "/" not in path and path.endswith(".md"):
            if path[:-3] not in sources:
                raise GuideError(f"link to a page that does not exist: {target}")
            return path[:-3] + ".html" + frag
        full = os.path.normpath(os.path.join(guide_dir, path))
        rel = os.path.relpath(full, ROOT)
        if rel.startswith("..") or not os.path.exists(full):
            raise GuideError(f"link to a file that is not in the repository: {target}")
        return f"{repo_url}/blob/main/{rel}{frag}"

    def image(src):
        full = os.path.normpath(os.path.join(guide_dir, src))
        rel = os.path.relpath(full, ROOT)
        if rel.startswith("..") or not os.path.isfile(full):
            raise GuideError(f"image that is not in the repository: {src}")
        name = copied.get(full)
        if name is None:
            # cmd/isotope-system/testdata/apps-browse.png -> isotope-system-apps-browse.png
            parts = [p for p in rel.split(os.sep) if p not in ("cmd", "testdata", "site", "guide")]
            name = "-".join(parts)
            copied[full] = name
            shutil.copy(full, os.path.join(dst, "img", name))
        size = png_size(full) or (0, 0)
        return f"img/{name}", size[0], size[1]

    pages = []
    for n in names:
        with open(os.path.join(guide_dir, n + ".md"), encoding="utf-8") as f:
            try:
                pages.append(render(n, f.read(), link, image))
            except GuideError as err:
                raise GuideError(f"{n}.md: {err}") from None

    if template is None:
        with open(os.path.join(SITE, "guide.html"), encoding="utf-8") as f:
            template = f.read()
    for i, p in enumerate(pages):
        with open(os.path.join(dst, p.name + ".html"), "w", encoding="utf-8") as f:
            f.write(page_html(template, pages, i, repo_url))
    return pages


def page_html(template, pages, i, repo_url):
    p = pages[i]
    nav = []
    for q in pages:
        cur = ' aria-current="page"' if q is p else ""
        label = "Overview" if q.name == "index" else q.title
        nav.append(f'<li><a href="{q.name}.html"{cur}>{e(label)}</a></li>')
    toc = ""
    subs = [(hid, words) for level, hid, words in p.headings if level == 2]
    if len(subs) >= 3:
        toc = ('<nav class="guide-toc" aria-label="On this page"><p>On this page</p><ul>'
               + "".join(f'<li><a href="#{hid}">{inline(w, lambda t: t)}</a></li>' for hid, w in subs)
               + "</ul></nav>")
    pn = []
    if i > 0:
        q = pages[i - 1]
        pn.append(f'<a class="guide-prev" href="{q.name}.html"><span>Previous</span>{e(q.title)}</a>')
    if i + 1 < len(pages):
        q = pages[i + 1]
        pn.append(f'<a class="guide-next" href="{q.name}.html"><span>Next</span>{e(q.title)}</a>')
    title = "Isotope user guide" if p.name == "index" else f"{p.title} · Isotope user guide"
    values = {
        "title": e(title),
        "nav": "\n".join(nav),
        "content": p.html.replace("</h1>", "</h1>\n" + toc, 1),
        "prevnext": "".join(pn),
        "repo_url": repo_url,
        "source": f"{repo_url}/blob/main/site/guide/{p.name}.md",
    }
    out = template
    for k, v in values.items():
        out = out.replace("{{" + k + "}}", v)
    left = re.findall(r"\{\{[a-z_]+\}\}", out)
    if left:
        raise GuideError(f"unfilled placeholders in guide.html: {left}")
    return out
