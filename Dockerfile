FROM golang:1.22-alpine AS builder

RUN apk add --no-cache make

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN make build

FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -u 1000 dbbouncer

COPY --from=builder /build/bin/dbbouncer /usr/local/bin/dbbouncer
COPY --from=builder /build/configs/dbbouncer.yaml /etc/dbbouncer/dbbouncer.yaml

USER dbbouncer

EXPOSE 6432 3307 8080

ENTRYPOINT ["dbbouncer"]
CMD ["-config", "/etc/dbbouncer/dbbouncer.yaml"]
