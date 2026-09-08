# Release-note and acceptance template

Copy this into the candidate's release record only after a version is assigned.
Replace every placeholder; never manufacture a checksum, result or release date.
Do not paste private deployment identifiers or credentials.

## Summary / 中文摘要

- Version / status: candidate, accepted or withdrawn.
- User-visible problem solved and who benefits.
- Migration or compatibility changes; scope explicitly excluded.

## Install or upgrade

- Link to the existing Release assets and verified direct registry.
- Exact compatible CPA runtime tested; plugin/sidecar version pair.
- Checksum instructions, sidecar prerequisites and same-origin setup.
- Preserve current accounts, Teams, proxies, disabled state and native fields.
- Retain previous accepted assets and independent data/key backups for rollback.

## Artifact identity

| Item | Verified value |
| --- | --- |
| Source tag and commit | Fill from Git |
| Build workflow/run | Link after successful completion |
| Multi-architecture image digest | Read from the published manifest |
| amd64/arm64 plugin ZIP size and SHA-256 | Calculate from actual downloads |
| Sidecar binary checksums | Match the Release checksums file |

## Acceptance

| Check | Result and evidence | Environment / limitation |
| --- | --- | --- |
| Exact artifacts match checksums and ABI/architecture | Not run | |
| Fresh native PAT import | Not run | |
| Fields/status survive refresh and restart | Not run | |
| Complete HTTP/SSE and native WS stream | Not run | |
| Ordinary read-only quota | Not run | |
| Working proxy and refused proxy fail-closed test | Not run | |
| JWT-specific scope, if changed | Not run | |
| Authorized rollout and independent smoke | Not run | |

**Never consume reset credits as a validation step.** Do not represent a status
code alone as valid JSON, a complete stream or successful persistence.

## Known limitations and rollback

List remaining issues, links, upstream permission limits and untested paths.
State which previous **existing** release is the rollback target. If acceptance
fails, keep the candidate prerelease and leave the accepted recommendation alone.

## Publication checklist

- [ ] Artifact downloads/metadata independently verified.
- [ ] Exact-asset isolated acceptance recorded, with failures visible.
- [ ] Authorized rollout/verification complete; private backups retained.
- [ ] Registry/image example updated in a separate reviewed PR.
- [ ] GitHub stable/Latest promoted without rebuilding or replacing old assets.
- [ ] GHCR latest promoted using the verified existing manifest digest.
- [ ] Issue/PR status distinguishes merged, published and deployed work.
