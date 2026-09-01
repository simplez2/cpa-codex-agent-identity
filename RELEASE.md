# Release and versioning

This project uses a staged release model so the Plugin Store never points at an
archive that has not been built and verified.

## Version authority

- `VERSION` is the single source of truth for the development version.
- `plugin/codex-agent-identity/plugin.go` keeps a matching `pluginVersion` for
  registration metadata. The release verifier rejects drift.
- `CHANGELOG.md` must contain `## [Unreleased] - <VERSION>` while a version is
  under development.
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

1. **Start the development line.** Update `VERSION`, the matching
   `pluginVersion`, and the `Unreleased` section in `CHANGELOG.md`. Keep
   `registry.json` and `SIDECAR_IMAGE` at the latest published version.
2. **Validate locally.** Run `make verify-release-state`, then `make test`,
   `make race`, `make vet`, and the portable Linux plugin builds for amd64 and
   arm64. Portable builds require Docker and use the manylinux2014 GLIBC 2.17
   baseline; never publish a Linux `.so` built on a modern Ubuntu host directly.
3. **Create the tag.** Commit the source changes and create exactly one tag
   named `v<VERSION>`. Do not update `registry.json` before this tag's assets
   exist.
4. **Let the release workflow publish assets.** The workflow re-checks the tag,
   source version, registry state, artifact naming, tests, and checksums before
   publishing the GitHub Release and GHCR images.
5. **Verify downloads.** Download both Linux plugin archives from the release,
   verify `checksums.txt`, record the exact byte sizes and SHA-256 values, and
   confirm each archive contains `codex-agent-identity.so` at its root.
6. **Publish the registry in a separate commit.** Download the two release
   archives into one directory and let the checked-in helper calculate their
   exact sizes and SHA-256 values:

   ```powershell
   make publish-registry ASSETS_DIR=dist/release-assets
   ```

   The helper refuses missing archives, wrong archive contents, a non-advancing
   version, or a registry update before the target version is in `VERSION`. It
   updates only `registry.json` and `.env.example`. Then run `jq -e -f
   .github/scripts/validate-registry.jq registry.json` and
   `make verify-published-release` before committing.
7. **Begin the next development line.** For example, after publishing `0.3.8`,
   bump `VERSION` and the `Unreleased` heading to `0.3.9`, while leaving
   `registry.json` and `.env.example` at `0.3.8` until the next release.

## Invariants enforced by CI

- The tag must be exactly `v<VERSION>`.
- The source plugin metadata and `VERSION` must match.
- `minimumSidecarVersion` cannot be newer than the source version.
- The registry must contain exactly the two Linux artifacts and its URLs must
  match the registry version.
- Registry SHA-256 values must be lowercase 64-character digests and sizes must
  be positive.
- The registry and sidecar image may not be ahead of the source line.
- Published metadata is not silently replaced by a development build.
- Linux plugin artifacts must remain compatible with GLIBC 2.17 and export the
  complete CPA dynamic-plugin ABI entrypoint set.

## Do not

- Do not point `registry.json` at a tag before its GitHub Release assets exist.
- Do not hand-write or guess a checksum or archive size.
- Do not overwrite an existing release tag or reuse an old archive under a new
  version number.
- Do not change `registry.json` and source version files in one pre-release
  commit unless the release assets have already been verified.
- Do not put PATs, JWTs, Management keys, or generated auth files in release
  notes, test output, artifacts, or the repository.
