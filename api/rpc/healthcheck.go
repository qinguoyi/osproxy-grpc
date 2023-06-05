package rpc

import (
	"context"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap/plugins"
	"github.com/qinguoyi/ObjectStorageProxy/proto"
)

var lgLogger *bootstrap.LangGoLogger

type HealthCheckServer struct {
	proto.UnimplementedHealthCheckServer
}

func (HealthCheckServer) PingHandler(ctx context.Context, in *proto.NULLRequest) (*proto.HelloResponse, error) {
	var lgDB = new(plugins.LangGoDB).Use("default").NewDB()

	var lgRedis = new(plugins.LangGoRedis).NewRedis()

	lgDB.Exec("select now();")
	//lgLogger.WithContext(ctx).Info("test router")

	// Redis Test
	err := lgRedis.Set(ctx, "key", "value", 0).Err()
	if err != nil {
		panic(err)
	}
	_, err = lgRedis.Get(ctx, "key").Result()
	if err != nil {
		panic(err)
	}
	//lgLogger.WithContext(c).Info(fmt.Sprintf("%v", val))
	return &proto.HelloResponse{
		Code:    0,
		Message: "",
		Data: &proto.HelloReply{
			Res: "test router",
		},
	}, nil
}

func (HealthCheckServer) HealthCheckHandler(ctx context.Context, in *proto.NULLRequest) (*proto.HelloResponse, error) {
	return &proto.HelloResponse{
		Code:    0,
		Message: "",
		Data: &proto.HelloReply{
			Res: "Health ... ",
		},
	}, nil
}
