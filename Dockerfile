# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27-trixie@sha256:47667934874913a449b96faa7ef4777fa285d9ec29126ee80ed61c45b7cccf0e AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go run ./cmd/webbuild internal/host/web \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X github.com/thekiharani/monitor/internal/host.version=${VERSION}" -o /out/monitor ./cmd/host \
    && mkdir -p /out/data

FROM debian:13.7-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f AS smart-tools
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends smartmontools \
    && mkdir -p /out/bin /out/lib /out/rootfs /out/status.d \
    && cp /usr/sbin/smartctl /out/bin/ \
    && ldd /out/bin/smartctl | awk '/=> \// {print $3} /^[[:space:]]*\// {print $1}' | sort -u > /tmp/libs \
    && xargs -a /tmp/libs -I{} cp -L {} /out/lib/ \
    && loader="$(ldd /out/bin/smartctl | awk '/^[[:space:]]*\// {print $1; exit}')" \
    && test -n "$loader" && cp --parents -L "$loader" /out/rootfs/ \
    && { echo smartmontools; xargs -a /tmp/libs readlink -f | xargs dpkg -S | cut -d: -f1; } | sort -u \
        | xargs -I{} sh -c 'dpkg-query -s {} > /out/status.d/smart-{}'

RUN mkdir -p /out/rootfs/usr \
    && for libdir in lib lib64; do \
         if [ -d "/out/rootfs/$libdir" ]; then mv "/out/rootfs/$libdir" /out/rootfs/usr/; fi; \
       done

FROM gcr.io/distroless/static-debian13:nonroot@sha256:d62067879de890a1def9da0e7766b7d20810a5e5555cef8506b752437d7a3e88
COPY --from=smart-tools /out/rootfs /
COPY --from=smart-tools /out/bin /opt/smart/bin
COPY --from=smart-tools /out/lib /opt/smart/lib
COPY --from=smart-tools /usr/share/smartmontools /usr/share/smartmontools
COPY --from=smart-tools /out/status.d /var/lib/dpkg/status.d/
COPY --from=build /out/monitor /usr/local/bin/monitor
COPY --from=build --chown=65532:65532 /out/data /data
ENV PATH=/opt/smart/bin:/usr/local/bin:/usr/bin:/bin LD_LIBRARY_PATH=/opt/smart/lib
USER 65532:65532
RUN ["/opt/smart/bin/smartctl", "--version"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --start-interval=2s CMD ["/usr/local/bin/monitor", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/monitor"]
CMD ["serve"]
