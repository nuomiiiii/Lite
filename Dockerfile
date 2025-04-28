FROM golang:1.24 AS builder
WORKDIR /app
RUN apt-get update && apt-get install -y musl-tools

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=1
RUN CC=musl-gcc go build -trimpath -ldflags="-s -w -linkmode external -extldflags -static" -o komari .

FROM scratch
WORKDIR /app

COPY --from=builder /app/komari .

ENV GIN_MODE=release
EXPOSE 25774

CMD ["/app/komari","server", "-l", "0.0.0.0:25774","-d", "/app/data/komari.db"]