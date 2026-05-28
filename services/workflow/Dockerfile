# Multi-stage build for the workflow service.
#
# Build context: repo root. docker-compose.yml sets
#   context: .
#   dockerfile: services/workflow/Dockerfile

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly

COPY shared/             ./shared/
COPY services/workflow/  ./services/workflow/

WORKDIR /src/services/workflow
RUN go mod download
RUN go build -ldflags="-s -w" -o /out/workflow ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
USER app
COPY --from=build /out/workflow /usr/local/bin/workflow
EXPOSE 8083
ENTRYPOINT ["/usr/local/bin/workflow"]
