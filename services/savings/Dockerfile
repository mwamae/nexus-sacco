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
#   /usr/local/bin/guarantor-reminder — hourly worker resending SMS
#                                        consent invites to guarantors
#                                        who haven't responded yet
#                                        (Loans Phase 5)
#   /usr/local/bin/collateral-revaluation-reminder — daily worker that
#                                        nudges officers when a pledged
#                                        collateral's valuation is
#                                        nearing expiry (Phase 1.5b)
#   /usr/local/bin/collateral-insurance-reminder   — daily worker that
#                                        nudges members + flips expired
#                                        policies on collateral
#                                        insurance (Phase 1.5b)
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
    go build -ldflags="-s -w" -o /out/collections-engine ./cmd/collections-engine && \
    go build -ldflags="-s -w" -o /out/guarantor-reminder ./cmd/guarantor-reminder && \
    go build -ldflags="-s -w" -o /out/collateral-revaluation-reminder ./cmd/collateral-revaluation-reminder && \
    go build -ldflags="-s -w" -o /out/collateral-insurance-reminder   ./cmd/collateral-insurance-reminder && \
    go build -ldflags="-s -w" -o /out/statement-mailer ./cmd/statement-mailer && \
    go build -ldflags="-s -w" -o /out/standing-order-processor ./cmd/standing-order-processor && \
    go build -ldflags="-s -w" -o /out/recurring-fee-charger    ./cmd/recurring-fee-charger

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && adduser -D -u 10001 app
# Filestore root (consent proofs etc.). Must exist + be writable by
# the `app` user before the savings binary starts; main.go fails
# fast if mkdir is denied. Production deployments mount a volume
# here so uploads survive container restarts.
RUN mkdir -p /var/lib/savings-uploads && chown -R app:app /var/lib/savings-uploads
USER app
COPY --from=build /out/savings            /usr/local/bin/savings
COPY --from=build /out/posting-dispatcher /usr/local/bin/posting-dispatcher
COPY --from=build /out/loan-snapshotter   /usr/local/bin/loan-snapshotter
COPY --from=build /out/dpd-classifier     /usr/local/bin/dpd-classifier
COPY --from=build /out/collections-engine /usr/local/bin/collections-engine
COPY --from=build /out/guarantor-reminder /usr/local/bin/guarantor-reminder
COPY --from=build /out/collateral-revaluation-reminder /usr/local/bin/collateral-revaluation-reminder
COPY --from=build /out/collateral-insurance-reminder   /usr/local/bin/collateral-insurance-reminder
COPY --from=build /out/statement-mailer                /usr/local/bin/statement-mailer
COPY --from=build /out/standing-order-processor        /usr/local/bin/standing-order-processor
COPY --from=build /out/recurring-fee-charger           /usr/local/bin/recurring-fee-charger
ENV SAVINGS_UPLOADS_DIR=/var/lib/savings-uploads
EXPOSE 8084
ENTRYPOINT ["/usr/local/bin/savings"]
