FROM node:23-alpine AS frontend
WORKDIR /web
RUN apk add --no-cache git
RUN git clone https://github.com/komari-monitor/komari-web .
RUN npm install
RUN npm run build

FROM golang:1.24 AS builder
WORKDIR /app
# RUN apt-get update && apt-get install -y musl-tools

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=frontend /web/dist ./public/dist

ENV CGO_ENABLED=1
# RUN CC=musl-gcc go build -trimpath -ldflags="-s -w -linkmode external -extldflags -static" -o komari .
RUN go build -trimpath -ldflags="-s -w -linkmode external -extldflags -static" -o komari .

# FROM scratch
FROM alpine:3.21
WORKDIR /app

COPY --from=builder /app/komari .

ENV GIN_MODE=release
# 数据库配置环境变量（可以在运行时覆盖）
ENV KOMARI_DB_TYPE=sqlite
ENV KOMARI_DB_FILE=/app/data/komari.db
ENV KOMARI_DB_HOST=localhost
ENV KOMARI_DB_PORT=3306
ENV KOMARI_DB_USER=root
ENV KOMARI_DB_PASS=
ENV KOMARI_DB_NAME=komari
ENV KOMARI_LISTEN=0.0.0.0:25774

EXPOSE 25774

# 使用环境变量启动服务
CMD ["/app/komari","server"]