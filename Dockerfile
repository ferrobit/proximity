# syntax=docker/dockerfile:1
ARG MIHOMO_IMAGE=metacubex/mihomo:v1.19.34
ARG GO_IMAGE=golang:1.25-bookworm
ARG NODE_IMAGE=node:22-bookworm-slim
ARG BASE_IMAGE=debian:bookworm-slim

FROM ${MIHOMO_IMAGE} AS mihomo

FROM ${NODE_IMAGE} AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/index.html web/vite.config.js ./
COPY web/public ./public
COPY web/src ./src
RUN npm run build

FROM ${GO_IMAGE} AS amnezia
ARG AWG_GO_VERSION=v3.1.20260828
ARG AWG_TOOLS_VERSION=v3.1.20260812
RUN git clone --depth 1 --branch ${AWG_GO_VERSION} \
        https://github.com/amnezia-vpn/amneziawg-go /src/amneziawg-go && \
    cd /src/amneziawg-go && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/amneziawg-go .
RUN git clone --depth 1 --branch ${AWG_TOOLS_VERSION} \
        https://github.com/amnezia-vpn/amneziawg-tools /src/amneziawg-tools && \
    make -C /src/amneziawg-tools/src && \
    cp /src/amneziawg-tools/src/wg /out/awg

FROM ${GO_IMAGE} AS build
ARG VERSION=1.0.0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go ./web/embed.go
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/proximity ./cmd/proximity

FROM ${BASE_IMAGE}
LABEL org.opencontainers.image.title="proximity"
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates iproute2 iputils-ping \
        openvpn oathtool tzdata && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /config /run/proximity /usr/share/proximity/geodata
COPY --from=mihomo /mihomo /usr/local/bin/mihomo
COPY --from=mihomo /root/.config/mihomo/geoip.metadb /root/.config/mihomo/geosite.dat \
     /usr/share/proximity/geodata/
COPY --from=amnezia /out/amneziawg-go /out/awg /usr/local/bin/
COPY --from=build /out/proximity /usr/local/bin/proximity
ENV PROXIMITY_CONFIG=/config \
    PROXIMITY_RUN=/run/proximity \
    PROXIMITY_LISTEN=:8899
VOLUME ["/config"]
EXPOSE 8899 7899
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/usr/local/bin/proximity", "health"]
ENTRYPOINT ["/usr/local/bin/proximity"]
