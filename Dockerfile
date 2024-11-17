# syntax=docker/dockerfile:1

ARG ALPINE_BASE=3.19

FROM docker.io/library/golang:1.22.3-alpine${ALPINE_BASE} as builder
LABEL auto_prune="true"

WORKDIR /build
COPY . .

RUN go mod download
RUN go build -o server .

FROM docker.io/library/alpine:${ALPINE_BASE}

WORKDIR /
COPY --from=builder /build/server /server

CMD ["/server"]
