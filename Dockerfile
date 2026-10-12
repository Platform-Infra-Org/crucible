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

# The image plan and apply pods run (awsLabs.terraformImage / CRUCIBLE_TERRAFORM_IMAGE): upstream terraform plus a
# filesystem mirror of hashicorp/aws 6.67.0, the version internal/content/awstf.go pins, so `terraform init` installs it
# without the registry. `providers mirror` checks the download against HashiCorp's signature and the committed lock
# file. Other providers still come from the registry. Build: docker build --target terraform -t crucible-terraform .
FROM hashicorp/terraform:1.16.5@sha256:c7926feace05d0f7e73542842bf3945924e955a1f782cf000ccbb8d18fa42d77 AS terraform
ARG TARGETARCH=amd64
COPY deploy/aws/labs/.terraform.lock.hcl /tmp/mirror/
RUN printf 'terraform {\n  required_providers {\n    aws = { source = "hashicorp/aws", version = "6.67.0" }\n  }\n}\n' > /tmp/mirror/main.tf \
 && terraform -chdir=/tmp/mirror providers mirror -platform=linux_${TARGETARCH} /usr/share/terraform/providers \
 && rm -rf /tmp/mirror \
 && printf 'provider_installation {\n  filesystem_mirror {\n    path    = "/usr/share/terraform/providers"\n    include = ["registry.terraform.io/hashicorp/aws"]\n  }\n  direct {\n    exclude = ["registry.terraform.io/hashicorp/aws"]\n  }\n}\n' > /etc/terraformrc
ENV TF_CLI_CONFIG_FILE=/etc/terraformrc

FROM alpine:3.22
# crucible-api runs git with only this config (no system or global config): see gitsync.git.
RUN apk add --no-cache git tar ca-certificates \
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
