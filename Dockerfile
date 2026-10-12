FROM node@sha256:2fe369e969550cde8e867afc3fe370b260140cab4a23d467074295b42163d553 AS web-builder
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
COPY web/tools/openapi/package.json ./tools/openapi/package.json
RUN corepack enable && pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/apophis-teamseatwatch ./cmd/apophis-teamseatwatch

FROM debian@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates chromium xvfb xauth fonts-liberation tini \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go-builder /out/apophis-teamseatwatch /apophis-teamseatwatch
COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=web-builder /src/web/dist/owner /app/owner
COPY --from=web-builder /src/web/dist/public /app/public
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "xvfb-run", "-a", "/apophis-teamseatwatch"]
