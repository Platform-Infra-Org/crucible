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
COPY docs/user/ docs/user/
ARG CRUCIBLE_VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X crucible/internal/docs.Version=${CRUCIBLE_VERSION}" -o /out/crucible-api ./cmd/crucible-api && CGO_ENABLED=0 go build -o /out/crucible ./cmd/crucible

FROM alpine:3.22
# crucible-api runs git with only this config (no system or global config): see gitsync.git.
RUN apk add --no-cache git tar ca-certificates openssh-client \
 && mkdir -p /etc/crucible \
 && git config --file /etc/crucible/gitconfig --add safe.directory '*' \
 && git config --file /etc/crucible/gitconfig credential.helper '!f() { test "$1" = get && git credential-store --file=/etc/crucible/git-credentials get; }; f' \
 && adduser -D -u 10001 crucible && mkdir /data && chown crucible /data
COPY --from=go /out/ /usr/local/bin/
COPY --from=web /src/web/dist /app/web
USER crucible
ENV CRUCIBLE_GIT_CONFIG=/etc/crucible/gitconfig CRUCIBLE_WEB_DIR=/app/web CRUCIBLE_DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["crucible-api"]
