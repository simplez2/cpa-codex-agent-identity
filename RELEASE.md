# Release and versioning

This project uses a staged release model so the Plugin Store never points at an
archive that has not been built and verified.

## Reader's version map

| State | Meaning | Recommended for users? |
| --- | --- | --- |
| `main` / unnumbered `Unreleased` | Merged work, not a new released binary | No automatic upgrade |
| Tagged prerelease | Immutable candidate assets exist; acceptance pending | No |
| Accepted stable release | Exact assets validated; registry and image recommendation match | Yes, within its documented scope |
| Withdrawn / withheld | Historical assets retained with an explicit warning | No |

Currently **v0.3.18** is recommended, **v0.3.17** is withdrawn and **v0.3.16**
remains unaccepted. See [compatibility](docs/compatibility.md) and the
[v0.3.18 acceptance record](docs/releases/v0.3.18.md). Do not hide withdrawal
history or describe a merged fix as deployed.

## Version authority

- `VERSION` is the single source of truth for the development version.
- `plugin/codex-agent-identity/plugin.go` keeps a matching `pluginVersion` for
  registration metadata. The release verifier rejects drift.
- `CHANGELOG.md` must contain `## [Unreleased] - <VERSION>` while a version is
  under development. Once `registry.json` catches up to `VERSION`, it must contain
  a dated `## [<VERSION>] - YYYY-MM-DD` release section instead.
- During a release hold, an unnumbered `## [Unreleased]` section is allowed for
  ordinary CI while `VERSION` stays at its historical source baseline. This is
  not permission to re-release that tag: tag verification still requires an
  explicitly numbered development section when the registry lags the source.
- `registry.json` describes the latest **published** plugin version. It may lag
  `VERSION`, but it must never be ahead of it.
- `.env.example` pins the latest published sidecar image and therefore normally
  follows `registry.json`, not an unreleased source version.

Run the local guard before every commit that changes versioned code:

```powershell
make verify-release-state
```

After the release assets and registry entries have been published, use the
stricter check:

```powershell
make verify-published-release
```

## Normal release sequence

1. **Fix and reproduce first.** Keep changes unnumbered during investigation.
   After native runtime acceptance passes, assign `VERSION`, the matching
   `pluginVersion`, and a numbered `Unreleased` section. Keep `registry.json`
   and `SIDECAR_IMAGE` at the accepted published or rollback version.
2. **Validate locally.** Run `make verify-release-state`, then `make test`,
   `make race`, `make vet`, and the portable Linux plugin builds for amd64 and
   arm64. Portable builds require Docker and use the manylinux2014 GLIBC 2.17
   baseline; never publish a Linux `.so` built on a modern Ubuntu host directly.
3. **Create the tag.** Commit the source changes and create exactly one tag
   named `v<VERSION>`. Do not update `registry.json` before this tag's assets
   exist.
4. **Let the release workflow stage assets as a prerelease.** The workflow re-checks the tag,
   source version, registry state, artifact naming, tests, and checksums before
   publishing the GitHub prerelease and versioned GHCR images. It does not move
   GitHub Latest or GHCR latest before the exact artifacts pass acceptance.
5. **Verify downloads.** Download both Linux plugin archives from the release,
   verify `checksums.txt`, record the exact byte sizes and SHA-256 values, and
   confirm each archive contains `codex-agent-identity.so` at its root.
6. **Accept the exact assets before advertising them.** Validate the downloaded
   plugin and versioned image together on disposable stock CPA. Record checksums,
   runtime versions, settings persistence, complete streams, quota and proxy
   checks. Perform an explicitly authorized rollout with a rollback backup and
   verify it; do not silently turn CI into production deployment. No live
   reset-credit consumption is part of acceptance. If checks fail, retain the
   prerelease warning and the previous accepted registry/image recommendation.
7. **Publish the registry in a separate commit.** Download the two release
   archives into one directory and let the checked-in helper calculate their
   exact sizes and SHA-256 values:

   ```powershell
   make publish-registry ASSETS_DIR=dist/release-assets
   ```

   The helper refuses missing archives, wrong archive contents, a non-advancing
   version, or a registry update before the target version is in `VERSION`. It
   updates only `registry.json` and `.env.example`. In the same post-release
   commit, rename `## [Unreleased] - <VERSION>` to a dated
   `## [<VERSION>] - YYYY-MM-DD` section. Then run `jq -e -f
   .github/scripts/validate-registry.jq registry.json` and
   `make verify-published-release` before committing.
8. **Promote only after acceptance and deployment.** Once the acceptance record
   and registry commit are on main,
   mark that existing prerelease stable/Latest (do not recreate its assets), then
   run **Promote verified container** with its verified manifest digest.
9. **Do not pre-allocate the next release.** Open an unnumbered `Unreleased`
   section for follow-up fixes. Advance the version only after the reported
   runtime workflow is reproduced and the candidate passes acceptance.

## Withdrawals and rollback

- Preserve existing tags, checksums and assets. Mark a failed release as a
  prerelease/withdrawn and remove it from `latest` recommendation.
- Restore the registry and image example to the exact existing rollback assets;
  never publish a development build under a previously released version.
- Tag builds publish only the versioned image, not `latest`. After registry and
  runtime acceptance (or an accepted rollback), run **Promote verified container**
  with the registry version and its verified multi-architecture SHA-256 digest.
  It rejects prereleases, registry mismatches and changed digests, and moves only
  `latest` without rebuilding or replacing any historical version tag.
- Back up current deployment files, preserve current account/Team/proxy/status
  settings, and validate on a disposable stock-CPA instance before cutover.
- Acceptance must cover file-backed controls, save/restart persistence, complete
  model streams, native quota calls, a usable proxy and a broken proxy. A sidecar
  health response or `Provider: codex` alone is not native compatibility evidence.

## Invariants enforced by CI

- The tag must be exactly `v<VERSION>`.
- The source plugin metadata and `VERSION` must match.
- `minimumSidecarVersion` cannot be newer than the source version.
- The registry must contain exactly the two Linux artifacts and its URLs must
  match the registry version.
- Registry SHA-256 values must be lowercase 64-character digests and sizes must
  be positive.
- The registry and sidecar image may not be ahead of the source line.
- Community checks keep the README, roadmap and compatibility/release guide's
  recommended version aligned with the published registry, not an unreleased source.
- Published metadata is not silently replaced by a development build.
- Linux plugin artifacts must remain compatible with GLIBC 2.17 and export the
  complete CPA dynamic-plugin ABI entrypoint set.

Runtime acceptance and approval are **maintainer gates**, not inferred by the
version checker. CI cannot prove a live workflow was tested merely because a
release document exists.

## Release notes and PR hygiene

- Draft notes with [the release template](docs/releases/TEMPLATE.md). Include a
  short English/Chinese summary, installation path, upgrade/rollback guidance,
  exact verification scope and remaining limitations.
- `.github/release.yml` categorizes GitHub-generated note suggestions from PR
  labels. It does not trigger a release, choose a version or replace the curated
  changelog/acceptance record. Read every generated entry before publication.
- Prefer one coherent accepted batch over a tag for each attempted fix. Normal
  docs, dependency and maintenance PRs leave `VERSION`, published assets and
  `registry.json` unchanged.
- Use issues for unresolved regressions/upstream limitations and an unnumbered
  milestone for candidates. Closing a PR does not prove its underlying issue is
  fixed. See [repository maintenance](docs/maintenance.md).

## Do not

- Do not point `registry.json` at a tag before its GitHub Release assets exist.
- Do not hand-write or guess a checksum or archive size.
- Do not overwrite an existing release tag or reuse an old archive under a new
  version number.
- Do not change `registry.json` and source version files in one pre-release
  commit unless the release assets have already been verified.
- Do not put PATs, JWTs, Management keys, or generated auth files in release
  notes, test output, artifacts, or the repository.
