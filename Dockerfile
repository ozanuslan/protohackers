# syntax=docker/dockerfile:1

ARG ALPINE_VERSION=3.19
ARG GO_VERSION=1.22.3

FROM docker.io/library/golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS builder
LABEL auto_prune="true"

WORKDIR /build
COPY . .

RUN go mod download
RUN go build -o server .

FROM docker.io/library/alpine:${ALPINE_VERSION}

WORKDIR /
COPY --from=builder /build/server /server

CMD ["/server"]
