import importlib.util
import json
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    "debug_install", Path(__file__).with_name("debug-plugin-install.py")
)
debug = importlib.util.module_from_spec(spec)
spec.loader.exec_module(debug)


class InstallCheckTests(unittest.TestCase):
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
