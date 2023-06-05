FROM golang:latest as builder
COPY .. /src/storage
RUN ls /src/storage
RUN pwd
RUN go env -w GOPROXY=https://goproxy.cn,direct  \
    && wget https://github.com/google/protobuf/releases/download/v3.11.2/protobuf-all-3.11.2.zip \
    && apt-get update && apt-get  install unzip && unzip protobuf-all-3.11.2.zip && cd protobuf-3.11.2/  \
    && ./configure && make && make install && ldconfig && protoc --version

RUN cd /src/storage && make all && /bin/cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && echo 'Asia/Shanghai' > /etc/timezone

FROM golang:latest as prod
COPY --from=builder /src/storage /storage
WORKDIR /storage
ENTRYPOINT [ "./bin/storage"]
EXPOSE 8888