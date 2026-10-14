FROM golang:1.23-bookworm AS builder
WORKDIR /src
ARG APP_VERSION=0.6.0
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${APP_VERSION}" -o /out/hytale-server-manager ./cmd/hsm

FROM eclipse-temurin:25-jre-noble
# curl, libicu74, libssl3t64, zlib1g and libstdc++6: tModLoader's launch scripts
# download .NET with curl, and .NET needs ICU, OpenSSL and zlib.
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates tini procps unzip gosu \
    && apt-get install -y --no-install-recommends curl libicu74 libssl3t64 zlib1g libstdc++6 \
    && if [ "$(dpkg --print-architecture)" = "amd64" ]; then apt-get install -y --no-install-recommends libc6-i386 lib32gcc-s1 lib32stdc++6; fi \
    && rm -rf /var/lib/apt/lists/*
RUN useradd --create-home --shell /bin/bash hsm \
    && mkdir -p /data \
    && chown -R hsm:hsm /data
COPY --from=builder /out/hytale-server-manager /usr/local/bin/hytale-server-manager
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8008/tcp 5520-5525/tcp 5520-5525/udp 7777-7782/udp
ENV HSM_DATA_DIR=/data \
    HSM_LISTEN=:8008
ENTRYPOINT ["/usr/bin/tini","--","/usr/local/bin/entrypoint.sh"]
CMD ["/usr/local/bin/hytale-server-manager"]
