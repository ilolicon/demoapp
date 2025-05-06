FROM golang:1.22.2-alpine3.19
WORKDIR /opt/demoapp
RUN sed -i 's/\(.*\/\/\).*\(\/alpine.*\)/\1mirrors.aliyun.com\2/' /etc/apk/repositories && \
    apk update && \
    apk add --no-cache tzdata && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime

ARG ARCH="arm64"
ARG OS="darwin"
COPY ./build/${OS}-${ARCH}/demoapp ./
COPY ./config.yaml ./
CMD [ "./demoapp" ]
EXPOSE 80
