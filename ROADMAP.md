# Roadmap / 路线图

**Direction: native where possible, explicit where not.** Make installation and
operation predictable before adding more switches. This is a priority list,
not a release-date promise or a claim that every item is implemented.

The current recommended release is **v0.3.18**. Its
[acceptance record](docs/releases/v0.3.18.md) is the evidence boundary; the
[compatibility matrix](docs/compatibility.md) describes remaining limits.

## Now — reliability and trust

- Keep native PAT settings, proxy behavior and HTTP/WS transport covered by
  regression tests; keep JWT/AgentAssertion limitations visible.
- Track and reproduce the management plugin API's JSON escaping discrepancy
  without changing CPA or Keeper. A successful HTTP status alone is insufficient.
- Make quota-only reset-credit responses understandable: show the known count,
  distinguish unknown detail from empty detail, and never invent expiry dates.
- Keep source, staged artifacts, accepted releases and the recommended registry
  distinct. No patch-number churn while an incident remains unverified.

## Next — easier adoption

- Exercise fresh installation and upgrades on both released Linux architectures
  with synthetic fixtures and publish a repeatable acceptance recipe.
- Reduce duplicated setup instructions; make the required sidecar/network/secret
  prerequisites clear before the first Plugin Store click.
- Add redacted product screenshots only from a verified UI with synthetic data,
  not mockups presented as a deployed result.
- Evaluate dependency and Go toolchain upgrades together with the portable
  plugin build, instead of changing only one Docker build stage.

## Later — driven by evidence

- Broader CPA-version compatibility reports and automated compatibility tests.
- Better per-credit UX **if** the upstream credential is allowed to read details.
- Narrower Agent Identity JWT compatibility gaps without taking over native OAuth.

## Not planned

- Forking CPA/Keeper to hide integration problems or promising full support for
  future upstream features without tests.
- Automatically consuming reset credits, changing user proxies or deploying on merge.
- Inflated performance claims, fake stars, testimonials or compatibility badges.

See [open work](https://github.com/simplez2/cpa-codex-agent-identity/issues) and
[milestones](https://github.com/simplez2/cpa-codex-agent-identity/milestones) for
current decisions. An issue closes when its acceptance criteria are met, not
because it is old. Help with a reproducible report or a focused PR is welcome.

中文：先可靠、再扩展。路线图不预分配下个版本号，不承诺上线日期；已解决、
待验证和上游限制会分别标注。欢迎贡献真实复现、安装文档与安全的合成测试。
