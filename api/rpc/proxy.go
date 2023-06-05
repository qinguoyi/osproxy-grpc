package rpc

import (
	"context"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/base"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/utils"
	"github.com/qinguoyi/ObjectStorageProxy/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"path"
	"strconv"
)

// ProxyServer 询问文件是否在当前服务
type ProxyServer struct {
	proto.UnimplementedProxyServer
}

func (ProxyServer) ProxyHandler(ctx context.Context, in *proto.ProxyReq) (*proto.ProxyResp, error) {
	uidStr := in.GetUid()
	_, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			"参数有误")
		return nil, st
	}
	dirName := path.Join(utils.LocalStore, uidStr)
	if _, err := os.Stat(dirName); os.IsNotExist(err) {
		st := status.Error(
			codes.NotFound,
			"数据不存在")
		return nil, st
	} else {
		ip, err := base.GetOutBoundIP()
		if err != nil {
			panic(err)
		}
		return &proto.ProxyResp{
			Code:    0,
			Message: "",
			Data:    ip,
		}, nil
	}
}
