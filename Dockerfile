FROM registry.cn-hangzhou.aliyuncs.com/kubernetes-syncer/golang:1.23.10-alpine3.22 AS builder
WORKDIR /demoapp
ENV GOPROXY=https://goproxy.cn,direct
RUN sed -i 's/\(.*\/\/\).*\(\/alpine.*\)/\1mirrors.aliyun.com\2/' /etc/apk/repositories && \
    apk update && \
    apk add --no-cache ca-certificates tzdata make gcc musl-dev git bash
COPY ./go.mod ./
COPY ./go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 make all

FROM registry.cn-hangzhou.aliyuncs.com/kubernetes-syncer/alpine AS runner
COPY --from=builder /usr/share/zoneinfo/Asia/Shanghai /etc/localtime
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
ARG OS="linux"
ARG ARCH="amd64"
COPY --from=builder /demoapp/build/${OS}-${ARCH}/demoapp /bin/
COPY --from=builder /demoapp/config.yaml /etc/demoapp/
ENTRYPOINT [ "/bin/demoapp" ]
CMD [ "--config.file", "/etc/demoapp/config.yaml" ]
EXPOSE 80
