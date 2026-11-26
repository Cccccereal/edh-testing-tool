FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/powerlevel ./cmd/server

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/powerlevel /usr/local/bin/powerlevel
# 容器场景必须绑所有接口（docker -p 端口映射要能从容器外进来）；
# 容器里没有桌面，别让服务尝试弹系统浏览器。
# 评分走纯 HTTP（getcards + 公式），不再需要本机浏览器内核。
ENV APP_ADDRESS=:18781 \
    POWERLEVEL_OPEN_BROWSER=0
EXPOSE 18781
USER nobody
ENTRYPOINT ["powerlevel"]
