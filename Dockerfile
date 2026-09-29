FROM golang:1.23-bookworm AS builder
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/hytale-server-manager ./cmd/hsm

FROM eclipse-temurin:25-jre-noble
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates tini procps unzip gosu \
    && rm -rf /var/lib/apt/lists/*
RUN useradd -m -u 1000 -s /bin/bash hsm \
    && mkdir -p /data \
    && chown -R hsm:hsm /data
COPY --from=builder /out/hytale-server-manager /usr/local/bin/hytale-server-manager
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080/tcp 5520-5539/udp
ENV HSM_DATA_DIR=/data \
    HSM_LISTEN=:8080
ENTRYPOINT ["/usr/bin/tini","--","/usr/local/bin/entrypoint.sh"]
CMD ["/usr/local/bin/hytale-server-manager"]
