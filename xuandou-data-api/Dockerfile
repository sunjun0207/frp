FROM golang:1.26 AS building

WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/xuandou-data-api .

FROM alpine:3

RUN apk add --no-cache tzdata
COPY --from=building /out/xuandou-data-api /usr/bin/xuandou-data-api

ENV TZ=Asia/Shanghai \
    DATA_DIR=/data \
    TLS_CERT=/certs/fullchain.pem \
    TLS_KEY=/certs/privkey.pem \
    HTTPS_ADDR=127.0.0.1:4000 \
    HTTP_ADDR=127.0.0.1:4080 \
    TCP_ADDR=127.0.0.1:4001 \
    UDP_ADDR=127.0.0.1:4002 \
    QUERY_ADDR=127.0.0.1:3090

EXPOSE 4000 4080 4001 4002/udp
ENTRYPOINT ["/usr/bin/xuandou-data-api"]
