# Multi-stage build for the identity service.

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -ldflags="-s -w" -o /out/identity ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
USER app
COPY --from=build /out/identity /usr/local/bin/identity
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/identity"]
