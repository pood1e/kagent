ARG HARNESS_IMAGE
FROM ${HARNESS_IMAGE}
USER 0:0
COPY --chmod=0755 toolbox.sh /tmp/install-toolbox.sh
RUN /tmp/install-toolbox.sh && rm /tmp/install-toolbox.sh
ARG TARGETARCH
COPY --chmod=0755 linux/${TARGETARCH}/rootfs/ /
ENV HOME=/data
RUN python --version && curl --version && jq --version && ssh -V
