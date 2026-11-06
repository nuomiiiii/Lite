FROM alpine:3.21

WORKDIR /app

# Docker buildx 会在构建时自动填充这些变量
ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache ca-certificates tzdata

COPY --chmod=755 Lite-${TARGETOS}-${TARGETARCH} /app/Lite
# 保留旧容器启动命令 /app/komari，方便直接换镜像。
RUN ln -s Lite /app/komari

ENV GIN_MODE=release
ENV LITE_DEPLOYMENT=docker
ENV TZ=Asia/Shanghai
ENV GODEBUG=disablethp=1

EXPOSE 27777 36888

CMD ["/app/Lite", "server"]
