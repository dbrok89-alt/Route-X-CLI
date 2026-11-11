# Multi-stage build for Route-X CLI

# Stage 1: Build binary using Go 1.23 Alpine
FROM golang:1.23-alpine AS builder

WORKDIR /app

COPY go.mod ./
COPY . ./

RUN CGO_ENABLED=0 go build -o route-x .

# Stage 2: Final lightweight runtime image
FROM alpine:3.20

RUN apk add --no-cache git bash ca-certificates

COPY --from=builder /app/route-x /usr/local/bin/route-x

ENTRYPOINT ["route-x"]
