package rpc

import (
	"context"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/repo"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap/plugins"
	"github.com/qinguoyi/ObjectStorageProxy/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strconv"
)

type CheckPointServer struct {
	proto.UnimplementedCheckPointServer
}

// CheckPointHandler    断点续传
func (CheckPointServer) CheckPointHandler(ctx context.Context, in *proto.CPReq) (*proto.CPResp, error) {
	lgLogger := bootstrap.NewLogger()
	uidStr := in.Uid
	uid, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			"参数有误")
		return nil, st
	}

	// 断点续传只看未上传且分片的数据
	lgDB := new(plugins.LangGoDB).Use("default").NewDB()
	partUidInfo, err := repo.NewMetaDataInfoRepo().GetPartByUid(lgDB, uid)
	if err != nil {
		lgLogger.Logger.Error("查询断点续传数据失败")
		st := status.Error(
			codes.Internal,
			"查询断点续传数据失败",
		)
		return nil, st
	}
	if len(partUidInfo) == 0 {
		st := status.Error(
			codes.InvalidArgument,
			"当前文件uid不存在分片数据")
		return nil, st
	}

	// 断点续传查询分片数字
	partNumInfo, err := repo.NewMultiPartInfoRepo().GetPartNumByUid(lgDB, uid)
	var partNum []int32
	for _, partInfo := range partNumInfo {
		partNum = append(partNum, int32(partInfo.ChunkNum))
	}
	return &proto.CPResp{
		Code:    0,
		Message: "",
		Data:    partNum,
	}, nil
}
