FROM golang:1.24 AS builder
WORKDIR /app
COPY . .
RUN go mod download
ENV CGO_ENABLED=1
ENV GIN_MODE=release
RUN go build -trimpath -ldflags="-s -w" -o komari .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/komari .

EXPOSE 25774

CMD ["./komari server"]