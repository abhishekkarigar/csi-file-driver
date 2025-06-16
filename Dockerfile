FROM golang:1.24-alpine AS builder

RUN apk add --no-cache bash git util-linux

WORKDIR /app
COPY . .

RUN go build -o csi-driver ./cmd/main.go

FROM alpine:3.19 AS final

RUN apk add --no-cache bash util-linux

COPY --from=builder /app/csi-driver /usr/local/bin/csi-driver

ENTRYPOINT ["/usr/local/bin/csi-driver"]


