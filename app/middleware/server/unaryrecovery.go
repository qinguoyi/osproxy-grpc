package server

import (
	"context"
	"fmt"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func Recovery(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	// 注意返回值，有变量和无变量的区别
	// handler相当于gin中的Next，但每个拦截器需要注意都要处理返回，而不是像gin那样可以传递
	err = status.Error(codes.Internal, "内部异常")
	defer func() {
		if e := recover(); e != nil {
			recoveryLog := "recovery log: method: %s, message: %v, stack: %s"
			bootstrap.NewLogger().Logger.Error(fmt.Sprintf(recoveryLog, info.FullMethod, e, string("error")))
		}
	}()
	resp, err = handler(ctx, req)

	return resp, err
}
