#!/usr/bin/env python3
"""Plan label updates by default; --apply updates only this project's labels."""

import argparse
import json
import subprocess
from pathlib import Path
from urllib.parse import quote


REPOSITORY = "simplez2/cpa-codex-agent-identity"
ROOT = Path(__file__).resolve().parents[2]


def gh(*args, payload=None):
    result = subprocess.run(
        ["gh", *args], cwd=ROOT, check=True, capture_output=True, text=True,
        encoding="utf-8", input=None if payload is None else json.dumps(payload),
    )
    return json.loads(result.stdout) if result.stdout.strip() else None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true", help="apply reviewed additions/updates; never delete labels")
    args = parser.parse_args()
    actual = gh("repo", "view", "--json", "nameWithOwner")["nameWithOwner"]
    if actual != REPOSITORY:
        raise SystemExit("Refusing to mutate labels outside " + REPOSITORY)
    desired = json.loads((ROOT / ".github/labels.json").read_text(encoding="utf-8"))
    pages = gh("api", f"repos/{REPOSITORY}/labels?per_page=100", "--paginate", "--slurp")
    existing = {label["name"]: label for page in pages for label in page}
    changes = 0
    for label in desired:
        old = existing.get(label["name"])
        if old and all(old.get(key) == label[key] for key in ("color", "description")):
            continue
        action = "update" if old else "create"
        print(f"{action}: {label['name']}")
        changes += 1
        if args.apply:
            endpoint = f"repos/{REPOSITORY}/labels"
            payload = label
            if old:
                endpoint += "/" + quote(label["name"], safe="")
                payload = {"new_name": label["name"], "color": label["color"], "description": label["description"]}
            gh("api", endpoint, "--method", "PATCH" if old else "POST", "--input", "-", payload=payload)
    print(f"{'Applied' if args.apply else 'Planned'} {changes} label changes; no labels deleted.")


if __name__ == "__main__":
    main()
