import contextlib
import io
import pathlib
import subprocess
import tempfile
import unittest

from check_publication import audit, check_content, forbidden


class PublicationAuditTests(unittest.TestCase):
    def repo(self, root):
        subprocess.run(["git", "init", "-q", "-b", "main", str(root)], check=True)
        for key, value in [("user.name", "zhongyuming"), ("user.email", "puppetdevzz@gmail.com")]:
            subprocess.run(["git", "-C", str(root), "config", key, value], check=True)

    def test_restricted_paths_and_only_synthetic_example_sql(self):
        for name in ["data/x.csv", "config.local.yaml", "scripts/local-db.env", "scripts/backup.sql", "reports/run.json"]:
            self.assertTrue(forbidden(name))
        self.assertFalse(forbidden("internal/diagnostics/report.go"))
        self.assertFalse(forbidden("examples/schema.sql"))
        self.assertFalse(forbidden("config.local.example.yaml"))

    def test_rule_source_does_not_match_its_own_markers(self):
        source = pathlib.Path(__file__).with_name("check_publication.py")
        self.assertFalse(check_content(source.name, source.read_bytes()))

    def test_findings_never_disclose_matched_values(self):
        secret = "AK" + "IA" + "0" * 16
        marker = "synthetic-restricted-organization"
        findings = check_content("example.txt", (secret + "\n" + marker).encode(), patterns=[marker])
        self.assertEqual(len(findings), 2)
        self.assertNotIn(secret, repr(findings))
        self.assertNotIn(marker, repr(findings))
        self.assertTrue(check_content("config.yaml", b'target:\n  password: "synthetic-value"\n'))
        self.assertFalse(check_content("config.yaml", b'target:\n  password: ""\n'))

    def test_snapshot_never_includes_ignored_private_config(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp) / "repo"
            self.repo(root)
            (root / ".gitignore").write_text("*.local.yaml\n")
            (root / "config.local.yaml").write_text("private runtime configuration")
            (root / "README.md").write_text("public")
            snapshot = pathlib.Path(tmp) / "public"
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(audit(root, snapshot=snapshot), 0)
            self.assertTrue((snapshot / "README.md").is_file())
            self.assertFalse((snapshot / "config.local.yaml").exists())
            self.assertFalse((snapshot / ".git").exists())
            with self.assertRaises(ValueError):
                audit(root, snapshot=snapshot)

    def test_default_history_allows_public_contributor(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            self.repo(root)
            subprocess.run(["git", "-C", str(root), "config", "user.name", "Synthetic Contributor"], check=True)
            subprocess.run(["git", "-C", str(root), "config", "user.email", "contributor@example.invalid"], check=True)
            (root / "README.md").write_text("public fixture")
            subprocess.run(["git", "-C", str(root), "add", "."], check=True)
            subprocess.run(["git", "-C", str(root), "commit", "-qm", "public contribution"], check=True)
            with contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(audit(root, history=True), 0)
                self.assertEqual(audit(root, history=True, expected_identity=("zhongyuming", "puppetdevzz@gmail.com")), 1)

    def test_deleted_private_file_in_history_still_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            self.repo(root)
            (root / "data").mkdir()
            f = root / "data" / "synthetic.csv"
            f.write_text("synthetic")
            subprocess.run(["git", "-C", str(root), "add", "."], check=True)
            subprocess.run(["git", "-C", str(root), "commit", "-qm", "test fixture"], check=True)
            f.unlink()
            subprocess.run(["git", "-C", str(root), "add", "-u"], check=True)
            subprocess.run(["git", "-C", str(root), "commit", "-qm", "remove fixture"], check=True)
            output = io.StringIO()
            with contextlib.redirect_stderr(output), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(audit(root, history=True), 1)
            self.assertIn("restricted-path", output.getvalue())


if __name__ == "__main__":
    unittest.main()
