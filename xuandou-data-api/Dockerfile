# 国内构建默认走 DaoCloud 镜像、阿里云 apk 和 goproxy.cn。
# 已把基础镜像推到自有仓库时，可改成 xuandou-server 使用的 ACR：
# --build-arg GOLANG_IMAGE=xuandou-acr-registry.cn-hangzhou.cr.aliyuncs.com/prod/golang:1.26
# --build-arg ALPINE_IMAGE=xuandou-acr-registry.cn-hangzhou.cr.aliyuncs.com/prod/alpine:3
ARG GOLANG_IMAGE=docker.m.daocloud.io/library/golang:1.26
ARG ALPINE_IMAGE=docker.m.daocloud.io/library/alpine:3

FROM ${GOLANG_IMAGE} AS building

ARG GOPROXY=https://goproxy.cn,direct
ARG GOSUMDB=sum.golang.google.cn
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/xuandou-data-api .

FROM ${ALPINE_IMAGE}

ARG ALPINE_APK_REPO_MIRROR=https://mirrors.aliyun.com
RUN set -eux; \
    if [ -n "${ALPINE_APK_REPO_MIRROR}" ]; then \
      sed -i "s#https://dl-cdn.alpinelinux.org#${ALPINE_APK_REPO_MIRROR}#g" /etc/apk/repositories; \
    fi; \
    apk update --no-cache \
    || ( sed -i 's|https://|http://|g' /etc/apk/repositories && apk update --no-cache ); \
    apk add --no-cache tzdata

COPY --from=building /out/xuandou-data-api /usr/bin/xuandou-data-api

ENV TZ=Asia/Shanghai \
    DATA_DIR=/data \
    TLS_ENABLE=true \
    TLS_CERT=/certs/fullchain.pem \
    TLS_KEY=/certs/privkey.pem \
    HTTPS_ADDR=127.0.0.1:4000 \
    HTTP_ADDR=127.0.0.1:4080 \
    TCP_ADDR=127.0.0.1:4001 \
    UDP_ADDR=127.0.0.1:4002 \
    QUERY_ADDR=0.0.0.0:3090

EXPOSE 4000 4080 4001 4002/udp 3090
ENTRYPOINT ["/usr/bin/xuandou-data-api"]
