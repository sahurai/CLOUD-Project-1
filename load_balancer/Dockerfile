FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN go build -o /load-balancer .

FROM alpine:3.20
COPY --from=builder /load-balancer /load-balancer
EXPOSE 8080
ENTRYPOINT ["/load-balancer"]
