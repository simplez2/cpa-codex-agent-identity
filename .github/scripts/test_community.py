import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("community", Path(__file__).with_name("check-community.py"))
community = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(community)
LABEL_SPEC = importlib.util.spec_from_file_location("labels", Path(__file__).with_name("sync-labels.py"))
labels = importlib.util.module_from_spec(LABEL_SPEC)
LABEL_SPEC.loader.exec_module(labels)


class CommunityChecks(unittest.TestCase):
    def test_repository(self):
        self.assertEqual(community.check(), [])

    def test_recommended_version_drift_and_missing_marker_fail(self):
        pattern = community.RECOMMENDATION_PATTERNS["README.md"]
        self.assertEqual(community.recommended_version_errors("Recommended: [v0.3.18]", pattern, "0.3.18"), [])
        self.assertTrue(community.recommended_version_errors("Recommended: [v0.3.15]", pattern, "0.3.18"))
        self.assertTrue(community.recommended_version_errors("No recommendation", pattern, "0.3.18"))
        self.assertTrue(community.recommended_version_errors("Recommended: [v0.3.18] Recommended: [v0.3.18]", pattern, "0.3.18"))

    def test_duplicate_and_invalid_labels(self):
        label = {"name": "bug", "color": "bad-color", "description": "Example"}
        errors = community.validate_labels([label, label])
        self.assertTrue(any("duplicate" in error for error in errors))
        self.assertTrue(any("color" in error for error in errors))

    def test_unknown_label_and_missing_safety_fail(self):
        form = {"name": "Bug", "description": "Report", "title": "Bug", "labels": ["unknown"], "body": []}
        errors = community.validate_form(form, {"bug"})
        self.assertIn("unknown form label: unknown", errors)
        self.assertIn("form must require a safety acknowledgement", errors)

    def test_duplicate_form_fields_fail(self):
        field = {"type": "input", "id": "versions", "attributes": {"label": "Version"}}
        errors = community.validate_form({"body": [field, field]}, set())
        self.assertTrue(any("duplicate form id" in error for error in errors))

    def test_links_are_offline_and_confined(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "exists.md").write_text("# Existing", encoding="utf-8")
            doc = root / "README.md"
            doc.write_text(
                "[ok](exists.md#heading) [web](https://example.invalid/never-fetch)\n"
                "[missing](absent.md) [escape](../outside.md)\n"
                "```sh\n[example](not-a-real-file.md)\n```\n",
                encoding="utf-8",
            )
            errors = community.broken_local_links(root, doc)
            self.assertEqual(len(errors), 2)
            self.assertTrue(any("absent.md" in error for error in errors))
            self.assertTrue(any("outside.md" in error for error in errors))

    def test_label_sync_is_read_only_by_default(self):
        with patch.object(labels, "gh", side_effect=[{"nameWithOwner": labels.REPOSITORY}, [[]]]) as call:
            with patch("sys.argv", ["sync-labels.py"]), patch("builtins.print"):
                labels.main()
        self.assertEqual(call.call_count, 2)
        self.assertTrue(all("--method" not in args.args for args in call.call_args_list))

    def test_label_sync_refuses_other_repository(self):
        with patch.object(labels, "gh", return_value={"nameWithOwner": "example/other"}) as call:
            with patch("sys.argv", ["sync-labels.py", "--apply"]):
                with self.assertRaises(SystemExit):
                    labels.main()
        self.assertEqual(call.call_count, 1)

    def test_label_sync_never_deletes_unmanaged_labels(self):
        desired = json.loads((labels.ROOT / ".github/labels.json").read_text(encoding="utf-8"))
        existing = desired + [{"name": "unmanaged", "color": "aaaaaa", "description": "Retain"}]
        with patch.object(labels, "gh", side_effect=[{"nameWithOwner": labels.REPOSITORY}, [existing]]) as call:
            with patch("sys.argv", ["sync-labels.py", "--apply"]), patch("builtins.print"):
                labels.main()
        self.assertEqual(call.call_count, 2)

    def test_label_sync_updates_without_renaming(self):
        desired = json.loads((labels.ROOT / ".github/labels.json").read_text(encoding="utf-8"))
        existing = [dict(item) for item in desired]
        existing[0]["description"] = "Old text"
        with patch.object(labels, "gh", side_effect=[{"nameWithOwner": labels.REPOSITORY}, [existing], {}]) as call:
            with patch("sys.argv", ["sync-labels.py", "--apply"]), patch("builtins.print"):
                labels.main()
        self.assertEqual(call.call_count, 3)
        request = call.call_args_list[-1]
        self.assertIn("PATCH", request.args)
        self.assertNotIn("DELETE", request.args)
        self.assertEqual(request.kwargs["payload"]["new_name"], desired[0]["name"])


if __name__ == "__main__":
    unittest.main()
