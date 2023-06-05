package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/qinguoyi/ObjectStorageProxy/app/models"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/base"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/repo"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/storage"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/thirdparty"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/utils"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap/plugins"
	"github.com/qinguoyi/ObjectStorageProxy/proto"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"os"
	"path"
	"strconv"
	"sync"
	"time"
)

/*
对象上传
上传和下载接口，grpc-gateway不支持file，https://github.com/grpc-ecosystem/grpc-gateway/issues/500
*/

type UploadServer struct {
	proto.UnimplementedUploadServer
}

func (UploadServer) UploadSingleHandler(stream proto.Upload_UploadSingleHandlerServer) error {
	lgLogger := bootstrap.NewLogger()
	in, err := stream.Recv()
	if err != nil && err != io.EOF {
		st := status.Error(
			codes.NotFound,
			err.Error())
		return st
	}
	uidStr := in.GetUid()
	md5 := in.GetMd5()
	date := in.GetDate()
	expireStr := in.GetExpire()
	signature := in.GetSignature()

	uid, err, errorInfo := base.CheckValid(uidStr, date, expireStr)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			errorInfo)
		return st
	}

	if !base.CheckUploadSignature(date, expireStr, signature) {
		st := status.Error(
			codes.InvalidArgument,
			"签名校验失败")
		return st
	}

	// 判断记录是否存在
	lgDB := new(plugins.LangGoDB).Use("default").NewDB()
	metaData, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			"当前上传链接无效，uid不存在")
		return st
	}

	dirName := path.Join(utils.LocalStore, uidStr)
	// 判断是否上传过，md5
	resumeInfo, err := repo.NewMetaDataInfoRepo().GetResumeByMd5(lgDB, []string{md5})
	if err != nil {
		lgLogger.Logger.Error("上传单个文件，查询是否已上传失败")
		st := status.Error(
			codes.Internal,
			"内部异常")
		return st
	}
	if len(resumeInfo) != 0 {
		now := time.Now()
		if err := repo.NewMetaDataInfoRepo().Updates(lgDB, metaData.UID, map[string]interface{}{
			"bucket":       resumeInfo[0].Bucket,
			"storage_name": resumeInfo[0].StorageName,
			"address":      resumeInfo[0].Address,
			"md5":          md5,
			"storage_size": resumeInfo[0].StorageSize,
			"multi_part":   false,
			"status":       1,
			"updated_at":   &now,
			"content_type": resumeInfo[0].ContentType,
		}); err != nil {
			lgLogger.Logger.Error("上传完更新数据失败")
			st := status.Error(
				codes.Internal,
				"上传完更新数据失败")
			return st
		}
		if err := os.RemoveAll(dirName); err != nil {
			lgLogger.Logger.Error(fmt.Sprintf("删除目录失败，详情%s", err.Error()))
			st := status.Error(
				codes.Internal,
				fmt.Sprintf("删除目录失败，详情%s", err.Error()))
			return st
		}
		// 首次写入redis 元数据
		lgRedis := new(plugins.LangGoRedis).NewRedis()
		metaCache, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
		if err != nil {
			lgLogger.Logger.Error("上传数据，查询数据元信息失败")
			st := status.Error(
				codes.Internal,
				"查询数据元信息失败")
			return st
		}
		b, err := json.Marshal(metaCache)
		if err != nil {
			lgLogger.Logger.Warn("上传数据，写入redis失败")
		}
		lgRedis.SetNX(context.Background(), fmt.Sprintf("%s-meta", uidStr), b, 5*60*time.Second)
		return stream.SendAndClose(&proto.UploadSingleResp{Code: 0, Message: "", Data: ""})
	}
	// 判断是否在本地
	if _, err := os.Stat(dirName); os.IsNotExist(err) {
		// 不在本地，询问集群内其他服务并转发
		serviceList, err := base.NewServiceRegister().Discovery()
		if err != nil || serviceList == nil {
			lgLogger.Logger.Error("发现其他服务失败")
			st := status.Error(
				codes.Internal,
				"发现其他服务失败")
			return st
		}
		var wg sync.WaitGroup
		var ipList []string
		ipChan := make(chan string, len(serviceList))
		for _, service := range serviceList {
			wg.Add(1)
			go func(ip string, port string, ipChan chan string, wg *sync.WaitGroup) {
				defer wg.Done()
				res, err := thirdparty.NewStorageRPCService().Locate(
					fmt.Sprintf("%s:%s", ip, bootstrap.NewConfig("").App.Port),
					&proto.ProxyReq{Uid: uidStr},
				)
				if err != nil || res == nil {
					lgLogger.Logger.Error(fmt.Sprintf("询问失败，详情：%s", err.Error()))
					return
				}
				ipChan <- res.Data
			}(service.IP, service.Port, ipChan, &wg)
		}
		wg.Wait()
		close(ipChan)
		for re := range ipChan {
			ipList = append(ipList, re)
		}
		if len(ipList) == 0 {
			lgLogger.Logger.Error("发现其他服务失败")
			st := status.Error(
				codes.Internal,
				"发现其他服务失败")
			return st
		}
		proxyIP := ipList[0]
		resp, err := thirdparty.NewStorageRPCService().UploadForward(
			fmt.Sprintf("%s:%s", proxyIP, bootstrap.NewConfig("").App.Port),
			true,
			in,
			stream,
		)
		if err != nil {
			lgLogger.Logger.Error(fmt.Sprintf("转发失败,%s", err.Error()))
			st := status.Error(
				codes.Internal,
				"转发失败")
			return st
		}
		return stream.SendAndClose(resp.(*proto.UploadSingleResp))
	}
	// 在本地
	fileName := path.Join(utils.LocalStore, uidStr, metaData.StorageName)
	out, err := os.Create(fileName)
	if err != nil {
		lgLogger.Logger.Error("本地创建文件失败")
		st := status.Error(
			codes.Internal,
			"本地创建文件失败")
		return st
	}
	fileBuf := bytes.NewReader(in.GetFile())
	for {
		// 流式写入文件
		for {
			buf := make([]byte, 1024)
			n, err := fileBuf.Read(buf)
			if err != nil && err != io.EOF {
				lgLogger.Logger.Error("接收数据到buffer失败")
				return status.Error(codes.Internal, err.Error())
			}
			if n == 0 {
				break
			}
			if _, err := out.Write(buf[:n]); err != nil {
				lgLogger.Logger.Error("请求数据存储到文件失败")
				return status.Error(codes.Internal, err.Error())
			}
		}
		// 从rpc流循环获取数据，获取一个数据帧
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			lgLogger.Logger.Error("流式接收数据失败")
			return status.Error(codes.Internal, err.Error())
		}
		fileBuf = bytes.NewReader(req.GetFile())
	}
	// 校验md5
	md5Str, err := base.CalculateFileMd5(fileName)
	if err != nil {
		lgLogger.Logger.Error(fmt.Sprintf("生成md5失败，详情%s", err.Error()))
		return status.Error(codes.Internal, err.Error())
	}
	if md5Str != md5 {
		return status.Error(codes.InvalidArgument,
			fmt.Sprintf("校验md5失败，计算结果:%s, 参数:%s", md5Str, md5))
	}
	// 上传到minio
	contentType, err := base.DetectContentType(fileName)
	if err != nil {
		lgLogger.Logger.Error("判断文件content-type失败")
		return status.Error(codes.Internal, "判断文件content-type失败")
	}
	if err := storage.NewStorage().Storage.PutObject(metaData.Bucket, metaData.StorageName, fileName, contentType); err != nil {
		lgLogger.Logger.Error("上传到minio失败")
		return status.Error(codes.Internal, "上传到minio失败")
	}
	// 更新元数据
	now := time.Now()
	fileInfo, _ := os.Stat(fileName)
	if err := repo.NewMetaDataInfoRepo().Updates(lgDB, metaData.UID, map[string]interface{}{
		"md5":          md5Str,
		"storage_size": fileInfo.Size(),
		"multi_part":   false,
		"status":       1,
		"updated_at":   &now,
		"content_type": contentType,
	}); err != nil {
		lgLogger.Logger.Error("上传完更新数据失败")
		return status.Error(codes.Internal, "上传完更新数据失败")
	}
	// 首次写入redis 元数据
	lgRedis := new(plugins.LangGoRedis).NewRedis()
	metaCache, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
	if err != nil {
		lgLogger.Logger.Error("上传数据，查询数据元信息失败")
		return status.Error(codes.Internal, "查询数据元信息失败")
	}
	b, err := json.Marshal(metaCache)
	if err != nil {
		lgLogger.Logger.Warn("上传数据，写入redis失败")
	}
	lgRedis.SetNX(context.Background(), fmt.Sprintf("%s-meta", uidStr), b, 5*60*time.Second)
	_ = out.Close()

	if err := os.RemoveAll(dirName); err != nil {
		lgLogger.Logger.Error(fmt.Sprintf("删除目录失败，详情%s", err.Error()))
		return status.Error(codes.Internal, fmt.Sprintf("删除目录失败，详情%s", err.Error()))
	}
	return stream.SendAndClose(&proto.UploadSingleResp{Code: 0, Message: "", Data: ""})
}

func (UploadServer) UploadSingleProxyHandler(stream proto.Upload_UploadSingleProxyHandlerServer) error {
	lgLogger := bootstrap.NewLogger()
	in, _ := stream.Recv()
	uidStr := in.GetUid()
	md5 := in.GetMd5()
	date := in.GetDate()
	expireStr := in.GetExpire()
	signature := in.GetSignature()

	uid, err, errorInfo := base.CheckValid(uidStr, date, expireStr)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			errorInfo)
		return st
	}

	if !base.CheckUploadSignature(date, expireStr, signature) {
		st := status.Error(
			codes.InvalidArgument,
			"签名校验失败")
		return st
	}

	// 判断记录是否存在
	lgDB := new(plugins.LangGoDB).Use("default").NewDB()
	metaData, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			"当前上传链接无效，uid不存在")
		return st
	}

	dirName := path.Join(utils.LocalStore, uidStr)
	// 判断是否上传过，md5
	resumeInfo, err := repo.NewMetaDataInfoRepo().GetResumeByMd5(lgDB, []string{md5})
	if err != nil {
		lgLogger.Logger.Error("上传单个文件，查询是否已上传失败")
		st := status.Error(
			codes.Internal,
			"内部异常")
		return st
	}
	if len(resumeInfo) != 0 {
		now := time.Now()
		if err := repo.NewMetaDataInfoRepo().Updates(lgDB, metaData.UID, map[string]interface{}{
			"bucket":       resumeInfo[0].Bucket,
			"storage_name": resumeInfo[0].StorageName,
			"address":      resumeInfo[0].Address,
			"md5":          md5,
			"storage_size": resumeInfo[0].StorageSize,
			"multi_part":   false,
			"status":       1,
			"updated_at":   &now,
			"content_type": resumeInfo[0].ContentType,
		}); err != nil {
			lgLogger.Logger.Error("上传完更新数据失败")
			st := status.Error(
				codes.Internal,
				"上传完更新数据失败")
			return st
		}
		if err := os.RemoveAll(dirName); err != nil {
			lgLogger.Logger.Error(fmt.Sprintf("删除目录失败，详情%s", err.Error()))
			st := status.Error(
				codes.Internal,
				fmt.Sprintf("删除目录失败，详情%s", err.Error()))
			return st
		}
		// 首次写入redis 元数据
		lgRedis := new(plugins.LangGoRedis).NewRedis()
		metaCache, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
		if err != nil {
			lgLogger.Logger.Error("上传数据，查询数据元信息失败")
			st := status.Error(
				codes.Internal,
				"查询数据元信息失败")
			return st
		}
		b, err := json.Marshal(metaCache)
		if err != nil {
			lgLogger.Logger.Warn("上传数据，写入redis失败")
		}
		lgRedis.SetNX(context.Background(), fmt.Sprintf("%s-meta", uidStr), b, 5*60*time.Second)
		return stream.SendAndClose(&proto.UploadSingleResp{Code: 0, Message: "", Data: ""})
	}
	// 判断是否在本地
	if _, err := os.Stat(dirName); !os.IsNotExist(err) {
		// 不在本地，询问集群内其他服务并转发
		serviceList, err := base.NewServiceRegister().Discovery()
		if err != nil || serviceList == nil {
			lgLogger.Logger.Error("发现其他服务失败")
			st := status.Error(
				codes.Internal,
				"发现其他服务失败")
			return st
		}
		var wg sync.WaitGroup
		var ipList []string
		ipChan := make(chan string, len(serviceList))
		for _, service := range serviceList {
			wg.Add(1)
			go func(ip string, port string, ipChan chan string, wg *sync.WaitGroup) {
				defer wg.Done()
				res, err := thirdparty.NewStorageRPCService().Locate(
					fmt.Sprintf("%s:%s", ip, bootstrap.NewConfig("").App.Port),
					&proto.ProxyReq{Uid: uidStr},
				)
				if err != nil || res == nil {
					lgLogger.Logger.Error(fmt.Sprintf("询问失败，详情：%s", err.Error()))
					return
				}
				ipChan <- res.Data
			}(service.IP, service.Port, ipChan, &wg)
		}
		wg.Wait()
		close(ipChan)
		for re := range ipChan {
			ipList = append(ipList, re)
		}
		if len(ipList) == 0 {
			lgLogger.Logger.Error("发现其他服务失败")
			st := status.Error(
				codes.Internal,
				"发现其他服务失败")
			return st
		}
		proxyIP := ipList[0]
		resp, err := thirdparty.NewStorageRPCService().UploadForward(
			fmt.Sprintf("%s:%s", proxyIP, bootstrap.NewConfig("").App.Port),
			true,
			in,
			stream,
		)

		if err != nil {
			lgLogger.Logger.Error(fmt.Sprintf("转发失败,%s", err.Error()))
			st := status.Error(
				codes.Internal,
				"转发失败")
			return st
		}
		return stream.SendAndClose(resp.(*proto.UploadSingleResp))
	}

	// 在本地
	fileName := path.Join(utils.LocalStore, uidStr, metaData.StorageName)
	out, err := os.Create(fileName)
	if err != nil {
		lgLogger.Logger.Error("本地创建文件失败")
		st := status.Error(
			codes.Internal,
			"本地创建文件失败")
		return st
	}
	fileBuf := bytes.NewReader(in.GetFile())
	for {
		// 流式写入文件
		for {
			buf := make([]byte, 1024)
			n, err := fileBuf.Read(buf)
			if err != nil && err != io.EOF {
				lgLogger.Logger.Error("接收数据到buffer失败")
				return status.Error(codes.Internal, err.Error())
			}
			if n == 0 {
				break
			}
			if _, err := out.Write(buf[:n]); err != nil {
				lgLogger.Logger.Error("请求数据存储到文件失败")
				return status.Error(codes.Internal, err.Error())
			}
		}
		// 从rpc流循环获取数据，获取一个数据帧
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			lgLogger.Logger.Error("流式接收数据失败")
			return status.Error(codes.Internal, err.Error())
		}
		fileBuf = bytes.NewReader(req.GetFile())
	}
	// 校验md5
	md5Str, err := base.CalculateFileMd5(fileName)
	if err != nil {
		lgLogger.Logger.Error(fmt.Sprintf("生成md5失败，详情%s", err.Error()))
		return status.Error(codes.Internal, err.Error())
	}
	if md5Str != md5 {
		return status.Error(codes.InvalidArgument,
			fmt.Sprintf("校验md5失败，计算结果:%s, 参数:%s", md5Str, md5))
	}
	// 上传到minio
	contentType, err := base.DetectContentType(fileName)
	if err != nil {
		lgLogger.Logger.Error("判断文件content-type失败")
		return status.Error(codes.Internal, "判断文件content-type失败")
	}
	if err := storage.NewStorage().Storage.PutObject(metaData.Bucket, metaData.StorageName, fileName, contentType); err != nil {
		lgLogger.Logger.Error("上传到minio失败")
		return status.Error(codes.Internal, "上传到minio失败")
	}
	// 更新元数据
	now := time.Now()
	fileInfo, _ := os.Stat(fileName)
	if err := repo.NewMetaDataInfoRepo().Updates(lgDB, metaData.UID, map[string]interface{}{
		"md5":          md5Str,
		"storage_size": fileInfo.Size(),
		"multi_part":   false,
		"status":       1,
		"updated_at":   &now,
		"content_type": contentType,
	}); err != nil {
		lgLogger.Logger.Error("上传完更新数据失败")
		return status.Error(codes.Internal, "上传完更新数据失败")
	}
	// 首次写入redis 元数据
	lgRedis := new(plugins.LangGoRedis).NewRedis()
	metaCache, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
	if err != nil {
		lgLogger.Logger.Error("上传数据，查询数据元信息失败")
		return status.Error(codes.Internal, "查询数据元信息失败")
	}
	b, err := json.Marshal(metaCache)
	if err != nil {
		lgLogger.Logger.Warn("上传数据，写入redis失败")
	}
	lgRedis.SetNX(context.Background(), fmt.Sprintf("%s-meta", uidStr), b, 5*60*time.Second)
	_ = out.Close()

	if err := os.RemoveAll(dirName); err != nil {
		lgLogger.Logger.Error(fmt.Sprintf("删除目录失败，详情%s", err.Error()))
		return status.Error(codes.Internal, fmt.Sprintf("删除目录失败，详情%s", err.Error()))
	}
	return stream.SendAndClose(&proto.UploadSingleResp{Code: 0, Message: "", Data: ""})
}

func (UploadServer) UploadMultiPartHandler(stream proto.Upload_UploadMultiPartHandlerServer) error {
	lgLogger := bootstrap.NewLogger()
	in, err := stream.Recv()
	if err != nil && err != io.EOF {
		st := status.Error(
			codes.NotFound,
			err.Error())
		return st
	}
	uidStr := in.GetUid()
	md5 := in.GetMd5()
	chunkNumStr := in.GetChunkNum()
	date := in.GetDate()
	expireStr := in.GetExpire()
	signature := in.GetSignature()

	uid, err, errorInfo := base.CheckValid(uidStr, date, expireStr)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			errorInfo)
		return st
	}

	chunkNum, err := strconv.ParseInt(chunkNumStr, 10, 64)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			"chunkNum参数有误")
		return st
	}

	if !base.CheckUploadSignature(date, expireStr, signature) {
		st := status.Error(
			codes.InvalidArgument,
			"签名校验失败")
		return st
	}

	// 判断记录是否存在
	lgDB := new(plugins.LangGoDB).Use("default").NewDB()
	metaData, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			"当前上传链接无效，uid不存在")
		return st
	}
	// 判断当前分片是否已上传
	var lgRedis = new(plugins.LangGoRedis).NewRedis()
	ctx := context.Background()
	createLock := base.NewRedisLock(&ctx, lgRedis, fmt.Sprintf("multi-part-%d-%d-%s", uid, chunkNum, md5))
	if flag, err := createLock.Acquire(); err != nil || !flag {
		lgLogger.Logger.Error("多文件上传，抢锁失败")
		st := status.Error(
			codes.Internal,
			"抢锁失败")
		return st
	}
	partInfo, err := repo.NewMultiPartInfoRepo().GetPartInfo(lgDB, uid, chunkNum, md5)
	if err != nil {
		lgLogger.Logger.Error("多文件上传，查询分片数据失败")
		st := status.Error(
			codes.Internal,
			"内部异常")
		return st
	}
	if len(partInfo) != 0 {
		return stream.SendAndClose(&proto.UploadMultiResp{Code: 0, Message: "", Data: ""})
	}
	_, _ = createLock.Release()

	// 判断是否在本地
	dirName := path.Join(utils.LocalStore, uidStr)
	if _, err := os.Stat(dirName); os.IsNotExist(err) {
		// 不在本地，询问集群内其他服务并转发
		serviceList, err := base.NewServiceRegister().Discovery()
		if err != nil || serviceList == nil {
			lgLogger.Logger.Error("发现其他服务失败")
			st := status.Error(
				codes.Internal,
				"发现其他服务失败")
			return st
		}
		var wg sync.WaitGroup
		var ipList []string
		ipChan := make(chan string, len(serviceList))
		for _, service := range serviceList {
			wg.Add(1)
			go func(ip string, port string, ipChan chan string, wg *sync.WaitGroup) {
				defer wg.Done()
				res, err := thirdparty.NewStorageRPCService().Locate(
					fmt.Sprintf("%s:%s", ip, bootstrap.NewConfig("").App.Port),
					&proto.ProxyReq{
						Uid: uidStr,
					},
				)
				if err != nil || res == nil {
					lgLogger.Logger.Error(fmt.Sprintf("询问失败，详情：%s", err.Error()))
					return
				}
				ipChan <- res.Data
			}(service.IP, service.Port, ipChan, &wg)
		}
		wg.Wait()
		close(ipChan)
		for re := range ipChan {
			ipList = append(ipList, re)
		}
		if len(ipList) == 0 {
			lgLogger.Logger.Error("发现其他服务失败")
			st := status.Error(
				codes.Internal,
				"发现其他服务失败")
			return st
		}
		proxyIP := ipList[0]
		resp, err := thirdparty.NewStorageRPCService().UploadForward(
			fmt.Sprintf("%s:%s", proxyIP, bootstrap.NewConfig("").App.Port),
			false,
			in,
			stream,
		)
		if err != nil {
			lgLogger.Logger.Error(fmt.Sprintf("转发失败，详情：%s", err.Error()))
			st := status.Error(
				codes.Internal,
				"转发失败")
			return st
		}
		return stream.SendAndClose(resp.(*proto.UploadMultiResp))
	}

	// 在本地
	fileName := path.Join(utils.LocalStore, uidStr, fmt.Sprintf("%d_%d", uid, chunkNum))
	out, err := os.Create(fileName)
	if err != nil {
		lgLogger.Logger.Error("本地创建文件失败")
		st := status.Error(
			codes.Internal,
			"本地创建文件失败")
		return st
	}
	defer func(out *os.File) {
		_ = out.Close()
	}(out)
	fileBuf := bytes.NewReader(in.GetFile())
	for {
		// 流式写入文件
		for {
			buf := make([]byte, 1024)
			n, err := fileBuf.Read(buf)
			if err != nil && err != io.EOF {
				lgLogger.Logger.Error("接收数据到buffer失败")
				return status.Error(codes.Internal, err.Error())
			}
			if n == 0 {
				break
			}
			if _, err := out.Write(buf[:n]); err != nil {
				lgLogger.Logger.Error("请求数据存储到文件失败")
				return status.Error(codes.Internal, err.Error())
			}
		}
		// 从rpc流循环获取数据，获取一个数据帧
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			lgLogger.Logger.Error("流式接收数据失败")
			return status.Error(codes.Internal, err.Error())
		}
		fileBuf = bytes.NewReader(req.GetFile())
	}

	// 校验md5
	md5Str, err := base.CalculateFileMd5(fileName)
	if err != nil {
		lgLogger.Logger.Error(fmt.Sprintf("生成md5失败，详情%s", err.Error()))
		return status.Error(codes.Internal, err.Error())
	}
	if md5Str != md5 {
		return status.Error(codes.InvalidArgument,
			fmt.Sprintf("校验md5失败，计算结果:%s, 参数:%s", md5Str, md5))
	}
	// 上传到minio
	contentType := "application/octet-stream"
	if err := storage.NewStorage().Storage.PutObject(metaData.Bucket, fmt.Sprintf("%d_%d", uid, chunkNum),
		fileName, contentType); err != nil {
		lgLogger.Logger.Error("上传到minio失败")
		return status.Error(codes.Internal, "上传到minio失败")
	}

	// 创建元数据
	now := time.Now()
	fileInfo, _ := os.Stat(fileName)
	if err := repo.NewMultiPartInfoRepo().Create(lgDB, &models.MultiPartInfo{
		StorageUid:   uid,
		ChunkNum:     int(chunkNum),
		Bucket:       metaData.Bucket,
		StorageName:  fmt.Sprintf("%d_%d", uid, chunkNum),
		StorageSize:  fileInfo.Size(),
		PartFileName: fmt.Sprintf("%d_%d", uid, chunkNum),
		PartMd5:      md5Str,
		Status:       1,
		CreatedAt:    &now,
		UpdatedAt:    &now,
	}); err != nil {
		lgLogger.Logger.Error("多文件上传，上传完更新数据失败")
		return status.Error(codes.Internal, "上传完更新数据失败")
	}
	return stream.SendAndClose(&proto.UploadMultiResp{Code: 0, Message: "", Data: ""})
}

func (UploadServer) UploadMergeHandler(ctx context.Context, in *proto.UploadMergeReq) (*proto.UploadMergeResp, error) {
	lgLogger := bootstrap.NewLogger()
	uidStr := in.GetUid()
	md5 := in.GetMd5()
	numStr := in.GetNum()
	size := in.GetSize()
	date := in.GetDate()
	expireStr := in.GetExpire()
	signature := in.GetSignature()

	uid, err, errorInfo := base.CheckValid(uidStr, date, expireStr)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			errorInfo)
		return nil, st
	}

	num, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			"num参数有误")
		return nil, st
	}

	if !base.CheckUploadSignature(date, expireStr, signature) {
		st := status.Error(
			codes.InvalidArgument,
			"签名校验失败")
		return nil, st
	}

	// 判断记录是否存在
	lgDB := new(plugins.LangGoDB).Use("default").NewDB()
	metaData, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			"当前合并链接无效，uid不存在")
		return nil, st
	}

	// 判断分片数量是否一致
	var multiPartInfoList []models.MultiPartInfo
	if err := lgDB.Model(&models.MultiPartInfo{}).Where(
		"storage_uid = ? and status = ?", uid, 1).Order("chunk_num ASC").Find(&multiPartInfoList).Error; err != nil {
		lgLogger.Logger.Error("合并数据，查询分片数据失败")
		st := status.Error(
			codes.Internal,
			"查询分片数据失败")
		return nil, st
	}
	if num != int64(len(multiPartInfoList)) {
		st := status.Error(
			codes.InvalidArgument,
			"分片数量和整体数量不一致")
		return nil, st
	}

	// 判断是否在本地
	dirName := path.Join(utils.LocalStore, uidStr)
	if _, err := os.Stat(dirName); os.IsNotExist(err) {
		// 不在本地，询问集群内其他服务并转发
		serviceList, err := base.NewServiceRegister().Discovery()
		if err != nil || serviceList == nil {
			lgLogger.Logger.Error("发现其他服务失败")
			st := status.Error(
				codes.Internal,
				"发现其他服务失败")
			return nil, st
		}
		var wg sync.WaitGroup
		var ipList []string
		ipChan := make(chan string, len(serviceList))
		for _, service := range serviceList {
			wg.Add(1)
			go func(ip string, port string, ipChan chan string, wg *sync.WaitGroup) {
				defer wg.Done()
				res, err := thirdparty.NewStorageRPCService().Locate(
					fmt.Sprintf("%s:%s", ip, bootstrap.NewConfig("").App.Port),
					&proto.ProxyReq{
						Uid: uidStr,
					},
				)
				if err != nil {
					lgLogger.Logger.Error(fmt.Sprintf("询问失败，详情：%s", err.Error()))
					return
				}
				ipChan <- res.Data
			}(service.IP, service.Port, ipChan, &wg)
		}
		wg.Wait()
		close(ipChan)
		for re := range ipChan {
			ipList = append(ipList, re)
		}
		if len(ipList) == 0 {
			lgLogger.Logger.Error("发现其他服务失败")
			st := status.Error(
				codes.Internal,
				"发现其他服务失败")
			return nil, st
		}
		proxyIP := ipList[0]
		resp, err := thirdparty.NewStorageRPCService().MergeForward(
			fmt.Sprintf("%s:%s", proxyIP, bootstrap.NewConfig("").App.Port),
			in,
		)
		if err != nil {
			lgLogger.Logger.Error(fmt.Sprintf("转发失败，详情：%s", err.Error()))
			st := status.Error(
				codes.Internal,
				"转发失败")
			return nil, st
		}
		return resp, nil
	}

	// 获取文件的content-type
	firstPart := multiPartInfoList[0]
	partName := path.Join(utils.LocalStore, fmt.Sprintf("%d", uid), firstPart.PartFileName)
	contentType, err := base.DetectContentType(partName)
	if err != nil {
		lgLogger.Logger.Error("判断文件content-type失败")
		st := status.Error(
			codes.Internal,
			"判断文件content-type失败")
		return nil, st
	}

	// 更新metadata的数据
	now := time.Now()
	if err := repo.NewMetaDataInfoRepo().Updates(lgDB, metaData.UID, map[string]interface{}{
		"part_num":     int(num),
		"md5":          md5,
		"storage_size": size,
		"multi_part":   true,
		"status":       1,
		"updated_at":   &now,
		"content_type": contentType,
	}); err != nil {
		lgLogger.Logger.Error("上传完更新数据失败")
		st := status.Error(
			codes.Internal,
			"上传完更新数据失败")
		return nil, st
	}
	// 创建合并任务
	msg := models.MergeInfo{
		StorageUid: uid,
		ChunkSum:   num,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		lgLogger.Logger.Error("消息struct转成json字符串失败", zap.Any("err", err.Error()))
		st := status.Error(
			codes.Internal,
			"创建合并任务失败")
		return nil, st
	}
	newModelTask := models.TaskInfo{
		Status:    utils.TaskStatusUndo,
		TaskType:  utils.TaskPartMerge,
		ExtraData: string(b),
	}
	if err := repo.NewTaskRepo().Create(lgDB, &newModelTask); err != nil {
		lgLogger.Logger.Error("创建合并任务失败", zap.Any("err", err.Error()))
		st := status.Error(
			codes.Internal,
			"创建合并任务失败")
		return nil, st
	}
	// 首次写入redis 元数据和分片信息
	lgRedis := new(plugins.LangGoRedis).NewRedis()
	metaCache, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
	if err != nil {
		lgLogger.Logger.Error("上传数据，查询数据元信息失败")
		st := status.Error(
			codes.Internal,
			"内部异常")
		return nil, st
	}
	b, err = json.Marshal(metaCache)
	if err != nil {
		lgLogger.Logger.Warn("上传数据，写入redis失败")
	}
	lgRedis.SetNX(context.Background(), fmt.Sprintf("%s-meta", uidStr), b, 5*60*time.Second)

	var multiPartInfoListCache []models.MultiPartInfo
	if err := lgDB.Model(&models.MultiPartInfo{}).Where(
		"storage_uid = ? and status = ?", uid, 1).Order("chunk_num ASC").Find(&multiPartInfoListCache).Error; err != nil {
		lgLogger.Logger.Error("上传数据，查询分片数据失败")
		st := status.Error(
			codes.Internal,
			"查询分片数据失败")
		return nil, st
	}
	// 写入redis
	b, err = json.Marshal(multiPartInfoListCache)
	if err != nil {
		lgLogger.Logger.Warn("上传数据，写入redis失败")
	}
	lgRedis.SetNX(context.Background(), fmt.Sprintf("%s-multiPart", uidStr), b, 5*60*time.Second)

	return &proto.UploadMergeResp{
		Code:    0,
		Message: "",
		Data:    "",
	}, nil
}
