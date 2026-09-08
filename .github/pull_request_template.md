## Summary / 变更说明

<!-- One problem per PR. Link the issue; use Fixes # only when all criteria are met. -->

## Evidence / 验证证据

| Check | Actual result / Not run and why |
| --- | --- |
| Reproduction or documentation validation | |
| Relevant tests across both Go modules | |
| Race / vet / portable build, when applicable | |
| Runtime acceptance, when applicable | |

## Risk and rollback / 风险与回滚

<!-- Note settings, proxy, data-format or compatibility impact. A green build is not deployment. -->

## Checklist

- [ ] No secrets, auth files, private emails/Team IDs, hostnames or proxy credentials.
- [ ] No live reset-credit consumption; external calls/deployments require separate authorization.
- [ ] Existing account/Team/proxy/enabled/WS settings and ordinary CPA OAuth stay untouched.
- [ ] Scope is this repository only; no unrelated CPA, Keeper or upstream Store changes.
- [ ] Docs and an unnumbered Unreleased entry updated if user-visible.
- [ ] No routine version bump, moved tag or replaced release asset; release-only changes follow RELEASE.md.
- [ ] Tests reported above were actually run; unverified behavior is stated explicitly.
