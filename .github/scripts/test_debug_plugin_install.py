import importlib.util
import json
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import textwrap
import unittest


spec = importlib.util.spec_from_file_location(
    "debug_install", Path(__file__).with_name("debug-plugin-install.py")
)
debug = importlib.util.module_from_spec(spec)
spec.loader.exec_module(debug)


class InstallCheckTests(unittest.TestCase):
    def test_regular_file_hash_uses_platform_safe_open_flags(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "plugin.so"
            path.write_bytes(b"synthetic-plugin")
            info, digest = debug.hash_regular_file(path, "Plugin file")
            self.assertEqual(info.st_ino, path.stat().st_ino)
            self.assertEqual(digest, hashlib.sha256(b"synthetic-plugin").hexdigest())
        flags = debug.regular_file_open_flags()
        if os.name == "posix":
            self.assertEqual(
                flags & getattr(os, "O_NONBLOCK", 0),
                getattr(os, "O_NONBLOCK", 0),
            )
        else:
            self.assertEqual(flags, os.O_RDONLY)

    @unittest.skipUnless(os.name == "posix", "Linux mapped-file inspection")
    def test_mapped_artifact_is_required_and_hashed(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "plugin.so"
            path.write_bytes(b"synthetic-plugin")
            info = path.stat()
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            row = (f"1000-2000 r-xp 0000 {os.major(info.st_dev):02x}:"
                   f"{os.minor(info.st_dev):02x} {info.st_ino} /plugin.so")
            self.assertEqual(debug.verify_mapped_plugin(path, row, digest), digest)
            for maps, checksum in (
                ("", digest), (row + " (deleted)", digest),
                (row.replace(f" {info.st_ino} ", f" {info.st_ino + 1} "), digest),
                (row, "0" * 64),
            ):
                with self.subTest(maps=maps), self.assertRaises(debug.CheckFailed):
                    debug.verify_mapped_plugin(path, maps, checksum)

    @unittest.skipUnless(os.name == "posix", "POSIX non-blocking FIFO inspection")
    def test_fifo_fails_quickly_without_disclosing_path(self):
        with tempfile.TemporaryDirectory() as directory:
            fifo = Path(directory) / "candidate-secret-name"
            os.mkfifo(fifo)
            program = textwrap.dedent(
                """
                import importlib.util
                from pathlib import Path
                import sys

                spec = importlib.util.spec_from_file_location("debug_install", sys.argv[1])
                debug = importlib.util.module_from_spec(spec)
                spec.loader.exec_module(debug)
                try:
                    debug.verify_mapped_plugin(Path(sys.argv[2]), "", "0" * 64)
                except debug.CheckFailed as error:
                    print(str(error))
                    raise SystemExit(0)
                raise SystemExit(3)
                """
            )
            result = subprocess.run(
                [sys.executable, "-c", program, str(spec.origin), str(fifo)],
                capture_output=True,
                text=True,
                timeout=2,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("Installed plugin is not a regular file", result.stdout)
            self.assertNotIn(str(fifo), result.stdout + result.stderr)

    @unittest.skipUnless(os.name == "posix", "POSIX device inspection")
    def test_device_is_rejected_before_hashing(self):
        with self.assertRaisesRegex(debug.CheckFailed, "not a regular file"):
            debug.verify_mapped_plugin(Path(os.devnull), "", "0" * 64)

    def test_rejects_unsafe_management_urls(self):
        for url in (
            "http://remote.invalid", "https://user:secret@host.invalid",
            "https://host.invalid?key=value", "https://host.invalid/#fragment",
            "file:///tmp/key", "/relative",
        ):
            with self.subTest(url=url), self.assertRaises(debug.CheckFailed):
                debug.normalized_url(url)

    def test_allows_https_and_loopback_prefixes(self):
        for url in ("https://host.invalid/cpa", "http://127.0.0.1:8317",
                    "http://[::1]:8317", "http://localhost:8317/prefix"):
            self.assertEqual(debug.normalized_url(url + "/"), url)

    def test_refuses_redirects(self):
        with self.assertRaises(debug.CheckFailed):
            debug.NoRedirect().redirect_request(None, None, 302, "", {}, "https://other.invalid")

    def test_bridge_read_never_issues_mutation(self):
        checker = debug.InstallCheck("http://127.0.0.1:8317", "synthetic-key")
        calls = []

        def request(path, payload=None, **kwargs):
            calls.append((path, payload))
            return {}, debug.PREFIX + b'{"value":"<team>&identity"}'

        checker.request = request
        self.assertEqual(checker.bridge_read("identities")["value"], "<team>&identity")
        self.assertEqual(calls, [(debug.BRIDGE, {"method": "GET", "path": "identities"})])

    def test_bridge_rejects_unframed_or_nonobject_responses(self):
        for body in (b'{"ok":true}', debug.PREFIX + b"[]", debug.PREFIX + b"invalid"):
            with self.subTest(body=body):
                checker = debug.InstallCheck("http://127.0.0.1:8317", "synthetic-key")
                checker.request = lambda *args, **kwargs: ({}, body)
                with self.assertRaises(debug.CheckFailed):
                    checker.bridge_read("identities")

    def test_bridge_json_framing_preserves_values(self):
        value = {"message": '"<&>\'\n', "plan": "team"}
        checker = debug.InstallCheck("http://127.0.0.1:8317", "synthetic-key")
        checker.request = lambda *args, **kwargs: ({}, debug.PREFIX + json.dumps(value).encode())
        self.assertEqual(checker.bridge_read("diagnostics"), value)


if __name__ == "__main__":
    unittest.main()
