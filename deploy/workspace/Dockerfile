# The AWS lab workspace: the AWS CLI plus what OS-level labs need (ssh, the Session Manager plugin, lab-ssh,
# lab-console). Published by digest (runbook: the workspace image); main's workspace_image points at it.
FROM amazon/aws-cli:2.27.0@sha256:e3e329e1d2894b7b4bbb0aacacd0a155262159b2e7a3b4275eb1f24046d3e06c
ARG TARGETARCH
ARG SSM_PLUGIN=1.2.896.0
ARG SSM_PLUGIN_SHA256_AMD64=89a4a07fcb92169bdd2f07c2531acd11b84857338172e7638964aaa08fd5f0e3
ARG SSM_PLUGIN_SHA256_ARM64=edb766755393202e1193f8d2578c356a5ce409182e618b302da4e9c253d05895
RUN yum install -y openssh-clients && yum clean all
RUN set -eu; case "$TARGETARCH" in amd64) d=linux_64bit; s=$SSM_PLUGIN_SHA256_AMD64 ;; arm64) d=linux_arm64; s=$SSM_PLUGIN_SHA256_ARM64 ;; *) exit 1 ;; esac; \
    curl -fsSL -o /tmp/ssm.rpm "https://s3.amazonaws.com/session-manager-downloads/plugin/$SSM_PLUGIN/$d/session-manager-plugin.rpm"; \
    echo "$s  /tmp/ssm.rpm" | sha256sum -c -; yum install -y /tmp/ssm.rpm; rm /tmp/ssm.rpm; yum clean all
COPY serial-console-endpoints /etc/crucible/serial-console-endpoints
COPY --chmod=755 lab-ssh lab-console /usr/local/bin/
