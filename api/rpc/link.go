package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-redis/redis/v8"
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
	"os"
	"path"
	"strconv"
	"sync"
)

/*
对象信息，生成连接(上传、下载)
*/

type LinkServer struct {
	proto.UnimplementedLinkServer
}

// UploadLinkHandler    初始化上传连接
func (LinkServer) UploadLinkHandler(ctx context.Context, in *proto.GenUploadReq) (*proto.GenUploadResp, error) {
	lgLogger := bootstrap.NewLogger()
	if len(in.FilePath) > utils.LinkLimit {
		st := status.Error(
			codes.InvalidArgument,
			fmt.Sprintf("批量上传路径数量有限，最多%d条", utils.LinkLimit))
		return nil, st
	}

	// deduplication filepath
	fileNameList := utils.RemoveDuplicates(in.FilePath)
	for _, fileName := range fileNameList {
		if base.GetExtension(fileName) == "" {
			st := status.Error(
				codes.InvalidArgument,
				fmt.Sprintf("文件[%s]后缀有误，不能为空", fileName))
			return nil, st
		}
	}

	var resp []*proto.GenUpload
	var resourceInfo []models.MetaDataInfo
	respChan := make(chan *proto.GenUpload, len(fileNameList))
	metaDataInfoChan := make(chan models.MetaDataInfo, len(fileNameList))

	var wg sync.WaitGroup
	for _, fileName := range fileNameList {
		wg.Add(1)
		go base.GenUploadSingle(fileName, int(in.Expire), respChan, metaDataInfoChan, &wg)
	}
	wg.Wait()
	close(respChan)
	close(metaDataInfoChan)

	for re := range respChan {
		resp = append(resp, re)
	}
	for re := range metaDataInfoChan {
		resourceInfo = append(resourceInfo, re)
	}
	if !(len(resp) == len(resourceInfo) && len(resp) == len(fileNameList)) {
		// clean local dir
		for _, i := range resp {
			dirName := path.Join(utils.LocalStore, i.Uid)
			go func() {
				_ = os.RemoveAll(dirName)
			}()
		}
		lgLogger.Logger.Error("生成链接，生成的url和输入数量不一致")
		st := status.Error(
			codes.Internal,
			"生成链接，生成的url和输入数量不一致",
		)
		return nil, st
	}

	// db batch create
	lgDB := new(plugins.LangGoDB).Use("default").NewDB()
	if err := repo.NewMetaDataInfoRepo().BatchCreate(lgDB, &resourceInfo); err != nil {
		lgLogger.Logger.Error("生成链接，批量落数据库失败，详情：", zap.Any("err", err.Error()))
		st := status.Error(
			codes.Internal,
			"生成链接，批量落数据库失败",
		)
		return nil, st
	}
	return &proto.GenUploadResp{
		Code:    0,
		Message: "",
		Data:    resp,
	}, nil
}

// DownloadLinkHandler    获取下载连接
func (LinkServer) DownloadLinkHandler(ctx context.Context, in *proto.GenDownloadReq) (*proto.GenDownloadResp, error) {
	lgLogger := bootstrap.NewLogger()
	if len(in.Uid) > 200 {
		st := status.Error(
			codes.InvalidArgument,
			"uid获取下载链接，数量不能超过200个")
		return nil, st
	}
	expireStr := fmt.Sprintf("%d", in.Expire)
	var uidList []int64
	var resp []*proto.GenDownload
	for _, uidStr := range utils.RemoveDuplicates(in.Uid) {
		uid, err := strconv.ParseInt(uidStr, 10, 64)
		if err != nil {
			st := status.Error(
				codes.InvalidArgument,
				"uid参数有误")
			return nil, st
		}

		// 查询redis
		key := fmt.Sprintf("%d-%s", uid, expireStr)
		lgRedis := new(plugins.LangGoRedis).NewRedis()
		val, err := lgRedis.Get(context.Background(), key).Result()
		// key在redis中不存在
		if err == redis.Nil {
			uidList = append(uidList, uid)
			continue
		}
		if err != nil {
			lgLogger.Logger.Error("获取下载链接，查询redis失败")
			st := status.Error(codes.Internal, "内部异常")
			return nil, st
		}
		var msg *proto.GenDownload
		if err := json.Unmarshal([]byte(val), &msg); err != nil {
			lgLogger.Logger.Error(fmt.Sprintf("获取下载链接，查询redis，结果序列化失败，详情:%s", err.Error()))
			st := status.Error(codes.Internal, "内部异常")
			return nil, st
		}
		resp = append(resp, msg)
	}

	lgDB := new(plugins.LangGoDB).Use("default").NewDB()
	metaList, err := repo.NewMetaDataInfoRepo().GetByUidList(lgDB, uidList)
	if err != nil {
		lgLogger.Logger.Error("获取下载链接，查询数据元数据信息失败")
		st := status.Error(codes.Internal, "内部异常")
		return nil, st
	}
	if len(metaList) == 0 && len(resp) == 0 {
		return &proto.GenDownloadResp{}, nil
	}
	uidMapMeta := map[int64]models.MetaDataInfo{}
	for _, meta := range metaList {
		uidMapMeta[meta.UID] = meta
	}

	respChan := make(chan *proto.GenDownload, len(metaList))
	var wg sync.WaitGroup
	for _, uid := range uidList {
		wg.Add(1)
		go base.GenDownloadSingle(uidMapMeta[uid], expireStr, respChan, &wg)
	}
	wg.Wait()
	close(respChan)

	for re := range respChan {
		resp = append(resp, re)
	}
	return &proto.GenDownloadResp{
		Code:    0,
		Message: "",
		Data:    resp,
	}, nil
}
