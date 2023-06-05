package server

import (
	"context"
	"fmt"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap"
	"google.golang.org/grpc"
	"time"
)

func AccessLog(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	requestLog := "access request log: method: %s, begin_time: %s, request: %s"
	beginTime := time.Now().Local()
	bootstrap.NewLogger().Logger.Info(fmt.Sprintf(requestLog, info.FullMethod, beginTime, req))

	resp, err := handler(ctx, req)

	responseLog := "access response log: method: %s, begin_time: %s, end_time: %s, response: %s, err: %v"
	endTime := time.Now().Local()
	bootstrap.NewLogger().Logger.Info(fmt.Sprintf(responseLog, info.FullMethod, beginTime, endTime, resp, err))
	return resp, err
}
