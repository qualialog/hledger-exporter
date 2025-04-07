FROM golang:1.24.1-alpine AS builder

WORKDIR /app
COPY . .

RUN go build -o ledger_exporter main.go

# ---------- Final Stage ----------
FROM alpine:latest

RUN apk add --no-cache ca-certificates curl

# Install hledger statically
RUN curl -L -o /tmp/hledger.tar.gz https://github.com/simonmichael/hledger/releases/download/1.42.1/hledger-linux-x64.tar.gz \
    && tar -xzf /tmp/hledger.tar.gz -C /usr/local/bin \
    && chmod +x /usr/local/bin/hledger \
    && rm /tmp/hledger.tar.gz

COPY --from=builder /app/ledger_exporter /ledger_exporter

ENTRYPOINT ["/ledger_exporter"]

