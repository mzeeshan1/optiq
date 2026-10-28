# Stage 1 — build both binaries
FROM golang:1.26-alpine AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w -X main.Version=${VERSION}" -o /out/optiq . \
 && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /out/loadgen ./loadgen

# Stage 2 — traffic generator image (docker build --target loadgen)
FROM alpine:3.20 AS loadgen
RUN apk --no-cache add ca-certificates
COPY --from=builder /out/loadgen /usr/local/bin/loadgen
ENTRYPOINT ["loadgen"]

# Stage 3 — shop runtime image (default target)
FROM alpine:3.20

# ca-certificates: Google Fonts / CDN are loaded client-side, but keep TLS roots for outbound calls
# tzdata: appointment slots are in Europe/Berlin local time
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/optiq     ./optiq
COPY --from=builder /app/templates ./templates

EXPOSE 8080

CMD ["./optiq"]
