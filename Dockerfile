ARG BASE_IMAGE="golang:1.22.12-alpine3.21"
FROM ${BASE_IMAGE}
RUN sed -i 's/\(.*\/\/\).*\(\/alpine.*\)/\1mirrors.aliyun.com\2/' /etc/apk/repositories && \
    apk update && \
    apk add --no-cache tzdata && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime

WORKDIR /opt/demoapp
ARG ARCH="arm64"
ARG OS="darwin"
COPY ./build/${OS}-${ARCH}/demoapp /bin/
COPY ./config.yaml ./
ENTRYPOINT [ "/bin/demoapp" ]
EXPOSE 80
