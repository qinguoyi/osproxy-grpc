package http1

import (
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
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"sync"
	"time"
)

/*上传和下载的http接口*/

func UploadSingleHandler(w http.ResponseWriter, r *http.Request) {
	lgLogger := bootstrap.NewLogger()
	if r.Method != "PUT" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("当前路径只支持PUT方法"))
		return
	}
	defer func() {
		if e := recover(); e != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("内部异常"))
			return
		}
	}()
	query := r.URL.Query()
	uidStr := base.Query(query, "uid")
	md5 := base.Query(query, "md5")
	date := base.Query(query, "date")
	expireStr := base.Query(query, "expire")
	signature := base.Query(query, "signature")

	uid, err, errorInfo := base.CheckValid(uidStr, date, expireStr)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(errorInfo))
		return
	}

	if !base.CheckUploadSignature(date, expireStr, signature) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("签名校验失败"))
		return
	}

	f, file, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("文件参数错误"))
		return
	}
	defer f.Close()

	// 判断记录是否存在
	lgDB := new(plugins.LangGoDB).Use("default").NewDB()
	metaData, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("当前上传链接无效，uid不存在"))
		return
	}

	dirName := path.Join(utils.LocalStore, uidStr)
	// 判断是否上传过，md5
	resumeInfo, err := repo.NewMetaDataInfoRepo().GetResumeByMd5(lgDB, []string{md5})
	if err != nil {
		lgLogger.Logger.Error("上传单个文件，查询是否已上传失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("内部异常"))
		return
	}

	if len(resumeInfo) != 0 {
		now := time.Now()
		if err := repo.NewMetaDataInfoRepo().Updates(lgDB, metaData.UID, map[string]interface{}{
			"md5":          md5,
			"storage_size": resumeInfo[0].StorageSize,
			"multi_part":   false,
			"status":       1,
			"updated_at":   &now,
			"content_type": resumeInfo[0].ContentType,
		}); err != nil {
			lgLogger.Logger.Error("上传完更新数据失败")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("上传完更新数据失败"))
			return
		}
		if err := os.RemoveAll(dirName); err != nil {
			lgLogger.Logger.Error(fmt.Sprintf("删除目录失败，详情%s", err.Error()))
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(fmt.Sprintf("删除目录失败，详情%s", err.Error())))
			return
		}

		// 首次写入redis 元数据
		lgRedis := new(plugins.LangGoRedis).NewRedis()
		metaCache, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
		if err != nil {
			lgLogger.Logger.Error("上传数据，查询数据元信息失败")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("上传数据，查询数据元信息失败"))
			return
		}
		b, err := json.Marshal(metaCache)
		if err != nil {
			lgLogger.Logger.Warn("上传数据，写入redis失败")
		}
		lgRedis.SetNX(context.Background(), fmt.Sprintf("%s-meta", uidStr), b, 5*60*time.Second)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0, "message":"", "data": ""}`))
		return
	}
	// 判断是否在本地
	if _, err := os.Stat(dirName); os.IsNotExist(err) {
		// 不在本地，询问集群内其他服务并转发
		serviceList, err := base.NewServiceRegister().Discovery()
		if err != nil || serviceList == nil {
			lgLogger.Logger.Error("发现其他服务失败")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("发现其他服务失败"))
			return
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
					lgLogger.Logger.Error(fmt.Sprintf("转发失败,%s", err.Error()))
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
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("发现其他服务失败"))
			return
		}
		proxyIP := ipList[0]
		_, err = thirdparty.NewStorageRPCService().UploadWebForward(
			fmt.Sprintf("%s:%s", proxyIP, bootstrap.NewConfig("").App.Port),
			true,
			r,
		)
		if err != nil {
			lgLogger.Logger.Error(fmt.Sprintf("转发失败,%s", err.Error()))
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("转发失败"))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0, "message":"", "data": ""}`))
		return
	}

	// 在本地
	fileName := path.Join(utils.LocalStore, uidStr, metaData.StorageName)
	out, err := os.Create(fileName)
	if err != nil {
		lgLogger.Logger.Error("本地创建文件失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("本地创建文件失败"))
		return
	}
	src, err := file.Open()
	if err != nil {
		lgLogger.Logger.Error("打开本地文件失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("打开本地文件失败"))
		return
	}
	if _, err = io.Copy(out, src); err != nil {
		lgLogger.Logger.Error("请求数据存储到文件失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("请求数据存储到文件失败"))
		return
	}
	// 校验md5
	md5Str, err := base.CalculateFileMd5(fileName)
	if err != nil {
		lgLogger.Logger.Error(fmt.Sprintf("生成md5失败，详情%s", err.Error()))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	if md5Str != md5 {
		lgLogger.Logger.Error(fmt.Sprintf("校验md5失败，计算结果:%s, 参数:%s", md5Str, md5))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(fmt.Sprintf("校验md5失败，计算结果:%s, 参数:%s", md5Str, md5)))
		return
	}
	// 上传到minio
	contentType, err := base.DetectContentType(fileName)
	if err != nil {
		lgLogger.Logger.Error("判断文件content-type失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("判断文件content-type失败"))
		return
	}
	if err := storage.NewStorage().Storage.PutObject(metaData.Bucket, metaData.StorageName, fileName, contentType); err != nil {
		lgLogger.Logger.Error("上传到minio失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("上传到minio失败"))
		return
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
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("上传完更新数据失败"))
		return
	}
	_, _ = out.Close(), src.Close()
	// 首次写入redis 元数据
	lgRedis := new(plugins.LangGoRedis).NewRedis()
	metaCache, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
	if err != nil {
		lgLogger.Logger.Error("上传数据，查询数据元信息失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("内部异常"))
		return
	}
	b, err := json.Marshal(metaCache)
	if err != nil {
		lgLogger.Logger.Warn("上传数据，写入redis失败")
	}
	lgRedis.SetNX(context.Background(), fmt.Sprintf("%s-meta", uidStr), b, 5*60*time.Second)
	if err := os.RemoveAll(dirName); err != nil {
		lgLogger.Logger.Error(fmt.Sprintf("删除目录失败，详情%s", err.Error()))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(fmt.Sprintf("删除目录失败，详情%s", err.Error())))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"code":0, "message":"", "data": ""}`))
	return
}

func UploadMultiPartHandler(w http.ResponseWriter, r *http.Request) {
	lgLogger := bootstrap.NewLogger()
	if r.Method != "PUT" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("当前路径只支持PUT方法"))
		return
	}
	defer func() {
		if e := recover(); e != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("内部异常"))
			return
		}
	}()
	query := r.URL.Query()
	uidStr := base.Query(query, "uid")
	md5 := base.Query(query, "md5")
	date := base.Query(query, "date")
	expireStr := base.Query(query, "expire")
	signature := base.Query(query, "signature")
	chunkNumStr := base.Query(query, "chunkNum")

	uid, err, errorInfo := base.CheckValid(uidStr, date, expireStr)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(errorInfo))
		return
	}

	chunkNum, err := strconv.ParseInt(chunkNumStr, 10, 64)
	if err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(errorInfo))
		return
	}

	if !base.CheckUploadSignature(date, expireStr, signature) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("签名校验失败"))
		return
	}

	f, file, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("文件参数错误"))
		return
	}
	defer f.Close()

	// 判断记录是否存在
	lgDB := new(plugins.LangGoDB).Use("default").NewDB()
	metaData, err := repo.NewMetaDataInfoRepo().GetByUid(lgDB, uid)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("当前上传链接无效，uid不存在"))
		return
	}
	// 判断当前分片是否已上传
	var lgRedis = new(plugins.LangGoRedis).NewRedis()
	ctx := context.Background()
	createLock := base.NewRedisLock(&ctx, lgRedis, fmt.Sprintf("multi-part-%d-%d-%s", uid, chunkNum, md5))
	if flag, err := createLock.Acquire(); err != nil || !flag {
		lgLogger.Logger.Error("多文件上传，抢锁失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("抢锁失败"))
		return
	}
	partInfo, err := repo.NewMultiPartInfoRepo().GetPartInfo(lgDB, uid, chunkNum, md5)
	if err != nil {
		lgLogger.Logger.Error("多文件上传，查询分片数据失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("内部异常"))
		return
	}
	if len(partInfo) != 0 {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0, "message":"", "data": ""}`))
		return
	}
	_, _ = createLock.Release()

	// 判断是否在本地
	dirName := path.Join(utils.LocalStore, uidStr)
	if _, err := os.Stat(dirName); os.IsNotExist(err) {
		// 不在本地，询问集群内其他服务并转发
		serviceList, err := base.NewServiceRegister().Discovery()
		if err != nil || serviceList == nil {
			lgLogger.Logger.Error("发现其他服务失败")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("发现其他服务失败"))
			return
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
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("发现其他服务失败"))
			return
		}
		proxyIP := ipList[0]
		_, err = thirdparty.NewStorageRPCService().UploadWebForward(
			fmt.Sprintf("%s:%s", proxyIP, bootstrap.NewConfig("").App.Port),
			false,
			r,
		)
		if err != nil {
			lgLogger.Logger.Error(fmt.Sprintf("转发失败，详情：%s", err.Error()))
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("转发失败"))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0, "message":"", "data": ""}`))
		return
	}

	// 在本地
	fileName := path.Join(utils.LocalStore, uidStr, fmt.Sprintf("%d_%d", uid, chunkNum))
	out, err := os.Create(fileName)
	if err != nil {
		lgLogger.Logger.Error("本地创建文件失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("本地创建文件失败"))
		return
	}
	src, err := file.Open()
	if err != nil {
		lgLogger.Logger.Error("打开本地文件失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("打开本地文件失败"))
		return
	}
	if _, err = io.Copy(out, src); err != nil {
		lgLogger.Logger.Error("请求数据存储到文件失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("请求数据存储到文件失败"))
		return
	}
	// 校验md5
	md5Str, err := base.CalculateFileMd5(fileName)
	if err != nil {
		lgLogger.Logger.Error(fmt.Sprintf("生成md5失败，详情%s", err.Error()))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	if md5Str != md5 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(fmt.Sprintf("校验md5失败，计算结果:%s, 参数:%s", md5Str, md5)))
		return
	}
	// 上传到minio
	contentType := "application/octet-stream"
	if err := storage.NewStorage().Storage.PutObject(metaData.Bucket, fmt.Sprintf("%d_%d", uid, chunkNum),
		fileName, contentType); err != nil {
		lgLogger.Logger.Error("上传到minio失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("上传到minio失败"))
		return
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
		lgLogger.Logger.Error("上传完更新数据失败")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("上传完更新数据失败"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"code":0, "message":"", "data": ""}`))
	return
}
