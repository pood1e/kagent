FROM ghcr.io/kagent-dev/kagent/claude-harness@sha256:22fdf7f60d1e183eddb6fd88e512be9aa76ebacdb9e8d6706dc89d5731650f79
USER 0:0
COPY --chmod=0755 toolbox.sh /tmp/install-toolbox.sh
RUN /tmp/install-toolbox.sh && rm /tmp/install-toolbox.sh
COPY --chmod=0755 kagent-claude /usr/local/bin/kagent-claude
USER 65532:65532
ENV HOME=/data
RUN python --version && curl --version && jq --version && ssh -V
