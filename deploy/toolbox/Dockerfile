# The cluster lab toolbox: what a trainee's terminals and a lab's scripts use against their cluster. The server pins it
# by digest (CRUCIBLE_TOOLBOX_IMAGE); bump these versions deliberately.
# Cross-compiled on the builder's platform (no emulated Go compile in a multi-arch build).
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS ready
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
COPY docs/user docs/user
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /crucible ./cmd/crucible

FROM alpine:3.22
ARG TARGETARCH
ARG KUBECTL=v1.34.1
ARG HELM=v3.19.0
ARG K9S=v0.50.15
RUN apk add --no-cache bash curl jq ca-certificates \
 && curl -fsSL "https://dl.k8s.io/release/${KUBECTL}/bin/linux/${TARGETARCH}/kubectl" -o /usr/local/bin/kubectl \
 && curl -fsSL "https://get.helm.sh/helm-${HELM}-linux-${TARGETARCH}.tar.gz" | tar -xz -C /tmp \
 && mv "/tmp/linux-${TARGETARCH}/helm" /usr/local/bin/helm \
 && curl -fsSL "https://github.com/derailed/k9s/releases/download/${K9S}/k9s_Linux_${TARGETARCH}.tar.gz" | tar -xz -C /usr/local/bin k9s \
 && chmod +x /usr/local/bin/kubectl /usr/local/bin/helm /usr/local/bin/k9s \
 && rm -rf /tmp/linux-*
COPY --from=ready /crucible /usr/local/bin/crucible
# Authored lab content may still call the old crucible-ready command; it is `crucible ready` now.
RUN printf '#!/bin/sh\nexec crucible ready "$@"\n' > /usr/local/bin/crucible-ready && chmod +x /usr/local/bin/crucible-ready
# Login shells (the agent's terminals) get PS1 reset by /etc/profile, so set it again after it.
RUN printf '%s\n' "PS1='\\[\\e[33m\\]\$CRUCIBLE_CONTEXT\\[\\e[0m\\]:\$LAB_NAMESPACE \\w \\\$ '" > /etc/profile.d/crucible-prompt.sh
ENV HOME=/tmp PS1='\[\e[33m\]$CRUCIBLE_CONTEXT\[\e[0m\]:$LAB_NAMESPACE \w \$ '
CMD ["sleep", "infinity"]
