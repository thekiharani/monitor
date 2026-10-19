import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("changes.sh").resolve()
OFF = {"build": "false", "test": "false", "compose": "false"}
BUILD = {**OFF, "build": "true", "test": "true"}
ALL = {key: "true" for key in OFF}
TEST = {**OFF, "test": "true"}
COMPOSE = {**OFF, "compose": "true"}


class ChangeDetectionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="monitor-ci-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = os.environ.copy()
        for key in ("GITHUB_OUTPUT", "GITHUB_STEP_SUMMARY", "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"):
            self.env.pop(key, None)
        self.git("init", "-q")
        self.git("config", "user.name", "CI tests")
        self.git("config", "user.email", "ci-tests@example.invalid")
        self.base = self.commit("README.md")

    def git(self, *args):
        return subprocess.run(
            ["git", *args], cwd=self.root, env=self.env, check=True,
            capture_output=True, text=True,
        ).stdout.strip()

    def commit(self, path):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text("change\n")
        self.git("add", "--", path)
        self.git("commit", "-qm", "Update " + path)
        return self.git("rev-parse", "HEAD")

    def detect(self, service, base=None, env=None):
        result = subprocess.run(
            ["bash", str(SCRIPT), service, self.base if base is None else base, "HEAD"],
            cwd=self.root, env=self.env if env is None else env, check=True,
            capture_output=True, text=True,
        )
        return dict(line.split("=", 1) for line in result.stdout.splitlines())

    def test_routes_changes_to_the_owning_images(self):
        cases = [
            ("internal/host/server.go", BUILD, OFF),
            ("internal/host/web/app.js", BUILD, OFF),
            ("internal/host/monitor_test.go", TEST, OFF),
            ("cmd/host/main.go", BUILD, OFF),
            ("cmd/webbuild/main.go", BUILD, OFF),
            ("internal/cloud/app/app.go", OFF, BUILD),
            ("internal/cloud/app/app_test.go", OFF, TEST),
            ("cmd/cloud/main.go", OFF, BUILD),
            ("cmd/cloud/main.go", OFF, BUILD),
            ("cloud/dashboard/src/App.tsx", OFF, BUILD),
            ("cloud/db/migrations/001.sql", OFF, BUILD),
            ("cloud/db/migrations.go", OFF, BUILD),
            ("cloud/scripts/build-images.sh", OFF, BUILD),
            ("cloud/db/provision/app_role.sql", OFF, TEST),
            ("cloud/compose.prod.yml", OFF, ALL),
            ("cloud/.env.example", OFF, ALL),
            ("deploy/compose.yml", COMPOSE, OFF),
            ("compose.local.yml", COMPOSE, OFF),
            ("Dockerfile", BUILD, OFF),
            ("cloud/Dockerfile", OFF, BUILD),
            ("go.mod", BUILD, BUILD),
            ("go.sum", BUILD, BUILD),
            (".dockerignore", BUILD, BUILD),
            ("internal/checks/checks.go", BUILD, BUILD),
            ("internal/checks/checks_test.go", TEST, TEST),
            ("internal/platform/logging.go", BUILD, BUILD),
            (".github/scripts/changes.sh", ALL, ALL),
            (".github/workflows/release.yml", ALL, OFF),
            (".github/workflows/cloud.yml", OFF, ALL),
            ("HOST.md", OFF, OFF),
            ("cloud/README.md", OFF, OFF),
            ("docs/guide.md", OFF, OFF),
        ]
        for path, host, cloud in cases:
            with self.subTest(path=path):
                self.git("reset", "--hard", self.base)
                self.commit(path)
                self.assertEqual(self.detect("host"), host)
                self.assertEqual(self.detect("cloud"), cloud)

    def test_missing_baselines_build_every_image(self):
        for base in ("", "0" * 40, "nonexistent-commit"):
            for service in ("host", "cloud"):
                with self.subTest(base=base, service=service):
                    self.assertEqual(self.detect(service, base=base), ALL)

    def test_unchanged_commits_skip_all_work(self):
        for service in ("host", "cloud"):
            self.assertEqual(self.detect(service), OFF)

    def test_pending_changes_survive_later_documentation_commits(self):
        self.commit("internal/cloud/app/app.go")
        self.commit("docs/guide.md")
        self.assertEqual(self.detect("host"), OFF)
        self.assertEqual(self.detect("cloud"), BUILD)

    def test_moves_between_services_rebuild_both(self):
        base = self.commit("internal/host/example.go")
        target = self.root / "internal/cloud/example.go"
        target.parent.mkdir(parents=True, exist_ok=True)
        (self.root / "internal/host/example.go").rename(target)
        self.git("add", "-A")
        self.git("commit", "-qm", "Move the package")
        for service in ("host", "cloud"):
            self.assertEqual(self.detect(service, base=base), BUILD)

    def test_github_outputs_and_summary_are_appended(self):
        self.commit("internal/checks/checks.go")
        output = self.root / "output"
        summary = self.root / "summary"
        output.write_text("existing=value\n")
        env = {**self.env, "GITHUB_OUTPUT": str(output), "GITHUB_STEP_SUMMARY": str(summary)}
        self.assertEqual(self.detect("host", env=env), {})
        self.assertEqual(output.read_text(), "existing=value\nbuild=true\ntest=true\ncompose=false\n")
        self.assertIn("host: build=true", summary.read_text())


if __name__ == "__main__":
    unittest.main()
