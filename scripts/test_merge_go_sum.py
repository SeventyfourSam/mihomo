import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("merge-go-sum.py")
SPEC = importlib.util.spec_from_file_location("merge_go_sum", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class MergeGoSumTests(unittest.TestCase):
    def test_independent_changes_and_deletions(self):
        base = "example.test/old v1.0.0 h1:old\nexample.test/keep v1.0.0 h1:keep\n"
        ours = base + "example.test/custom v1.0.0 h1:custom\n"
        theirs = "example.test/keep v1.0.0 h1:keep\nexample.test/new v1.1.0 h1:new\n"
        result = MODULE.parse(MODULE.merge(base, ours, theirs))
        self.assertEqual(result, {
            ("example.test/keep", "v1.0.0"): "h1:keep",
            ("example.test/custom", "v1.0.0"): "h1:custom",
            ("example.test/new", "v1.1.0"): "h1:new",
        })

    def test_different_hashes_always_stop(self):
        a = "example.test/a v1.0.0 h1:a\n"
        b = "example.test/a v1.0.0 h1:b\n"
        for versions in [("", a, b), (a, a, b), (a, b, b), (a, "", b)]:
            with self.subTest(versions=versions), self.assertRaises(ValueError):
                MODULE.merge(*versions)
        with self.assertRaises(ValueError):
            MODULE.parse(a + b)

    def test_go_mod_hash_is_a_distinct_key_and_versions_sort_numerically(self):
        text = "\n".join([
            "example.test/a v1.10.0/go.mod h1:mod",
            "example.test/a v1.10.0 h1:ten",
            "example.test/a v1.9.0 h1:nine",
            "example.test/a v1.10.0-rc.2 h1:rc",
        ]) + "\n"
        result = MODULE.merge("", text, text).splitlines()
        self.assertEqual(result, [text.splitlines()[2], text.splitlines()[3], text.splitlines()[1], text.splitlines()[0]])

    def test_invalid_records_stop(self):
        with self.assertRaises(ValueError):
            MODULE.merge("", "<<<<<<< HEAD\n", "")

    def test_real_git_conflict_is_resolved_but_source_conflicts_are_preserved(self):
        with tempfile.TemporaryDirectory(prefix="mihomo-sum-merge-") as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull)

            def git(*args, check=True):
                return subprocess.run(["git", "-C", directory, *args], env=env, capture_output=True, check=check)

            git("init", "-q", "-b", "custom")
            git("config", "user.name", "Fixture")
            git("config", "user.email", "fixture@example.invalid")
            base = "example.test/a v1.0.0 h1:a\nexample.test/z v1.0.0 h1:z\n"
            (root / "go.sum").write_text(base)
            (root / "source.go").write_text("base\n")
            git("add", ".")
            git("commit", "-qm", "base")
            git("branch", "upstream")
            (root / "go.sum").write_text(base.replace("example.test/z", "example.test/custom v1.0.0 h1:custom\nexample.test/z"))
            git("add", "go.sum")
            git("commit", "-qm", "custom")
            git("checkout", "-q", "upstream")
            (root / "go.sum").write_text(base.replace("v1.0.0 h1:a", "v1.1.0 h1:new"))
            git("add", "go.sum")
            git("commit", "-qm", "upstream")
            git("checkout", "-q", "custom")
            self.assertNotEqual(git("merge", "--no-edit", "upstream", check=False).returncode, 0)
            result = subprocess.run([os.sys.executable, str(SCRIPT)], cwd=root, env=env, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(git("diff", "--name-only", "--diff-filter=U").stdout, b"")
            self.assertIn("h1:custom", (root / "go.sum").read_text())
            git("commit", "-qm", "resolved")
            git("checkout", "-q", "upstream")
            (root / "source.go").write_text("upstream change\n")
            git("add", ".")
            git("commit", "-qm", "source upstream")
            git("checkout", "-q", "custom")
            (root / "source.go").write_text("custom change\n")
            git("add", ".")
            git("commit", "-qm", "source custom")
            self.assertNotEqual(git("merge", "--no-edit", "upstream", check=False).returncode, 0)
            before = (root / "source.go").read_bytes()
            result = subprocess.run([os.sys.executable, str(SCRIPT)], cwd=root, env=env, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual((root / "source.go").read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
