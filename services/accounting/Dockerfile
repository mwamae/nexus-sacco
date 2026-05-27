# Multi-stage build for the accounting service.

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -ldflags="-s -w" -o /out/accounting ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && adduser -D -u 10001 app
USER app
COPY --from=build /out/accounting /usr/local/bin/accounting
EXPOSE 8086
ENTRYPOINT ["/usr/local/bin/accounting"]
