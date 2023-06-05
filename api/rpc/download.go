package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-redis/redis/v8"
	"github.com/qinguoyi/ObjectStorageProxy/app/models"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/base"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/repo"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/storage"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/utils"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap"
	"github.com/qinguoyi/ObjectStorageProxy/bootstrap/plugins"
	"github.com/qinguoyi/ObjectStorageProxy/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"time"
)

/*
对象下载
*/

// DownloadServer .
type DownloadServer struct {
	proto.UnimplementedDownloadServer
}

// DownloadHandler .
func (DownloadServer) DownloadHandler(in *proto.DownloadReq, stream proto.Download_DownloadHandlerServer) error {
	lgLogger := bootstrap.NewLogger()
	online := "1"
	if in.Online == "" {
		online = "1"
	}
	if !utils.Contains(online, []string{"0", "1"}) {
		st := status.Error(
			codes.InvalidArgument,
			"online参数有误")
		return st
	}

	uid, err, errorInfo := base.CheckValid(in.Uid, in.Date, in.Expire)
	if err != nil {
		st := status.Error(
			codes.InvalidArgument,
			fmt.Sprintf("参数校验失败，详情:%s", errorInfo))
		return st
	}
	if !base.CheckDownloadSignature(in.Date, in.Expire, in.Bucket, in.Object, in.Signature) {
		st := status.Error(
			codes.InvalidArgument,
			"签名校验失败")
		return st
	}
	// 判断是分片数据还是整个文件
	var meta *models.MetaDataInfo
	lgRedis := new(plugins.LangGoRedis).NewRedis()
	val, err := lgRedis.Get(context.Background(), fmt.Sprintf("%s-meta", in.Uid)).Result()
	// key在redis中不存在
	if err == redis.Nil {
		lgDB := new(plugins.LangGoDB).Use("default").NewDB()
		meta, err = repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
		if err != nil {
			lgLogger.Logger.Error("下载数据，查询数据元信息失败")
			st := status.Error(
				codes.Internal,
				"内部异常")
			return st
		}
		// 写入redis
		lgRedis := new(plugins.LangGoRedis).NewRedis()
		b, err := json.Marshal(meta)
		if err != nil {
			lgLogger.Logger.Warn("数据元信息写入redis失败")
		}
		lgRedis.SetNX(context.Background(), fmt.Sprintf("%s-meta", in.Uid), b, 5*60*time.Second)
	} else {
		if err != nil {
			lgLogger.Logger.Error("下载数据，查询redis失败")
			st := status.Error(
				codes.Internal,
				"内部异常")
			return st
		}
		var msg models.MetaDataInfo
		if err := json.Unmarshal([]byte(val), &msg); err != nil {
			lgLogger.Logger.Error("下载数据，查询redis结果，序列化失败")
			st := status.Error(
				codes.Internal,
				"内部异常")
			return st
		}
		meta = &msg
	}

	bucketName := meta.Bucket
	objectName := meta.StorageName
	start := int64(0)
	fileSize := meta.StorageSize
	end := fileSize - 1
	ch := make(chan []byte, 1024*1024*20)
	if !meta.MultiPart {
		go func() {
			step := int64(1 * 1024 * 1024)
			for {
				if start >= end {
					close(ch)
					break
				}
				length := step
				if start+length > end {
					length = end - start + 1
				}
				data, err := storage.NewStorage().Storage.GetObject(bucketName, objectName, start, length)
				if err != nil && err != io.EOF {
					lgLogger.Logger.Error(fmt.Sprintf("从minio获取数据失败%s", err.Error()))
				}
				ch <- data
				start += step
			}
		}()
	} else {
		// 分片数据传输
		var multiPartInfoList []models.MultiPartInfo
		val, err := lgRedis.Get(context.Background(), fmt.Sprintf("%s-multiPart", in.Uid)).Result()
		// key在redis中不存在
		if err == redis.Nil {
			lgDB := new(plugins.LangGoDB).Use("default").NewDB()
			if err := lgDB.Model(&models.MultiPartInfo{}).Where(
				"storage_uid = ? and status = ?", uid, 1).Order("chunk_num ASC").Find(&multiPartInfoList).Error; err != nil {
				lgLogger.Logger.Error("下载数据，查询分片数据失败")
				st := status.Error(
					codes.Internal,
					"内部异常")
				return st
			}
			// 写入redis
			lgRedis := new(plugins.LangGoRedis).NewRedis()
			b, err := json.Marshal(multiPartInfoList)
			if err != nil {
				lgLogger.Logger.Warn("数据分片信息写入redis失败")
			}
			lgRedis.SetNX(context.Background(), fmt.Sprintf("%s-multiPart", in.Uid), b, 5*60*time.Second)
		} else {
			if err != nil {
				lgLogger.Logger.Error("下载数据，查询redis失败")
				st := status.Error(
					codes.Internal,
					"内部异常")
				return st
			}
			var msg []models.MultiPartInfo
			if err := json.Unmarshal([]byte(val), &msg); err != nil {
				lgLogger.Logger.Error("下载数据，查询reids，结果序列化失败")
				st := status.Error(
					codes.Internal,
					"内部异常")
				return st
			}
			multiPartInfoList = msg
		}
		if meta.PartNum != len(multiPartInfoList) {
			lgLogger.Logger.Error("分片数量和整体数量不一致")
			st := status.Error(
				codes.Internal,
				"分片数量和整体数量不一致")
			return st
		}

		// 查找起始分片
		index, totalSize := int64(0), int64(0)
		var startP, lengthP int64
		for {
			if totalSize >= start {
				startP, lengthP = 0, multiPartInfoList[index].StorageSize
			} else {
				if totalSize+multiPartInfoList[index].StorageSize > start {
					startP, lengthP = start-totalSize, multiPartInfoList[index].StorageSize-(start-totalSize)
				} else {
					totalSize += multiPartInfoList[index].StorageSize
					index++
					continue
				}
			}
			break
		}
		var chanSlice []chan int
		for i := 0; i < utils.MultiPartDownload; i++ {
			chanSlice = append(chanSlice, make(chan int, 1))
		}

		chanSlice[0] <- 1
		j := 0
		for i := 0; i < utils.MultiPartDownload; i++ {
			go func(i int, startP_, lengthP_ int64) {
				for {
					// 当前块计算完后，需要等待前一个块合并到主哈希
					<-chanSlice[i]

					if index >= int64(meta.PartNum) {
						close(ch)
						break
					}
					if totalSize >= start {
						startP_, lengthP_ = 0, multiPartInfoList[index].StorageSize
					}
					totalSize += multiPartInfoList[index].StorageSize

					data, err := storage.NewStorage().Storage.GetObject(
						multiPartInfoList[index].Bucket,
						multiPartInfoList[index].StorageName,
						startP_,
						lengthP_,
					)
					if err != nil && err != io.EOF {
						lgLogger.Logger.Error(fmt.Sprintf("从minio获取数据失败%s", err.Error()))
					}
					// 合并到主哈希
					ch <- data
					index++
					// 这里要注意适配chanSlice的长度
					if j == utils.MultiPartDownload-1 {
						j = 0
					} else {
						j++
					}
					chanSlice[j] <- 1
				}
			}(i, startP, lengthP)
		}
	}
	for {
		// Read a chunk from the chan
		data, ok := <-ch
		if !ok {
			break
		}
		// Send the chunk to the client
		if err := stream.Send(&proto.DownloadResp{Data: data}); err != nil {
			lgLogger.Logger.Error(fmt.Sprintf("发送stream出错，详情:%s", err.Error()))
			return err
		}
	}
	return nil
}
