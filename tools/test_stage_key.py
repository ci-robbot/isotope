#!/usr/bin/env python3
"""Tests for tools/stage-key.py. Run: python3 tools/test_stage_key.py"""

import os
import subprocess
import sys
import tempfile
import unittest

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "stage-key.py")

BASE = """\
# syntax=docker/dockerfile:1
ARG HADRON_VERSION=v1
ARG GPU=vm
FROM example/toolchain:${HADRON_VERSION} AS toolchain

FROM toolchain AS dep
RUN make dep

# A stage nothing below reaches.
FROM toolchain AS unrelated
RUN make unrelated

FROM toolchain AS gpu-vm
RUN make vm
FROM toolchain AS gpu-full
RUN make full
FROM gpu-${GPU} AS gpu

FROM toolchain AS mounted
RUN make mounted

FROM toolchain AS target
ARG GPU
COPY --from=dep /dep /
COPY --from=gpu /gpu /
COPY tools/helper.sh /usr/bin/helper
RUN --mount=type=bind,from=mounted,target=/m \\
    make target
"""


class StageKeyTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        os.mkdir(os.path.join(self.dir, "tools"))
        self.write("tools/helper.sh", "echo one\n")
        self.write("Dockerfile", BASE)

    def write(self, rel, text):
        with open(os.path.join(self.dir, rel), "w") as f:
            f.write(text)

    def key(self, *args, stage="target"):
        out = subprocess.run(
            [sys.executable, SCRIPT, "--dockerfile",
             os.path.join(self.dir, "Dockerfile"), *args, stage],
            capture_output=True, text=True, check=True,
        )
        return out.stdout.strip()

    def edit(self, old, new):
        self.write("Dockerfile", BASE.replace(old, new))

    def test_format(self):
        self.assertRegex(self.key(), r"^target-[0-9a-f]{12}$")

    def test_comments_and_unrelated_stages_do_not_change_it(self):
        k = self.key()
        self.edit("# A stage nothing below reaches.", "# Reworded.\n\n# More.")
        self.assertEqual(self.key(), k)
        self.edit("make unrelated", "make something-else")
        self.assertEqual(self.key(), k)

    def test_every_reached_stage_changes_it(self):
        k = self.key()
        for old, new in [
            ("make dep", "make dep2"),              # COPY --from
            ("make mounted", "make mounted2"),      # RUN --mount from=
            ("make vm", "make vm2"),                # FROM with an ARG
            ("make target", "make target2"),        # the stage itself
            ("AS toolchain", "AS toolchain\nRUN x"),  # the base of all
        ]:
            with self.subTest(old=old):
                self.edit(old, new)
                self.assertNotEqual(self.key(), k)

    def test_the_arg_picks_the_reached_stage(self):
        k = self.key()
        self.edit("make full", "make full2")
        self.assertEqual(self.key(), k)
        self.assertNotEqual(self.key("--arg", "GPU=full"), k)

    def test_build_args(self):
        k = self.key()
        self.assertNotEqual(self.key("--arg", "HADRON_VERSION=v2"), k)
        self.assertEqual(self.key("--arg", "DESKTOP=cosmic"), k)

    def test_context_files(self):
        k = self.key()
        self.write("tools/helper.sh", "echo two\n")
        self.assertNotEqual(self.key(), k)

    def test_bind_mounted_context_files(self):
        self.write("tools/mounted.sh", "echo one\n")
        self.edit("make mounted\n",
                  "make mounted\nRUN --mount=type=bind,source=tools/mounted.sh,target=/m.sh sh /m.sh\n")
        k = self.key()
        self.write("tools/mounted.sh", "echo two\n")
        self.assertNotEqual(self.key(), k)

    def test_symlinks_are_hashed_by_target_not_followed(self):
        os.makedirs(os.path.join(self.dir, "tree", "real"))
        self.write("tree/real/file", "x\n")
        # Dangling in the context: it points at a path inside the image.
        os.symlink("/usr/lib/systemd/system/a.service", os.path.join(self.dir, "tree", "a.service"))
        os.symlink("real", os.path.join(self.dir, "tree", "dirlink"))
        self.edit("COPY tools/helper.sh", "COPY tree/ /\nCOPY tools/helper.sh")
        k = self.key()
        os.remove(os.path.join(self.dir, "tree", "dirlink"))
        os.symlink("elsewhere", os.path.join(self.dir, "tree", "dirlink"))
        k2 = self.key()
        self.assertNotEqual(k2, k)
        os.remove(os.path.join(self.dir, "tree", "a.service"))
        os.symlink("/usr/lib/systemd/system/b.service", os.path.join(self.dir, "tree", "a.service"))
        self.assertNotEqual(self.key(), k2)

    def test_unknown_stage_fails(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.key(stage="nope")


class RepoDockerfileTest(unittest.TestCase):
    """The COSMIC halves are built from a closure that reaches Mesa, which
    differs per GPU line, so the two lines get different stage keys. They link
    only libgbm.so.1 from it, the same on every line, so the Makefile builds
    and looks them up on the full line for every line (see
    test_makefile_keys_cosmic_on_the_full_line)."""

    def key(self, *args):
        repo = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
        out = subprocess.run(
            [sys.executable, SCRIPT, "--dockerfile", os.path.join(repo, "Dockerfile"), *args],
            capture_output=True, text=True, check=True, cwd=repo,
        )
        return out.stdout.strip()

    def test_cosmic_halves_differ_per_gpu_line(self):
        for stage in ("cosmic-shell", "cosmic-apps"):
            with self.subTest(stage=stage):
                vm = self.key("--arg", "DESKTOP=cosmic", "--arg", "GPU=vm", stage)
                full = self.key("--arg", "DESKTOP=cosmic", "--arg", "GPU=full", stage)
                self.assertNotEqual(vm, full)
                # FIRMWARE does not reach COSMIC: the lines share nothing else.
                self.assertEqual(full, self.key("--arg", "DESKTOP=cosmic", "--arg", "GPU=full",
                                                "--arg", "FIRMWARE=true", stage))

    def test_makefile_keys_cosmic_on_the_full_line(self):
        # release.yml prebuilds each half once, with GPU=full, and every cosmic
        # build (vm line too) looks it up through `make print-stage-key`.
        repo = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
        for stage in ("cosmic-shell", "cosmic-apps"):
            with self.subTest(stage=stage):
                full = self.key("--arg", "DESKTOP=cosmic", "--arg", "GPU=full", stage)
                for gpu in ("vm", "full"):
                    out = subprocess.run(
                        ["make", "-s", "DESKTOP=cosmic", f"GPU={gpu}", f"STAGE={stage}", "print-stage-key"],
                        capture_output=True, text=True, check=True, cwd=repo,
                    )
                    self.assertEqual(out.stdout.strip(), full, f"GPU={gpu}")


if __name__ == "__main__":
    unittest.main()
