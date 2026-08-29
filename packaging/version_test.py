"""Unit tests for packaging/version.py (pure helpers + a git fixture repo)."""
import os
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(__file__))
import version  # noqa: E402


class PureHelpers(unittest.TestCase):
    def test_parse_tag(self):
        self.assertEqual(version.parse_tag("v0.1"), (0, 1))
        self.assertEqual(version.parse_tag("v12.345"), (12, 345))
        self.assertIsNone(version.parse_tag("v1.2.3"))
        self.assertIsNone(version.parse_tag("v0.1-3-gabc1234"))
        self.assertIsNone(version.parse_tag("0.1"))

    def test_next_patch(self):
        self.assertEqual(version.next_patch(None), "v0.1")
        self.assertEqual(version.next_patch("v0.1"), "v0.2")
        self.assertEqual(version.next_patch("v0.9"), "v0.10")
        self.assertEqual(version.next_patch("v3.41"), "v3.42")

    def test_deb_version(self):
        self.assertEqual(version.deb_version("v0.2"), "0.2")
        with self.assertRaises(ValueError):
            version.deb_version("v0.1-3-gabc1234")


class GitRepo(unittest.TestCase):
    """Exercise the git-reading helpers against a throwaway repository."""

    def setUp(self):
        self.dir = tempfile.mkdtemp(prefix="ct-version-", dir=os.getcwd())
        self.addCleanup(lambda: subprocess.run(["rm", "-r", self.dir], check=True))
        self.cwd = os.getcwd()
        os.chdir(self.dir)
        self.addCleanup(os.chdir, self.cwd)
        env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@x",
               "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@x"}
        self.env = env
        subprocess.run(["git", "init", "-q", "-b", "main"], check=True, env=env)
        self.commit("one")

    def commit(self, msg):
        subprocess.run(["git", "commit", "-q", "--allow-empty", "-m", msg],
                       check=True, env=self.env)

    def tag(self, name):
        subprocess.run(["git", "tag", name], check=True, env=self.env)

    def test_no_tags_starts_at_v0_1(self):
        self.assertIsNone(version.exact_tag())
        self.assertIsNone(version.latest_reachable_tag())
        self.assertEqual(version.head_tag(), "v0.1")

    def test_exact_tag_wins(self):
        self.tag("v0.1")
        self.assertEqual(version.exact_tag(), "v0.1")
        self.assertEqual(version.head_tag(), "v0.1")

    def test_next_patch_after_untagged_commit(self):
        self.tag("v0.1")
        self.commit("two")
        self.assertIsNone(version.exact_tag())
        self.assertEqual(version.latest_reachable_tag(), "v0.1")
        self.assertEqual(version.head_tag(), "v0.2")

    def test_non_matching_tags_ignored(self):
        self.tag("v0.0")
        self.tag("latest")
        self.commit("two")
        self.tag("v0.1-rc")  # not vX.Y
        self.assertEqual(version.head_tag(), "v0.1")


if __name__ == "__main__":
    unittest.main()
