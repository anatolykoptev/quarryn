FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY . .
RUN VERSION=$(git describe --tags --always 2>/dev/null || echo "dev") && \
    CGO_ENABLED=0 go build -mod=vendor -ldflags="-s -w -X main.version=${VERSION}" -o /go-product-search ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates curl
WORKDIR /app
COPY --from=builder /go-product-search /usr/local/bin/go-product-search
EXPOSE 8922
HEALTHCHECK --interval=15s --timeout=5s --retries=3 CMD wget -q --spider http://localhost:8922/healthz
ENTRYPOINT ["go-product-search"]
