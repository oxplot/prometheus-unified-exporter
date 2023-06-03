FROM alpine as certs
RUN apk update && apk add ca-certificates

FROM golang as builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /prometheus-unified-exporter

FROM busybox:glibc
COPY --from=builder /prometheus-unified-exporter /
COPY --from=certs /etc/ssl/certs /etc/ssl/certs
ENV PUE_CONFIG=/config.yaml
ENTRYPOINT ["/prometheus-unified-exporter"]
