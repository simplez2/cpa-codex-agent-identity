import contextlib
import os
from pathlib import Path
import shutil
import stat
import subprocess
import tempfile
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[2]
SHELL = shutil.which("sh")


def write_shell_script(path, source):
    path.write_text(source, encoding="utf-8", newline="\n")
    path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


@unittest.skipUnless(SHELL, "a POSIX-compatible sh is required")
class BootstrapRuntimeTests(unittest.TestCase):
    @contextlib.contextmanager
    def fixture(self):
        with tempfile.TemporaryDirectory() as directory:
            project = Path(directory) / "project"
            deploy = project / "deploy"
            binaries = project / "test-bin"
            deploy.mkdir(parents=True)
            binaries.mkdir()
            shutil.copy2(ROOT / "deploy" / "bootstrap-runtime.sh", deploy)
            shutil.copy2(ROOT / "deploy" / "docker-compose.production.yml", deploy)
            shutil.copy2(ROOT / ".env.example", project)
            write_shell_script(
                deploy / "init-runtime.sh",
                """#!/bin/sh
set -eu
printf 'init <%s>\\n' "$1" >> "$BOOTSTRAP_TEST_LOG"
runtime_root=$1
mkdir -p "$runtime_root/data-v3" "$runtime_root/secrets" "$runtime_root/cpa-plugins"
[ -e "$runtime_root/secrets/data-encryption-key" ] ||
  printf 'fixture-data-key' > "$runtime_root/secrets/data-encryption-key"
[ -e "$runtime_root/secrets/management-key" ] ||
  printf 'fixture-management-key' > "$runtime_root/secrets/management-key"
""",
            )
            write_shell_script(
                binaries / "openssl",
                """#!/bin/sh
set -eu
printf 'openssl' >> "$BOOTSTRAP_TEST_LOG"
for argument in "$@"; do
  printf ' <%s>' "$argument" >> "$BOOTSTRAP_TEST_LOG"
done
printf '\\n' >> "$BOOTSTRAP_TEST_LOG"
if [ "${1:-}" = rand ] && [ "${2:-}" = -hex ]; then
  printf '0123456789abcdef0123456789abcdef0123456789abcdef\\n'
  exit 0
fi
exit 4
""",
            )
            write_shell_script(
                binaries / "docker",
                """#!/bin/sh
set -eu
printf 'docker' >> "$BOOTSTRAP_TEST_LOG"
for argument in "$@"; do
  printf ' <%s>' "$argument" >> "$BOOTSTRAP_TEST_LOG"
done
printf '\\n' >> "$BOOTSTRAP_TEST_LOG"
if [ "${1:-}" = network ] && [ "${2:-}" = inspect ]; then
  exit 1
fi
exit 0
""",
            )
            yield project

    def run_bootstrap(self, project, *arguments, extra_environment=None):
        environment = os.environ.copy()
        for name in ("SIDECAR_ROOT", "SIDECAR_UI_URL", "AGENT_IDENTITY_NETWORK",
                     "SIDECAR_UID", "SIDECAR_GID"):
            environment.pop(name, None)
        environment["BOOTSTRAP_TEST_LOG"] = "test-actions.log"
        if extra_environment:
            environment.update(extra_environment)
        return subprocess.run(
            [
                SHELL,
                "-c",
                'PATH="$PWD/test-bin:$PATH"; export PATH; '
                'exec sh deploy/bootstrap-runtime.sh "$@"',
                "bootstrap-test",
                *arguments,
            ],
            cwd=project,
            env=environment,
            capture_output=True,
            text=True,
            timeout=10,
            check=False,
        )

    @staticmethod
    def action_log(project):
        path = project / "test-actions.log"
        return path.read_text(encoding="utf-8") if path.exists() else ""

    def test_fresh_start_initializes_then_starts(self):
        with self.fixture() as project:
            result = self.run_bootstrap(project, "--start")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((project / ".env").is_file())
            self.assertTrue((project / "config.yaml").is_file())
            self.assertIn(
                'secret-key: "fixture-management-key"',
                (project / "config.yaml").read_text(encoding="utf-8"),
            )
            actions = self.action_log(project).splitlines()
            self.assertTrue(actions[0].startswith("init <"))
            self.assertEqual(actions[1], "openssl <rand> <-hex> <24>")
            self.assertEqual(
                actions[2], "docker <network> <inspect> <agent-identity>"
            )
            self.assertEqual(
                actions[3], "docker <network> <create> <agent-identity>"
            )
            self.assertEqual(actions[4], "docker <info>")
            self.assertIn("docker <compose>", actions[5])
            self.assertTrue(actions[5].endswith("<up> <-d>"))
            self.assertNotIn("fixture-management-key", result.stdout + result.stderr)

    def test_existing_env_or_config_refuses_start_before_any_side_effect(self):
        for existing_name in (".env", "config.yaml"):
            with self.subTest(existing_name=existing_name), self.fixture() as project:
                existing = project / existing_name
                sentinel = f"{existing_name}-sentinel".encode()
                existing.write_bytes(sentinel)
                secrets = project / "runtime" / "secrets"
                secrets.mkdir(parents=True)
                secret_values = {
                    "data-encryption-key": b"data-sentinel",
                    "management-key": b"management-sentinel",
                    "cpa-api-key": b"api-sentinel",
                }
                for name, value in secret_values.items():
                    (secrets / name).write_bytes(value)

                result = self.run_bootstrap(project, "--start")

                self.assertEqual(result.returncode, 3)
                self.assertEqual(existing.read_bytes(), sentinel)
                for name, value in secret_values.items():
                    self.assertEqual((secrets / name).read_bytes(), value)
                self.assertEqual(self.action_log(project), "")
                self.assertFalse((project / "auths").exists())
                self.assertFalse((project / "logs").exists())
                self.assertNotIn("<up>", result.stdout + result.stderr)

    def test_prepared_default_env_and_existing_secrets_are_preserved(self):
        with self.fixture() as project:
            env_bytes = (
                b"SIDECAR_ROOT=./runtime\r\n"
                b"AGENT_IDENTITY_NETWORK=prepared-network\r\n"
                b"CPA_IMAGE=example.invalid/cpa@sha256:sentinel\r\n"
                b"SIDECAR_IMAGE=example.invalid/sidecar@sha256:sentinel\r\n"
            )
            config_bytes = b"existing: config-sentinel\n"
            (project / ".env").write_bytes(env_bytes)
            (project / "config.yaml").write_bytes(config_bytes)
            secrets = project / "runtime" / "secrets"
            secrets.mkdir(parents=True)
            secret_values = {
                "data-encryption-key": b"existing-data-secret",
                "management-key": b"existing-management-secret",
                "cpa-api-key": b"existing-api-secret",
            }
            for name, value in secret_values.items():
                (secrets / name).write_bytes(value)

            result = self.run_bootstrap(project)

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((project / ".env").read_bytes(), env_bytes)
            self.assertEqual((project / "config.yaml").read_bytes(), config_bytes)
            for name, value in secret_values.items():
                self.assertEqual((secrets / name).read_bytes(), value)
                self.assertNotIn(value.decode(), result.stdout + result.stderr)
            actions = self.action_log(project)
            self.assertIn("init <", actions)
            self.assertIn(
                "docker <network> <create> <prepared-network>", actions
            )
            self.assertNotIn("docker <compose>", actions)
            self.assertNotIn("docker <info>", actions)

    def test_empty_existing_secret_is_not_replaced(self):
        with self.fixture() as project:
            secret = project / "runtime" / "secrets" / "management-key"
            secret.parent.mkdir(parents=True)
            secret.write_bytes(b"")

            result = self.run_bootstrap(project)

            self.assertEqual(result.returncode, 3)
            self.assertEqual(secret.read_bytes(), b"")
            self.assertEqual(self.action_log(project), "")

    def test_custom_sidecar_root_is_rejected_before_initialization(self):
        cases = (
            ("env-file", None),
            ("env-template", None),
            ("process-environment", {"SIDECAR_ROOT": "../outside-secret"}),
        )
        for name, extra_environment in cases:
            with self.subTest(name=name), self.fixture() as project:
                if name == "env-file":
                    (project / ".env").write_text(
                        "SIDECAR_ROOT=../outside-secret\n",
                        encoding="utf-8",
                        newline="\n",
                    )
                elif name == "env-template":
                    (project / ".env.example").write_text(
                        "SIDECAR_ROOT=../outside-secret\n",
                        encoding="utf-8",
                        newline="\n",
                    )
                result = self.run_bootstrap(
                    project, extra_environment=extra_environment
                )
                self.assertEqual(result.returncode, 3)
                self.assertIn("supports only SIDECAR_ROOT=./runtime", result.stderr)
                self.assertNotIn("outside-secret", result.stdout + result.stderr)
                self.assertEqual(self.action_log(project), "")
                self.assertFalse((project / "runtime").exists())
                self.assertFalse((project.parent / "outside-secret").exists())

    def test_non_directory_runtime_path_is_rejected_unchanged(self):
        with self.fixture() as project:
            runtime = project / "runtime"
            runtime.write_bytes(b"runtime-sentinel")
            result = self.run_bootstrap(project)
            self.assertEqual(result.returncode, 3)
            self.assertEqual(runtime.read_bytes(), b"runtime-sentinel")
            self.assertEqual(self.action_log(project), "")

    @unittest.skipUnless(os.name == "posix", "POSIX symlink boundary")
    def test_runtime_symlink_is_rejected_without_touching_target(self):
        with self.fixture() as project:
            outside = project.parent / "outside-runtime"
            outside.mkdir()
            (project / "runtime").symlink_to(outside, target_is_directory=True)
            result = self.run_bootstrap(project)
            self.assertEqual(result.returncode, 3)
            self.assertEqual(list(outside.iterdir()), [])
            self.assertEqual(self.action_log(project), "")

    @unittest.skipUnless(os.name == "posix", "POSIX symlink boundary")
    def test_config_symlinks_are_rejected_before_initialization(self):
        for name in (".env", "config.yaml"):
            with self.subTest(name=name), self.fixture() as project:
                target = project.parent / "outside-config"
                target.write_bytes(b"outside-sentinel")
                (project / name).symlink_to(target)
                result = self.run_bootstrap(project)
                self.assertEqual(result.returncode, 3)
                self.assertEqual(target.read_bytes(), b"outside-sentinel")
                self.assertEqual(self.action_log(project), "")

    def test_process_network_override_matches_compose_environment(self):
        with self.fixture() as project:
            result = self.run_bootstrap(
                project, extra_environment={"AGENT_IDENTITY_NETWORK": "custom-network"}
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(
                "docker <network> <create> <custom-network>", self.action_log(project)
            )

    def test_rejects_ambiguous_sidecar_urls_before_side_effects(self):
        invalid_urls = (
            "https://user:top-secret@host.invalid/path",
            "//host.invalid/path",
            "/agent\nidentity",
            "/agent\\identity",
            "/agent/%2e%2e/secret",
            "/agent/../secret",
            "/agent//identity",
            "https://host.invalid/agent//identity",
            "/agent/\tidentity",
            '/agent/"identity',
        )
        for url in invalid_urls:
            with self.subTest(url=repr(url)), self.fixture() as project:
                result = self.run_bootstrap(
                    project, extra_environment={"SIDECAR_UI_URL": url}
                )
                self.assertEqual(result.returncode, 2)
                self.assertIn("sidecar URL is invalid", result.stderr)
                self.assertNotIn(url, result.stdout + result.stderr)
                self.assertNotIn("top-secret", result.stdout + result.stderr)
                self.assertEqual(self.action_log(project), "")
                self.assertFalse((project / "runtime").exists())

    def test_unknown_option_does_not_echo_its_value(self):
        with self.fixture() as project:
            result = self.run_bootstrap(project, "--token=top-secret")
            self.assertEqual(result.returncode, 2)
            self.assertIn("unknown option", result.stderr)
            self.assertNotIn("top-secret", result.stdout + result.stderr)
            self.assertEqual(self.action_log(project), "")

    def test_legal_direct_dashboard_fallback_is_written_but_not_echoed(self):
        with self.fixture() as project:
            fallback = "/agent-identity/"
            result = self.run_bootstrap(project, "--sidecar-url", fallback)
            self.assertEqual(result.returncode, 0, result.stderr)
            config = (project / "config.yaml").read_text(encoding="utf-8")
            self.assertIn(f'sidecar_url: "{fallback}"', config)
            self.assertNotIn(fallback, result.stdout + result.stderr)
            self.assertNotIn("docker <compose>", self.action_log(project))

    def test_legal_absolute_dashboard_does_not_reject_scheme_separator(self):
        for fallback in (
            "http://127.0.0.1:18787/agent-identity/",
            "https://sidecar.example.test/agent-identity/",
        ):
            with self.subTest(fallback=fallback), self.fixture() as project:
                result = self.run_bootstrap(project, "--sidecar-url", fallback)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(
                    f'sidecar_url: "{fallback}"',
                    (project / "config.yaml").read_text(encoding="utf-8"),
                )

    def test_fixture_clears_inherited_bootstrap_environment(self):
        contaminated = {
            "SIDECAR_ROOT": "../host-runtime",
            "SIDECAR_UI_URL": "/agent//host-value",
            "AGENT_IDENTITY_NETWORK": "invalid host network",
            "SIDECAR_UID": "host-uid",
            "SIDECAR_GID": "host-gid",
        }
        with mock.patch.dict(os.environ, contaminated), self.fixture() as project:
            result = self.run_bootstrap(project)
            self.assertEqual(result.returncode, 0, result.stderr)
            config = (project / "config.yaml").read_text(encoding="utf-8")
            self.assertNotIn("host-value", config)
            self.assertNotIn("host-runtime", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
