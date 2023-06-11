package http1

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
	"io"
	"net/http"
	"time"
)

// DownloadHandler .
func DownloadHandler(w http.ResponseWriter, r *http.Request) {
	lgLogger := bootstrap.NewLogger()
	defer func() {
		if e := recover(); e != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("内部异常"))
			return
		}
	}()
	query := r.URL.Query()
	uidStr := base.Query(query, "uid")
	name := base.Query(query, "name")
	online := base.Query(query, "online")
	date := base.Query(query, "date")
	expireStr := base.Query(query, "expire")
	bucketName := base.Query(query, "bucket")
	objectName := base.Query(query, "object")
	signature := base.Query(query, "signature")

	if online == "" {
		online = "1"
	}
	if !utils.Contains(online, []string{"0", "1"}) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("online参数有误"))
		return
	}

	uid, err, errorInfo := base.CheckValid(uidStr, date, expireStr)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(fmt.Sprintf("参数校验失败，详情:%s", errorInfo)))
		return
	}
	if !base.CheckDownloadSignature(date, expireStr, bucketName, objectName, signature) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("签名校验失败"))
		return
	}
	var meta *models.MetaDataInfo
	lgRedis := new(plugins.LangGoRedis).NewRedis()
	val, err := lgRedis.Get(context.Background(), fmt.Sprintf("%s-meta", uidStr)).Result()
	// key在redis中不存在
	if err == redis.Nil {
		lgDB := new(plugins.LangGoDB).Use("default").NewDB()
		meta, err = repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
		if err != nil {
			lgLogger.Logger.Error("下载数据，查询数据元信息失败")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("内部异常"))
			return
		}
		// 写入redis
		lgRedis := new(plugins.LangGoRedis).NewRedis()
		b, err := json.Marshal(meta)
		if err != nil {
			lgLogger.Logger.Warn("数据元信息写入redis失败")
		}
		lgRedis.SetNX(context.Background(), fmt.Sprintf("%s-meta", uidStr), b, 5*60*time.Second)
	} else {
		if err != nil {
			lgLogger.Logger.Error("下载数据，查询redis失败")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("内部异常"))
			return
		}
		var msg models.MetaDataInfo
		if err := json.Unmarshal([]byte(val), &msg); err != nil {
			lgLogger.Logger.Error("下载数据，查询redis结果，序列化失败")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("内部异常"))
			return
		}
		// 续期
		lgRedis.Expire(context.Background(), fmt.Sprintf("%s-meta", uidStr), 5*60*time.Second)
		meta = &msg
	}

	bucketName = meta.Bucket
	objectName = meta.StorageName
	fileSize := meta.StorageSize
	start, end := base.GetRange(r.Header.Get("Range"), fileSize)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
	if online == "0" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", name))
	} else {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%s", name))
	}
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
	w.Header().Set("Accept-Ranges", "bytes")
	if start == fileSize {
		w.WriteHeader(http.StatusOK)
		return
	}
	if end == fileSize-1 {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusPartialContent)
	}

	ch := make(chan []byte, 1024*1024*20)
	// 判断是否在本地

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
					lgLogger.Logger.Error(fmt.Sprintf("从对象存储获取数据失败%s", err.Error()))
				}
				ch <- data
				start += step
			}
		}()

		// 这种场景，会先从minio中获取全部数据，再流式传输，所以下载前会等待一下，但会把内存打爆
		//go func() {
		//	data, err := inner.NewStorage().Storage.GetObject(bucketName, objectName, start, end-start+1)
		//	if err != nil && err != io.EOF {
		//		lgLogger.WithContext(c).Error(fmt.Sprintf("从minio获取数据失败%s", err.Error()))
		//	}
		//	ch <- data
		//	close(ch)
		//}()

	} else {
		// 分片数据传输
		var multiPartInfoList []models.MultiPartInfo
		val, err := lgRedis.Get(context.Background(), fmt.Sprintf("%s-multiPart", uidStr)).Result()
		// key在redis中不存在
		if err == redis.Nil {
			lgDB := new(plugins.LangGoDB).Use("default").NewDB()
			if err := lgDB.Model(&models.MultiPartInfo{}).Where(
				"storage_uid = ? and status = ?", uid, 1).Order("chunk_num ASC").Find(&multiPartInfoList).Error; err != nil {
				lgLogger.Logger.Error("下载数据，查询分片数据失败")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("内部异常"))
				return
			}
			// 写入redis
			lgRedis := new(plugins.LangGoRedis).NewRedis()
			b, err := json.Marshal(multiPartInfoList)
			if err != nil {
				lgLogger.Logger.Warn("数据分片信息写入redis失败")
			}
			lgRedis.SetNX(context.Background(), fmt.Sprintf("%s-multiPart", uidStr), b, 5*60*time.Second)
		} else {
			if err != nil {
				lgLogger.Logger.Error("下载数据，查询redis失败")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("内部异常"))
				return
			}
			var msg []models.MultiPartInfo
			if err := json.Unmarshal([]byte(val), &msg); err != nil {
				lgLogger.Logger.Error("下载数据，查询reids，结果序列化失败")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("内部异常"))
				return
			}
			// 续期
			lgRedis.Expire(context.Background(), fmt.Sprintf("%s-multiPart", uidStr), 5*60*time.Second)
			multiPartInfoList = msg
		}
		if meta.PartNum != len(multiPartInfoList) {
			lgLogger.Logger.Error("分片数量和整体数量不一致")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("分片数量和整体数量不一致"))
			return
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
						lgLogger.Logger.Error(fmt.Sprintf("从对象存储获取数据失败%s", err.Error()))
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

	clientGone := w.(http.CloseNotifier).CloseNotify()
	step := func(w io.Writer) bool {
		defer func() {
			if err := recover(); err != nil {
				lgLogger.Logger.Error(fmt.Sprintf("stream流式响应出错，%s", err))
			}
		}()
		data, ok := <-ch
		if !ok {
			return false
		}
		_, err := w.Write(data)
		if err != nil {
			lgLogger.Logger.Error(fmt.Sprintf("写入http响应出错，%s", err.Error()))
			return false
		}
		return true
	}
	for {
		select {
		case <-clientGone:
			lgLogger.Logger.Error("发送stream出错1")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("内部异常"))
			return
		default:
			keepOpen := step(w)
			w.(http.Flusher).Flush()
			if !keepOpen {
				return
			}
		}
	}
}
