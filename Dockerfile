FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -o /out/crucible-api ./cmd/crucible-api && CGO_ENABLED=0 go build -o /out/crucible ./cmd/crucible

FROM alpine:3.22 AS infracost
ARG TARGETARCH=amd64
ARG INFRACOST_VERSION=v0.10.41
# sha256 of the v0.10.41 release assets, matched against the release's .sha256 files and the downloaded bytes
ARG INFRACOST_SHA256_AMD64=9e37a53ba65dd4ad1e25d6023e8ef511c47c8d80d2b064e8bc2598d983ef79e5
ARG INFRACOST_SHA256_ARM64=4f643c8f4894dc924f7e8dd861b28e17a9d8cc773b3f2d15c000409252749488
RUN apk add --no-cache curl \
 && if [ "$TARGETARCH" = arm64 ]; then sum="$INFRACOST_SHA256_ARM64"; else sum="$INFRACOST_SHA256_AMD64"; fi \
 && curl -fsSL "https://github.com/infracost/infracost/releases/download/${INFRACOST_VERSION}/infracost-linux-${TARGETARCH}.tar.gz" -o /tmp/i.tgz \
 && echo "$sum  /tmp/i.tgz" | sha256sum -c - \
 && tar -xzf /tmp/i.tgz -C /tmp && install -m 0755 "/tmp/infracost-linux-${TARGETARCH}" /usr/local/bin/infracost

FROM alpine:3.22
RUN apk add --no-cache git tar ca-certificates \
 && git config --system --add safe.directory '*' \
 && git config --system credential.helper '!f() { test "$1" = get && git credential-store --file=/etc/crucible/git-credentials get; }; f' \
 && adduser -D -u 10001 crucible && mkdir /data && chown crucible /data
COPY --from=go /out/ /usr/local/bin/
COPY --from=infracost /usr/local/bin/infracost /usr/local/bin/
COPY --from=web /src/web/dist /app/web
USER crucible
ENV CRUCIBLE_WEB_DIR=/app/web CRUCIBLE_DATA_DIR=/data INFRACOST_SKIP_UPDATE_CHECK=true
EXPOSE 8080
ENTRYPOINT ["crucible-api"]
