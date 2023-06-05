package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/qinguoyi/ObjectStorageProxy/app/models"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/base"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/repo"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/utils"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap/plugins"
	"github.com/qinguoyi/ObjectStorageProxy/proto"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"path/filepath"
	"time"
)

/*
秒传
*/

type ResumeServer struct {
	proto.UnimplementedResumeServer
}

// ResumeHandler    秒传
func (ResumeServer) ResumeHandler(ctx context.Context, in *proto.ResumeReq) (*proto.ResumeResp, error) {
	lgLogger := bootstrap.NewLogger()
	if len(in.Data) > utils.LinkLimit {
		st := status.Error(
			codes.InvalidArgument,
			fmt.Sprintf("判断文件秒传，数量不能超过%d个", utils.LinkLimit))
		return nil, st
	}

	var md5List []string
	md5MapName := map[string]string{}
	for _, i := range in.Data {
		md5MapName[i.Md5] = i.Path
		md5List = append(md5List, i.Md5)
	}
	md5List = utils.RemoveDuplicates(md5List)

	md5MapResp := map[string]*proto.ResumeInfo{}
	for _, md5 := range md5List {
		tmp := proto.ResumeInfo{
			Uid: "",
			Md5: md5,
		}
		md5MapResp[md5] = &tmp
	}

	// 秒传只看已上传且完整文件的数据
	lgDB := new(plugins.LangGoDB).Use("default").NewDB()
	resumeInfo, err := repo.NewMetaDataInfoRepo().GetResumeByMd5(lgDB, md5List)
	if err != nil {
		lgLogger.Logger.Error("查询秒传数据失败")
		st := status.Error(
			codes.Internal,
			"查询秒传数据失败",
		)
		return nil, st
	}
	// 去重
	md5MapMetaInfo := map[string]models.MetaDataInfo{}
	for _, resume := range resumeInfo {
		if _, ok := md5MapMetaInfo[resume.Md5]; !ok {
			md5MapMetaInfo[resume.Md5] = resume
		}
	}

	var newMetaDataList []models.MetaDataInfo
	for _, resume := range in.Data {
		if _, ok := md5MapMetaInfo[resume.Md5]; !ok {
			continue
		}
		// 相同数据上传需要复制一份数据
		uid, _ := base.NewSnowFlake().NextId()
		now := time.Now()
		newMetaDataList = append(newMetaDataList,
			models.MetaDataInfo{
				UID:         uid,
				Bucket:      md5MapMetaInfo[resume.Md5].Bucket,
				Name:        filepath.Base(resume.Path),
				StorageName: md5MapMetaInfo[resume.Md5].StorageName,
				Address:     md5MapMetaInfo[resume.Md5].Address,
				Md5:         resume.Md5,
				MultiPart:   false,
				StorageSize: md5MapMetaInfo[resume.Md5].StorageSize,
				Status:      1,
				ContentType: md5MapMetaInfo[resume.Md5].ContentType,
				CreatedAt:   &now,
				UpdatedAt:   &now,
			})
		fmt.Println(uid)
		md5MapResp[resume.Md5].Uid = fmt.Sprintf("%d", uid)
	}
	if len(newMetaDataList) != 0 {
		if err := repo.NewMetaDataInfoRepo().BatchCreate(lgDB, &newMetaDataList); err != nil {
			lgLogger.Logger.Error("秒传批量落数据库失败，详情：", zap.Any("err", err.Error()))
			st := status.Error(
				codes.Internal,
				"内部异常",
			)
			return nil, st
		}
	}
	lgRedis := new(plugins.LangGoRedis).NewRedis()
	for _, metaDataCache := range newMetaDataList {
		b, err := json.Marshal(metaDataCache)
		if err != nil {
			lgLogger.Logger.Warn("秒传数据，写入redis失败")
		}
		lgRedis.SetNX(context.Background(), fmt.Sprintf("%d-meta", metaDataCache.UID), b, 5*60*time.Second)
	}
	var respList []*proto.ResumeInfo
	for _, resp := range md5MapResp {
		respList = append(respList, resp)
	}
	return &proto.ResumeResp{
		Code:    0,
		Message: "",
		Data:    respList,
	}, nil
}
