# Contributing / 参与贡献

Thanks for helping make Codex credential management easier to operate. Small,
well-tested fixes and clear documentation are welcome. English and 简体中文
issues and pull requests are both welcome.

## Start with the right place

- Installation or configuration: [support guide](SUPPORT.md).
- Reproducible bug: [bug report](https://github.com/simplez2/cpa-codex-agent-identity/issues/new?template=bug_report.yml).
- Improvement: [feature request](https://github.com/simplez2/cpa-codex-agent-identity/issues/new?template=feature_request.yml).
- Security or leaked credentials: **do not post publicly**; use [private reporting](SECURITY.md).
- Unsure what to work on? Check the [roadmap](ROADMAP.md) and
  [help wanted](https://github.com/simplez2/cpa-codex-agent-identity/issues?q=is%3Aissue%20is%3Aopen%20label%3A%22help%20wanted%22).

Please discuss changes to authentication, routing, storage formats or the release
process before implementing a large patch. Changes in this repository do not
authorize modifications to CPA, Keeper or the upstream plugin store.

## Development workflow

1. Branch from current `main`; keep one problem per PR. Maintainer work uses
   `codex/<short-topic>`; external contributors may use their own branch names.
2. Describe the observed failure and the intended behavior. Add a regression
   test before or with a runtime fix. Use synthetic credentials and local mocks.
3. Run the checks below and record their actual results in the PR. State what
   was **not** tested; a green unit suite is not production acceptance.
4. Keep `VERSION`, published tags, `registry.json` and image recommendations
   unchanged for routine fixes, docs and dependency PRs. Add user-visible changes
   under an unnumbered `Unreleased` heading; assign a version only at release time.
5. Submit a focused PR with a conventional title, for example
   `fix(quota): preserve JSON responses` or `docs: clarify first installation`.

### Checks

Use the Go toolchain declared in both `go.mod` files (currently Go 1.26.6).
Tests run across **two modules**; testing only the repository root misses the
CPA plugin. CGO and a C compiler are needed for race/plugin checks.

```sh
make test
make race
make vet
make verify-release-state
python -m pip install -r .github/requirements-maintenance.txt
python .github/scripts/check-community.py
python -m unittest discover -s .github/scripts -p 'test_community.py'
git diff --check
```

For Linux binary changes, also run the portable amd64/arm64 build and ABI/GLIBC
checks in [RELEASE.md](RELEASE.md). Docker is required; do not substitute a
modern host-built `.so` for the portable release artifact.

For docs-only changes, the community checks, version guard and `git diff --check`
are the local minimum; normal PR CI still runs. Changes to `management-overlay/`
must pass its [pinned build and verification](management-overlay/README.md).

### Safe evidence

- Never attach `.env`, auth files, encrypted stores, real tokens, Management keys,
  proxy credentials, cookies, complete request bodies or private hostnames.
- Keep account emails and Team identifiers out of public reports; use synthetic
  labels such as `account-A / team-1` while preserving relationships.
- Do not use reset credits for tests or acceptance. Mock redemption routes
  locally; live health checks and diagnostics must stay read-only.
- Preserve current account, Team, proxy and enable/disable settings when testing
  an explicitly authorized deployment. There is no automatic production deploy
  on merge.

## Review and merge

`main` requires PRs, passing required checks, a current base, resolved review
threads and linear history. The repository uses **rebase merges**. Set a GitHub
`noreply` commit email before committing: CI rejects public personal commit
emails. Do not bypass checks, resolve unaddressed review threads or force-push
`main` to tidy the PR list.

Dependency PRs are reviewed, not automatically merged. Build-action updates,
runtime-image updates, Go libraries and Go toolchain migrations have different
acceptance needs; see the [maintenance policy](docs/maintenance.md).

## 中文速览

- 先复现，再修复；一个 PR 解决一类问题，写清测试结果与未验证范围。
- 普通修复、文档和依赖更新不抢跑版本号，不覆盖旧 tag、资产或 checksum。
- 只改本项目，不顺手修改 CPA、Keeper 或官方插件商店。
- 不上传真实凭证和用户数据，不拿重置券做测试；安全问题走私密报告。
- 欢迎改进安装文档、补充合成数据测试，以及提交可复现的兼容性报告。
