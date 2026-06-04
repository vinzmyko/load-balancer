FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o loadbalancer ./cmd/loadbalancer

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/loadbalancer .
COPY config.yaml .
EXPOSE 8080 9091
CMD ["./loadbalancer"]
