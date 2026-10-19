# syntax=docker/dockerfile:1.11.0

FROM node:24.20.0-trixie-slim AS dashboard-builder
WORKDIR /src/dashboard
RUN corepack enable
COPY cloud/dashboard/package.json cloud/dashboard/pnpm-lock.yaml cloud/dashboard/pnpm-workspace.yaml ./
RUN --mount=type=cache,id=monitor-cloud-pnpm,target=/pnpm/store \
    pnpm config set store-dir /pnpm/store && \
    pnpm install --frozen-lockfile --ignore-scripts
COPY cloud/dashboard/index.html cloud/dashboard/tsconfig.json cloud/dashboard/tsconfig.app.json cloud/dashboard/tsconfig.node.json cloud/dashboard/vite.config.ts ./
COPY cloud/dashboard/public ./public
COPY cloud/dashboard/src ./src
RUN pnpm build

FROM golang:1.27.2-trixie AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY cloud/db ./cloud/db
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -mod=readonly -trimpath \
    -ldflags "-s -w -buildid= -X main.version=${VERSION} -X main.commit=${GIT_COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/monitor ./cmd/cloud

FROM gcr.io/distroless/static-debian13:nonroot
ENV DASHBOARD_DIR=/dashboard
COPY --from=go-builder --chown=nonroot:nonroot /out/monitor /monitor
COPY --from=dashboard-builder --chown=nonroot:nonroot /src/dashboard/dist /dashboard
USER nonroot:nonroot
EXPOSE 8910
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/monitor", "healthcheck"]
ENTRYPOINT ["/monitor"]
CMD ["serve"]
