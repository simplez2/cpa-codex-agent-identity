#!/usr/bin/env python3
"""Validate local community metadata without GitHub access or runtime requests."""

import json
import re
import sys
from pathlib import Path
from urllib.parse import unquote, urlsplit

import yaml


ROOT = Path(__file__).resolve().parents[2]


def validate_labels(labels):
    errors = []
    names = set()
    for label in labels:
        name = label.get("name", "")
        if not name or name in names:
            errors.append(f"invalid or duplicate label: {name!r}")
        names.add(name)
        if not re.fullmatch(r"[0-9a-f]{6}", label.get("color", "")):
            errors.append(f"invalid label color: {name}")
        if not 1 <= len(label.get("description", "")) <= 100:
            errors.append(f"invalid label description: {name}")
    return errors


def validate_form(form, labels):
    errors = []
    for key in ("name", "description", "title", "body"):
        if not form.get(key):
            errors.append(f"missing form field: {key}")
    for label in form.get("labels", []):
        if label not in labels:
            errors.append(f"unknown form label: {label}")
    ids = set()
    safety_required = False
    for item in form.get("body", []):
        kind = item.get("type")
        if kind == "markdown":
            continue
        if kind not in {"input", "textarea", "dropdown", "checkboxes"}:
            errors.append(f"unsupported form item: {kind}")
        field_id = item.get("id", "")
        if not re.fullmatch(r"[a-z][a-z0-9_-]*", field_id) or field_id in ids:
            errors.append(f"invalid or duplicate form id: {field_id!r}")
        ids.add(field_id)
        attributes = item.get("attributes", {})
        if not attributes.get("label"):
            errors.append(f"missing label for form field: {field_id}")
        if kind in {"checkboxes", "dropdown"} and not attributes.get("options"):
            errors.append(f"missing options for form field: {field_id}")
        if field_id == "safety" and kind == "checkboxes":
            safety_required = any(
                option.get("required") is True
                for option in attributes.get("options", [])
            )
    if not safety_required:
        errors.append("form must require a safety acknowledgement")
    return errors


def broken_local_links(root, document):
    # Check file destinations, not remote URLs or GitHub-rendered heading slugs.
    # Ignore fenced examples so placeholder commands do not become link targets.
    text = re.sub(r"(?ms)^(```|~~~).*?^\1[^\n]*$", "", document.read_text(encoding="utf-8"))
    errors = []
    for match in re.finditer(r"\[[^\]\n]+\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)", text):
        raw = match.group(1).strip("<>")
        url = urlsplit(raw)
        if url.scheme or url.netloc or not url.path:
            continue
        path = (document.parent / unquote(url.path)).resolve()
        if not path.is_relative_to(root.resolve()) or not path.exists():
            errors.append(f"{document.relative_to(root)}: missing local link {raw}")
    return errors


def check(root=ROOT):
    errors = []
    labels = json.loads((root / ".github/labels.json").read_text(encoding="utf-8"))
    errors.extend(validate_labels(labels))
    names = {label["name"] for label in labels}
    forms = root / ".github/ISSUE_TEMPLATE"
    for path in sorted(forms.glob("*.yml")):
        data = yaml.safe_load(path.read_text(encoding="utf-8"))
        if path.name == "config.yml":
            if data.get("blank_issues_enabled") is not False:
                errors.append("issue chooser must direct users through safe forms")
            for link in data.get("contact_links", []):
                if not link.get("url", "").startswith("https://github.com/simplez2/cpa-codex-agent-identity/"):
                    errors.append("issue contact link must stay in this repository")
        else:
            errors.extend(f"{path.name}: {error}" for error in validate_form(data, names))
    release = yaml.safe_load((root / ".github/release.yml").read_text(encoding="utf-8"))
    categories = release["changelog"]["categories"]
    if categories[-1].get("labels") != ["*"]:
        errors.append("release notes must end with a catch-all category")
    used_labels = release["changelog"].get("exclude", {}).get("labels", [])[:]
    for category in categories:
        used_labels.extend(category["labels"])
    for name in used_labels:
        if name != "*" and name not in names:
            errors.append(f"unknown release-note label: {name}")
    updates = yaml.safe_load((root / ".github/dependabot.yml").read_text(encoding="utf-8"))["updates"]
    for update in updates:
        if update.get("schedule", {}).get("interval") != "weekly":
            errors.append("dependency version updates must stay weekly")
        for name in update.get("labels", []):
            if name not in names:
                errors.append(f"unknown Dependabot label: {name}")
        if update.get("ignore"):
            errors.append("do not silently ignore dependency/security upgrades")
    documents = list(root.glob("*.md")) + list((root / "docs").rglob("*.md"))
    documents.append(root / "management-overlay/README.md")
    for document in documents:
        errors.extend(broken_local_links(root, document))
    return errors


if __name__ == "__main__":
    failures = check()
    for failure in failures:
        print(f"community: {failure}", file=sys.stderr)
    if failures:
        sys.exit(1)
    print("community: labels, forms, release categories, dependency policy and local links are valid")
