#!/usr/bin/env python3
"""Read-only acceptance for the installed plugin and its private sidecar route."""

import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request


PLUGIN_ID = "codex-agent-identity"
RESOURCE = "/v0/resource/plugins/" + PLUGIN_ID
BRIDGE = "/v0/management/" + PLUGIN_ID + "/ui-api"
PREFIX = b")]}',\n"
MAX_BODY = 4 * 1024 * 1024


def verify_mapped_plugin(path, maps, expected):
    """Verify the actual file backing the host process's mapped plugin."""
    with path.open("rb") as file:
        before = os.fstat(file.fileno())
        digest = hashlib.file_digest(file, "sha256").hexdigest()
        after = os.fstat(file.fileno())
    if (before.st_ino, before.st_size, before.st_mtime_ns) != (
        after.st_ino, after.st_size, after.st_mtime_ns
    ):
        raise CheckFailed("Installed plugin changed during inspection")
    device = (os.major(before.st_dev), os.minor(before.st_dev))
    mapped = False
    for line in maps.splitlines():
        columns = line.split(None, 5)
        if len(columns) < 6 or columns[5].endswith(" (deleted)"):
            continue
        if int(columns[4]) == before.st_ino and tuple(
            int(part, 16) for part in columns[3].split(":")
        ) == device:
            mapped = True
    if not mapped:
        raise CheckFailed("Installed plugin is not mapped by the CPA process; restart required")
    if digest != expected:
        raise CheckFailed("CPA mapped plugin checksum differs from the verified artifact")
    return digest


def verify_container_plugin(container, registered_path, expected):
    """Run on the Linux Docker host, through its authenticated deployment session."""
    if os.name != "posix" or not container or container.startswith("-"):
        raise CheckFailed("Container inspection requires a local Linux Docker host")

    def inspect():
        try:
            result = subprocess.run(
                ["docker", "inspect", container], check=True, capture_output=True, timeout=15
            )
            value = json.loads(result.stdout)[0]
            if not value["State"]["Running"]:
                raise CheckFailed("CPA container is not running")
            return value["Id"], value["State"]["Pid"], value["State"]["StartedAt"]
        except (OSError, ValueError, KeyError, IndexError, subprocess.SubprocessError):
            raise CheckFailed("Could not inspect the CPA container") from None

    snapshot = inspect()
    proc = Path("/proc") / str(snapshot[1])
    registered = Path(registered_path)
    if not registered_path or ".." in registered.parts:
        raise CheckFailed("CPA reported an invalid plugin path")
    mapped_path = (proc / "root" / str(registered).lstrip("/")) if registered.is_absolute() else (
        proc / "cwd" / registered
    )
    digest = verify_mapped_plugin(mapped_path, (proc / "maps").read_text(), expected)
    if inspect() != snapshot:
        raise CheckFailed("CPA restarted during loaded-plugin inspection")
    return digest


class CheckFailed(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise CheckFailed("HTTP redirect refused")


def normalized_url(raw):
    parsed = urllib.parse.urlsplit(raw)
    if (
        parsed.scheme not in ("http", "https")
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
    ):
        raise CheckFailed("CPA URL must be an absolute origin or path prefix")
    if parsed.scheme == "http":
        try:
            local = ipaddress.ip_address(parsed.hostname).is_loopback
        except ValueError:
            local = parsed.hostname.lower() == "localhost"
        if not local:
            raise CheckFailed("HTTP is allowed only on loopback; use HTTPS otherwise")
    return raw.rstrip("/")


class InstallCheck:
    def __init__(self, base_url, key):
        self.base_url = normalized_url(base_url)
        if not key or "\r" in key or "\n" in key:
            raise CheckFailed("Management key file is empty or invalid")
        self.key = key
        # Local checks must not send the management key through an environment proxy.
        self.client = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), NoRedirect()
        )
        self.results = {}

    def request(self, path, payload=None, authenticated=True, expected=200):
        headers = {}
        if authenticated:
            headers["Authorization"] = "Bearer " + self.key
        if payload is not None:
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(
            self.base_url + path,
            data=None if payload is None else json.dumps(payload).encode(),
            headers=headers,
        )
        try:
            response = self.client.open(req, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        except CheckFailed:
            raise
        except (OSError, ValueError):
            raise CheckFailed("CPA request could not connect") from None
        with response:
            body = response.read(MAX_BODY + 1)
            status = response.code
            response_headers = response.headers
        if status != expected:
            raise CheckFailed(f"{path}: HTTP {status}, expected {expected}")
        if len(body) > MAX_BODY:
            raise CheckFailed(f"{path}: response exceeded size limit")
        if self.key.encode() in body:
            raise CheckFailed(f"{path}: management key exposed in response")
        return response_headers, body

    def bridge_read(self, path):
        _, body = self.request(BRIDGE, {"method": "GET", "path": path})
        if not body.startswith(PREFIX):
            raise CheckFailed(f"{path}: UI bridge framing is missing")
        try:
            value = json.loads(body[len(PREFIX) :])
        except (ValueError, UnicodeError):
            raise CheckFailed(f"{path}: UI bridge did not return valid JSON") from None
        if not isinstance(value, dict):
            raise CheckFailed(f"{path}: UI bridge returned an unexpected JSON shape")
        return value

    def run(self, version, negative_checks=False):
        _, body = self.request("/v0/management/plugins")
        listing = json.loads(body)
        entries = [p for p in listing.get("plugins", []) if p.get("id") == PLUGIN_ID]
        if len(entries) != 1:
            raise CheckFailed("Expected exactly one Agent Identity plugin")
        plugin = entries[0]
        if not all(plugin.get(k) for k in ("registered", "enabled", "effective_enabled")):
            raise CheckFailed("Agent Identity plugin is not effectively enabled")
        if (plugin.get("metadata") or {}).get("version") != version:
            raise CheckFailed("Loaded plugin version does not match the candidate")
        if plugin.get("oauth_provider") != PLUGIN_ID:
            raise CheckFailed("Plugin must not claim CPA's native Codex OAuth provider")
        if not any(m.get("path") == RESOURCE + "/open" for m in plugin.get("menus", [])):
            raise CheckFailed("Plugin menu is missing")
        self.results["registered_version"] = version
        self.results["registered_plugin_path"] = plugin.get("path", "")
        self.results["native_oauth_provider_not_claimed"] = True

        _, wrapper = self.request(RESOURCE + "/open", authenticated=False)
        if b"pluginHostedUIURL" not in wrapper:
            raise CheckFailed("Installed wrapper does not select the embedded UI")
        expected_assets = {
            "/ui?embed=cpamc": (b'id="connection-form"', "text/html"),
            "/app.js": (b"resolvePluginManagementAPIURL", "javascript"),
            "/style.css": (b":root", "text/css"),
            "/theme.js": (b"theme", "javascript"),
        }
        for path, (marker, content_type) in expected_assets.items():
            headers, body = self.request(RESOURCE + path, authenticated=False)
            if marker not in body or content_type not in headers.get("Content-Type", ""):
                raise CheckFailed(f"{path}: embedded resource is invalid")
            if headers.get("Cache-Control") != "no-store":
                raise CheckFailed(f"{path}: embedded resource can be cached")
        self.results["embedded_assets"] = True

        identities = self.bridge_read("identities")
        if not isinstance(identities.get("identities"), list):
            raise CheckFailed("Identity list is unavailable")
        if not identities.get("channel_management_enabled"):
            raise CheckFailed("CPA credential synchronization is disabled")
        diagnostics = self.bridge_read("diagnostics")
        if diagnostics.get("status") != "ok":
            raise CheckFailed("Sidecar diagnostics are not healthy")
        sync = diagnostics.get("cpa_sync") or {}
        if sync.get("state") != "ready" or not sync.get("reachable"):
            raise CheckFailed("Sidecar cannot reach CPA management")
        self.results["identity_count"] = len(identities["identities"])
        self.results["private_sidecar_and_cpa_sync"] = True

        if negative_checks:
            self.request(
                BRIDGE, {"method": "GET", "path": "identities"},
                authenticated=False, expected=401,
            )
            for payload in (
                {"method": "GET", "path": "not-a-registered-route"},
                {"method": "PUT", "path": "identities"},
            ):
                self.request(BRIDGE, payload, expected=400)
            self.results["unauthenticated_and_unregistered_routes_rejected"] = True
        return self.results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cpa-url", required=True)
    parser.add_argument("--key-file", required=True, type=Path)
    parser.add_argument("--expect-version", required=True)
    parser.add_argument("--plugin-file", type=Path)
    parser.add_argument("--expect-sha256")
    parser.add_argument("--cpa-container",
                        help="On Linux Docker host, verify the CPA-mapped library, not only a candidate file")
    parser.add_argument("--negative-checks", action="store_true",
                        help="Use only on an isolated canary, not a shared production login")
    args = parser.parse_args()
    try:
        if bool(args.plugin_file) != bool(args.expect_sha256):
            raise CheckFailed("Provide both --plugin-file and --expect-sha256")
        if args.cpa_container and not args.expect_sha256:
            raise CheckFailed("--cpa-container requires an expected artifact checksum")
        if args.plugin_file:
            digest = hashlib.sha256(args.plugin_file.read_bytes()).hexdigest()
            if digest != args.expect_sha256:
                raise CheckFailed("Plugin file checksum does not match the verified artifact")
        checker = InstallCheck(args.cpa_url, args.key_file.read_text().strip())
        results = checker.run(args.expect_version, args.negative_checks)
        if args.plugin_file:
            results["plugin_file_sha256"] = digest
        if args.cpa_container:
            results["mapped_plugin_sha256"] = verify_container_plugin(
                args.cpa_container, results["registered_plugin_path"], args.expect_sha256
            )
        print(json.dumps({"passed": True, "checks": results}, sort_keys=True))
        return 0
    except CheckFailed as error:
        print(json.dumps({"passed": False, "error": str(error)}))
    except Exception as error:
        # Do not echo response bodies, credentials, proxy URLs, or file contents.
        print(json.dumps({"passed": False, "error_type": type(error).__name__}))
    return 1


if __name__ == "__main__":
    sys.exit(main())
