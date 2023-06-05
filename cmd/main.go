package main

import (
	"github.com/qinguoyi/ObjectStorageProxy/app"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/base"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/storage"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap/plugins"
)

func main() {
	// config log
	lgConfig := bootstrap.NewConfig("conf/config.yaml")
	_ = bootstrap.NewLogger()

	// plugins DB Redis Minio
	plugins.NewPlugins()
	defer plugins.ClosePlugins()

	// init Snowflake
	base.InitSnowFlake()

	// init storage
	storage.InitStorage(lgConfig)

	// 启动服务
	app.RunServer(lgConfig)
}
