# syntax=docker/dockerfile:1.7
FROM golang:1.23-alpine AS builder

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /lut-puller .

FROM alpine:3.21 AS runtime

RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /lut-puller /usr/local/bin/lut-puller
COPY docker-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
CMD []
