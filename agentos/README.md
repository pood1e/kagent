# AgentOS runtime artifacts

The `AgentOS images` workflow builds the controller and three Harnesses from `main` after runtime verification. Images carry the source commit tag and the `main` alias; Helm charts and GitHub releases use `0.0.0-main.g<commit>`.

Deployment inputs pin the release commit, image digests and chart checksums. The base images are digest-pinned upstream `main` snapshots; the controller and Harness binaries are compiled from the fork checkout. Release assets include `SHA256SUMS` and the charts' built-in Harness image catalog.
