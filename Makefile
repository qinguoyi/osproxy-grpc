.PHONY: all build swager clean help

BINARY="storage"
GO_CMD = $(shell which go)
GO_BUILD_CMD=$(GO_CMD) build
BASEDIR=$(shell pwd)
CONFIG_DIR=conf
EXE=$(BASEDIR)/bin/$(BINARY)

.PHONY: all
all: swager build

.PHONY: build
build:
	@cd ./cmd && CGO_ENABLED=0 $(GO_BUILD_CMD) -v -o $(BASEDIR)/bin/$(BINARY)
	@cp -r ${BASEDIR}/${CONFIG_DIR} $(BASEDIR)/bin/

.PHONY:swager
swager:
	@$(GO_CMD) get google.golang.org/protobuf
	@$(GO_CMD) get google.golang.org/grpc@latest
	@$(GO_CMD) install github.com/golang/protobuf/protoc-gen-go
	@$(GO_CMD) get google.golang.org/grpc/cmd/protoc-gen-go-grpc
	@$(GO_CMD) install google.golang.org/grpc/cmd/protoc-gen-go-grpc
	@$(GO_CMD) get github.com/grpc-ecosystem/grpc-gateway/protoc-gen-swagger
	@$(GO_CMD) install github.com/grpc-ecosystem/grpc-gateway/protoc-gen-swagger
	@$(GO_CMD) install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2
	@$(GO_CMD) install github.com/go-bindata/go-bindata/...
	@$(GO_CMD) install github.com/elazarl/go-bindata-assetfs/...
	echo $(shell which protoc)
	@protoc  -I=proto --go_out=proto --go_opt=paths=source_relative --go-grpc_out=proto --go-grpc_opt=paths=source_relative --grpc-gateway_out=proto --grpc-gateway_opt=paths=source_relative ./proto/storage.proto
	@go-bindata --nocompress -pkg swagger -o docs/swagger/data.go docs/swagger-ui/...
	@protoc -I=proto -I. -I./proto/google/api --swagger_out=logtostderr=true:./proto  ./proto/storage.proto

.PHONY: clean
clean:
	@if [ -f ${EXE} ] ; then rm ${EXE} ; fi

.PHONY: help
help:
	@echo "make all- 编译生成二进制文件, 生成接口文档"
	@echo "make build - 编译 Go 代码, 生成二进制文件"
	@echo "make swag - 生成接口文档"
	@echo "make clean - 移除二进制文件"