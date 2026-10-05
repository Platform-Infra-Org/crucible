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

FROM alpine:3.22
RUN apk add --no-cache git tar ca-certificates \
 && git config --system --add safe.directory '*' \
 && git config --system credential.helper 'store --file=/etc/crucible/git-credentials' \
 && adduser -D -u 10001 crucible && mkdir /data && chown crucible /data
COPY --from=go /out/ /usr/local/bin/
COPY --from=web /src/web/dist /app/web
USER crucible
ENV CRUCIBLE_WEB_DIR=/app/web CRUCIBLE_DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["crucible-api"]
