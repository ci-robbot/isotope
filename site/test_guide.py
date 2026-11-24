"""Tests for the user guide's build (site/guide.py) and its checks (site/check.py).

    python3 -m unittest discover -s site -p 'test_*.py'
"""

import os
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import check  # noqa: E402
import guide  # noqa: E402

REPO = "https://github.com/kairos-community/isotope"


def no_links(target):
    raise AssertionError(f"unexpected link {target}")


class Inline(unittest.TestCase):
    def test_code_keys_bold_and_links(self):
        got = guide.inline("Press [[Super+Escape]], then **Install** `isotope <x>` [page](a.html).",
                           lambda t: t)
        self.assertEqual(
            got,
            'Press <span class="keys"><kbd>Super</kbd>+<kbd>Escape</kbd></span>, then '
            "<strong>Install</strong> <code>isotope &lt;x&gt;</code> "
            '<a href="a.html">page</a>.')

    def test_text_is_escaped(self):
        self.assertEqual(guide.inline("a < b & c", no_links), "a &lt; b &amp; c")


class Render(unittest.TestCase):
    def render(self, text, link=lambda t: t, image=None):
        return guide.render("page", text, link, image or (lambda s: (s, 0, 0)))

    def test_blocks(self):
        p = self.render("# Title\n\nIntro\nline two.\n\n## Do it\n\n1. One\n   more.\n2. Two\n\n"
                        "- a\n- b\n\n> Careful.\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n```\nx < y\n```\n")
        self.assertEqual(p.title, "Title")
        self.assertEqual(p.headings, [(2, "do-it", "Do it")])
        self.assertIn("<p>Intro line two.</p>", p.html)
        self.assertIn("<ol><li>One more.</li><li>Two</li></ol>", p.html)
        self.assertIn("<ul><li>a</li><li>b</li></ul>", p.html)
        self.assertIn('<aside class="note"><p>Careful.</p></aside>', p.html)
        self.assertIn('<th scope="col">A</th>', p.html)
        self.assertIn("<td>2</td>", p.html)
        self.assertIn('<pre class="term"><code>x &lt; y</code></pre>', p.html)

    def test_duplicate_headings_get_their_own_id(self):
        p = self.render("# T\n\n## Same\n\n## Same\n")
        self.assertEqual([h[1] for h in p.headings], ["same", "same-2"])

    def test_image(self):
        p = self.render("# T\n\n![A screen](x.png)\n", image=lambda s: ("img/x.png", 10, 20))
        self.assertIn('<img class="shot" src="img/x.png" alt="A screen" width="10" height="20"', p.html)

    def test_errors(self):
        for text in ("no title", "# A\n\n# B\n", "# T\n\n```\nopen", "# T\n\n| a |\n| b |\n"):
            with self.assertRaises(guide.GuideError, msg=text):
                self.render(text)


class Build(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.src = os.path.join(self.tmp, "src")
        os.makedirs(self.src)

    def tearDown(self):
        shutil.rmtree(self.tmp)

    def write(self, name, text):
        with open(os.path.join(self.src, name), "w") as f:
            f.write(text)

    def build(self):
        return guide.build(os.path.join(self.tmp, "out"), REPO, guide_dir=self.src,
                           template="{{title}}|{{nav}}|{{content}}|{{prevnext}}|{{repo_url}}|{{source}}")

    def test_order_links_and_prev_next(self):
        self.write("index.md", "# Guide\n\n- [B](b.md)\n- [A](a.md#x)\n")
        self.write("a.md", "# A\n\nBack to [B](b.md#y).\n")
        self.write("b.md", "# B\n")
        pages = self.build()
        self.assertEqual([p.name for p in pages], ["index", "b", "a"])
        with open(os.path.join(self.tmp, "out", "guide", "a.html")) as f:
            a = f.read()
        self.assertIn('href="b.html#y"', a)
        self.assertIn('class="guide-prev" href="b.html"', a)
        self.assertNotIn("guide-next", a)

    def test_a_page_not_in_the_index_is_an_error(self):
        self.write("index.md", "# Guide\n")
        self.write("lost.md", "# Lost\n")
        with self.assertRaisesRegex(guide.GuideError, "not linked from index.md"):
            self.build()

    def test_a_broken_link_is_an_error(self):
        self.write("index.md", "# Guide\n\n[Gone](gone.md)\n")
        with self.assertRaisesRegex(guide.GuideError, "do not exist"):
            self.build()
        self.write("index.md", "# Guide\n\n[File](no-such-file.txt)\n")
        with self.assertRaisesRegex(guide.GuideError, "not in the repository"):
            self.build()

    def test_the_real_guide_builds_and_passes_the_checks(self):
        out = os.path.join(self.tmp, "site")
        os.makedirs(out)
        pages = guide.build(out, REPO)
        self.assertGreater(len(pages), 5)
        # The pieces of the site the guide links to.
        for name in ("site.css", "favicon.svg", "LICENSE.txt"):
            open(os.path.join(out, name), "w").close()
        with open(os.path.join(out, "index.html"), "w") as f:
            f.write('<a href="guide/">Guide</a><a id="download" class="dl-button" '
                    f'href="{REPO}/releases/latest">x</a>')
        problems, _, _ = check.check(out, catalog=False, external=False)
        self.assertEqual(problems, [])


class Check(unittest.TestCase):
    def site(self, files):
        root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, root)
        for name, text in files.items():
            path = os.path.join(root, name)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w") as f:
                f.write(text)
        return root

    def problems(self, files):
        files.setdefault("index.html", f'<a href="guide/">G</a><a class="dl-button" href="{REPO}/releases/latest">x</a>')
        return check.check(self.site(files), catalog=False, external=False)[0]

    def test_guide_rules(self):
        self.assertIn("guide/index.html is missing: the user guide was not built", self.problems({}))
        got = self.problems({
            "index.html": f'<a class="dl-button" href="{REPO}/releases/latest">x</a>',
            "guide/index.html": '<a href="a.html#nope">A</a><a href="b.md">B</a>',
            "guide/a.html": "<h2 id='yes'>A</h2>",
            "guide/orphan.html": "",
        })
        self.assertIn("index.html does not link to the user guide (guide/)", got)
        self.assertIn("guide/index.html: a.html#nope: no element with id 'nope'", got)
        self.assertIn("guide/index.html: href=b.md points at Markdown, not at a built page", got)
        self.assertIn("guide/orphan.html: not linked from guide/index.html", got)


if __name__ == "__main__":
    unittest.main()
