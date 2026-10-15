# syntax=docker/dockerfile:1

# 构建阶段：静态编译，产物不依赖 libc。
FROM golang:1.25-alpine AS builder
WORKDIR /src

# 先只拷依赖清单，让 go mod download 这一层能被缓存。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# VERSION 由 CI 注入（例如 v0.1.0-beta.1）；本地构建保持 dev。
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/confdeckd ./cmd/confdeckd

# 运行阶段：只有二进制与一个空的数据目录。
FROM alpine:3.22

# 数据文件里有明文密钥：用非 root 用户跑，数据目录归属它。
RUN adduser -D -u 10001 confdeck \
 && mkdir -p /data \
 && chown confdeck:confdeck /data

COPY --from=builder /out/confdeckd /usr/local/bin/confdeckd

USER confdeck
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8600

# token 从环境变量 CONFDECK_TOKEN 读（也可以显式传 -token）。
ENV CONFDECK_TOKEN=""

HEALTHCHECK --interval=10s --timeout=3s --retries=5 \
  CMD wget -qO- http://127.0.0.1:8600/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/confdeckd"]
CMD ["-addr", ":8600", "-data", "/data/confdeck.db"]
