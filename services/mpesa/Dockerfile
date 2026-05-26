# Multi-stage build for the mpesa service. Mirrors services/savings/Dockerfile.

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -ldflags="-s -w" -o /out/mpesa ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
USER app
COPY --from=build /out/mpesa /usr/local/bin/mpesa
EXPOSE 8087
ENTRYPOINT ["/usr/local/bin/mpesa"]
