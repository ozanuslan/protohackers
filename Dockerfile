FROM docker.io/library/golang:1.22.3-alpine3.19 as builder
LABEL auto_prune="true"

WORKDIR /build
COPY . .

RUN go mod download
RUN go build -o server .

FROM docker.io/library/alpine:3.19

WORKDIR /
COPY --from=builder /build/server /server

CMD ["/server"]
