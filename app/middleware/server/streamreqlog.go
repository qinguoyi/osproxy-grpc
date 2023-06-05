package server

import (
	"fmt"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap"
	"google.golang.org/grpc"
)

func LoggingInterceptor(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	bootstrap.NewLogger().Logger.Info(fmt.Sprintf("gRPC method: %s", info.FullMethod))
	err := handler(srv, &loggingServerStream{stream})

	if err != nil {
		bootstrap.NewLogger().Logger.Error(fmt.Sprintf("gRPC method %q failed with error %v", info.FullMethod, err))
	}
	return err
}

type loggingServerStream struct {
	grpc.ServerStream
}

func (ss *loggingServerStream) RecvMsg(m interface{}) error {
	return ss.ServerStream.RecvMsg(m)
}

func (ss *loggingServerStream) SendMsg(m interface{}) error {
	return ss.ServerStream.SendMsg(m)
}
