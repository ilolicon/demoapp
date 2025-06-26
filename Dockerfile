FROM registry.cn-hangzhou.aliyuncs.com/kubernetes-syncer/alpine:latest
LABEL maintainer="ilolicon <97431110@qq.com>"

RUN sed -i 's/\(.*\/\/\).*\(\/alpine.*\)/\1mirrors.aliyun.com\2/' /etc/apk/repositories && \
    apk update && \
    apk add --no-cache tzdata && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime

WORKDIR /opt/demoapp
ARG ARCH="amd64"
ARG OS="linux"
COPY ./build/${OS}-${ARCH}/demoapp /bin/
COPY ./config.yaml ./
ENTRYPOINT [ "/bin/demoapp" ]
EXPOSE 80
