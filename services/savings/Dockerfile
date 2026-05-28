# Multi-stage build for the savings service.
#
# Build context: repo root. docker-compose.yml sets
#   context: .
#   dockerfile: services/savings/Dockerfile
#
# Produces five binaries:
#   /usr/local/bin/savings            — HTTP service (cmd/server)
#   /usr/local/bin/posting-dispatcher — background outbox drainer
#   /usr/local/bin/loan-snapshotter   — daily portfolio snapshot worker
#                                        (Loans Phase 2 — feeds the
#                                        Reports page's trend charts)
#   /usr/local/bin/dpd-classifier     — daily DPD + SASRA + IFRS 9
#                                        classification worker (Loans
#                                        Phase 3 — feeds the
#                                        provisioning + classification
#                                        timeline surfaces)
#   /usr/local/bin/collections-engine — hourly collections workflow
#                                        worker (Loans Phase 4 — fires
#                                        auto-SMS + generates letter
#                                        PDFs per the escalation ladder)
#
# Compose decides which one each container runs by overriding ENTRYPOINT.

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly

COPY shared/            ./shared/
COPY services/savings/  ./services/savings/

WORKDIR /src/services/savings
RUN go mod download
RUN go build -ldflags="-s -w" -o /out/savings            ./cmd/server && \
    go build -ldflags="-s -w" -o /out/posting-dispatcher ./cmd/posting-dispatcher && \
    go build -ldflags="-s -w" -o /out/loan-snapshotter   ./cmd/loan-snapshotter && \
    go build -ldflags="-s -w" -o /out/dpd-classifier     ./cmd/dpd-classifier && \
    go build -ldflags="-s -w" -o /out/collections-engine ./cmd/collections-engine

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && adduser -D -u 10001 app
USER app
COPY --from=build /out/savings            /usr/local/bin/savings
COPY --from=build /out/posting-dispatcher /usr/local/bin/posting-dispatcher
COPY --from=build /out/loan-snapshotter   /usr/local/bin/loan-snapshotter
COPY --from=build /out/dpd-classifier     /usr/local/bin/dpd-classifier
COPY --from=build /out/collections-engine /usr/local/bin/collections-engine
EXPOSE 8084
ENTRYPOINT ["/usr/local/bin/savings"]
