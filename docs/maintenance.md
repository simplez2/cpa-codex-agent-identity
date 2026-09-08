# Repository maintenance / 仓库维护规则

The small-project goal is predictable decisions, not more automation. Use one
issue per observed problem, one focused PR per change and one accepted release
per coherent batch. Never manufacture stars, reviews, users or resolved issues.

## Issue lifecycle

1. New reports get `status:needs-triage` plus a type label from their form.
2. Confirm scope and reproduction. Replace the status with `status:confirmed`,
   `status:needs-validation` or `status:blocked`; record why in a short comment.
3. Link a PR and its acceptance evidence. A fix merged to `main` is not yet a
   published fix; say which release, if any, contains it.
4. Close as completed only when the issue's criteria are met. Close duplicates
   with the canonical link, or explicitly state why work is not planned. Do not
   auto-close old issues or unresolved upstream limitations to improve metrics.

Use existing `bug`, `enhancement`, `question`, `documentation` and `dependencies`
labels for type; `area:*` for scope. Keep one `status:*` label at a time. Apply
`good first issue` only after the task is genuinely small and specified.
`.github/labels.json` is the managed label inventory; unrelated labels are retained.

Preview remote drift with `python .github/scripts/sync-labels.py`; only an
intentional `--apply` changes labels. The helper is scoped to this repository
and never deletes labels or touches issues/releases.

Milestones group acceptance goals, not guessed dates. **Next stable — quality &
compatibility** may collect unnumbered work; assigning it does not allocate the
next patch number or promise that all backlog items ship together.

## Pull requests and dependency queue

| Change | Merge gate |
| --- | --- |
| Docs and community files | Links/forms/label checks, version guard and normal PR CI |
| GitHub Actions | Verify pinned upstream commit, cover every workflow using the action, fresh-base CI; never execute publishing to test it |
| Go libraries | Both modules' relevant tests, race/vet/vulnerability checks; assess credential/storage compatibility |
| Runtime image digest | Verify published image/platforms and run image/health/proxy/TLS smoke in an isolated environment |
| Go minor/toolchain update | Align both modules, Docker builder and portable plugin toolchain; test both architectures; do not merge a Docker-only migration |
| Authentication, routing or storage | Regression tests plus exact-scope isolated runtime acceptance before promotion |

Dependabot opens weekly grouped **version-update** PRs for compatible changes;
security updates are not disabled and there is no automatic merge. Toolchain
and CPA SDK changes stay separate for explicit compatibility review. A stale
green run is not evidence against today's `main`.

Keep source upgrades distinct from deploying a versioned image. Consolidating
bot PRs is allowed only with cross-links and after the replacement actually
contains their changes; do not close a valid update simply to make the list empty.

## Release decision

Follow [RELEASE.md](../RELEASE.md). In particular:

```text
issue/reproduction -> focused PR -> current-base CI -> candidate acceptance
-> assign version/tag -> immutable staged prerelease
-> verify exact assets + isolated acceptance + authorized rollout/verification
-> publish registry -> stable GitHub release -> verified GHCR latest promotion
```

No publishing or production workflow runs automatically on merging ordinary PRs.
Release-note categories provide drafting help, not acceptance evidence. Each
release needs upgrade/rollback instructions, verified scope and known limits.
Historical withdrawals stay visible and their assets are not replaced.

## Periodic maintainer checklist

This is a **manual checklist**, not a scheduler or notification subscription.

- Triage new issues; ask for only the smallest redacted reproduction.
- Check open PRs for stale bases, missing workflow pins and explicit blockers.
- Check latest Release, `registry.json` and `.env.example` agree; do not bump
  `VERSION` just because maintenance occurred.
- Run community checks after editing templates, labels or guides.
- Keep the About description/topics factual and link the onboarding guide.
- Keep existing main-branch rules; add a new required job only after its exact
  check name has successfully run. Never weaken protection to merge a PR.

中文：不靠清空 Issue 列表制造“全修好”，不靠频繁加版本号制造进展。每项工作
都要有范围、证据和结论；阻塞与上游限制保留公开说明，发布与上线分别验收。
