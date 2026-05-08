FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /orchestrator .

# Final image carries kubectl so the orchestrator can shell out to the cluster.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates curl bash \
    && curl -sSL -o /usr/local/bin/kubectl \
       https://dl.k8s.io/release/v1.30.0/bin/linux/amd64/kubectl \
    && chmod +x /usr/local/bin/kubectl

COPY --from=builder /orchestrator /orchestrator
RUN mkdir -p /var/lib/orchestrator

EXPOSE 9000
ENTRYPOINT ["/orchestrator"]
