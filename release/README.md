# Fork release

The `Fork release` workflow (`.github/workflows/fork-release.yaml`) verifies the fork's runtime extensions and publishes the controller and the standard, Claude and Codex Harness images.

- Pull requests run the race tests in four shards (with PostgreSQL, envtest and the pinned agentgateway binary) and lint. `verify` aggregates them.
- `dev` accepts pull requests whose `verify` passed on a branch that is up to date with `dev`; each merge builds and publishes directly from that verified tree.
- `main` runs the tests and lint before publishing.

Each image is built on a native amd64 and arm64 runner: the binary is compiled from the fork checkout and layered onto a digest-pinned upstream snapshot (`images/`). Each component and architecture keeps its own Go module cache and continues from its previous build cache. Images carry the commit tag and the branch alias in a multi-arch index. Helm charts and GitHub releases use `0.0.0-<branch>.g<commit>`; release assets include `SHA256SUMS` and the charts' built-in Harness image catalog.

Deployment inputs pin the release commit, image digests and chart checksums.
