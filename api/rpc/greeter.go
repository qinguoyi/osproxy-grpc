package rpc

import (
	"context"
	"github.com/qinguoyi/ObjectStorageProxy/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GreeterServer struct {
	proto.UnimplementedGreeterServer
}

func (GreeterServer) SayHello(ctx context.Context, in *proto.HelloRequest) (*proto.HelloResponse, error) {
	msg := in.Name + " world"
	var a []string
	if in.Name == "111" {
		// 自定义状态码
		st := status.Error(
			codes.PermissionDenied,
			"权限问题")

		return nil, st
	}
	if in.Name == "1111" {
		// 自定义状态码

		return &proto.HelloResponse{
			Code:    0,
			Message: a[0],
			Data: &proto.HelloReply{
				Res: msg,
			},
		}, nil
	}
	return &proto.HelloResponse{
		Code:    0,
		Message: "",
		Data: &proto.HelloReply{
			Res: msg,
		},
	}, nil
}

func (GreeterServer) SayPing(ctx context.Context, in *proto.NULLRequest) (*proto.HelloResponse, error) {
	return &proto.HelloResponse{
		Code:    0,
		Message: "",
		Data: &proto.HelloReply{
			Res: "Health ... ",
		},
	}, nil
}
