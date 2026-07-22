.schema_version == 2 and
(.plugins | type == "array" and length == 1) and
(.plugins[0] as $plugin |
  $plugin.id == "codex-agent-identity" and
  ($plugin.version | test("^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\\.[0-9A-Za-z-]+)*)?$")) and
  $plugin.repository == "https://github.com/simplez2/cpa-codex-agent-identity" and
  $plugin.install.type == "direct" and
  ($plugin.install.artifacts | type == "array" and length == 2) and
  ([$plugin.install.artifacts[].goarch] | sort == ["amd64", "arm64"]) and
  all($plugin.install.artifacts[];
    .goos == "linux" and
    (.goarch == "amd64" or .goarch == "arm64") and
    .url == (
      "https://github.com/simplez2/cpa-codex-agent-identity/releases/download/v" +
      $plugin.version + "/codex-agent-identity_" + $plugin.version +
      "_linux_" + .goarch + ".zip"
    ) and
    (.sha256 | test("^[0-9a-f]{64}$")) and
    (.size | type == "number" and . > 0)
  )
)
