package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/base"
	"github.com/qinguoyi/ObjectStorageProxy/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"
)

func GetClientConn(ctx context.Context, target string, opts []grpc.DialOption) (*grpc.ClientConn, error) {
	creds, err := credentials.NewClientTLSFromFile("../conf/certs/server.pem", "object-storage-proxy")
	if err != nil {
		log.Println("Failed to create TLS credentials %v", err)
		return nil, errors.New("")
	}
	return grpc.Dial(target, grpc.WithTransportCredentials(creds))

	//opts = append(opts, grpc.WithInsecure())
	//return grpc.DialContext(ctx, target, opts...)
}

func min(a, b int64) int64 {
	if a <= b {
		return a
	} else {
		return b
	}
}

func main() {
	baseUrl := "124.222.198.8:443"
	uploadFilePath := "./xxx.jpg"
	uploadFile := filepath.Base(uploadFilePath)

	ctx := context.Background()
	clientConn, _ := GetClientConn(ctx, baseUrl, nil)
	defer clientConn.Close()

	// ##################### 获取上传连接 ######################
	fmt.Println("获取上传连接")
	linkClient := proto.NewLinkClient(clientConn)
	linkResp, _ := linkClient.UploadLinkHandler(ctx,
		&proto.GenUploadReq{
			FilePath: []string{fmt.Sprintf("%s", uploadFile)},
			Expire:   86400,
		})
	log.Printf("resp: %v", linkResp)

	// ##################### 上传文件 ######################
	// +++++++ 单文件 ++++++
	fmt.Println("上传文件")
	filePath := uploadFilePath
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println("Failed to open file:", err)
		return
	}
	defer file.Close()
	md5Str, _ := base.CalculateFileMd5(filePath)

	fileInfo, _ := os.Stat(filePath)
	fileSize := fileInfo.Size()

	uploadClient := proto.NewUploadClient(clientConn)

	// 本地读取文件数据到chan
	ch := make(chan []byte, 4096)
	if fileSize < 1024*1024*1 {
		uploadSingleClient, _ := uploadClient.UploadSingleProxyHandler(ctx)
		go func() {
			defer close(ch)
			for {
				// buf如果在外层声明，则会造成并发问题，发送的数据会乱序
				buf := make([]byte, 1024)
				n, err := file.Read(buf)
				if err != nil && err != io.EOF {
					fmt.Println("Failed to read file:", err)
					return
				}
				if n == 0 {
					break
				}
				ch <- buf[:n]
			}
		}()
		// 客户端从chan读取数据发送到服务端
		for {
			data, ok := <-ch
			if !ok {
				break
			}
			req := proto.UploadSingleReq{
				Uid:       linkResp.Data[0].Uid,
				File:      data,
				Md5:       md5Str,
				Date:      linkResp.Data[0].Date,
				Expire:    linkResp.Data[0].Expire,
				Signature: linkResp.Data[0].Signature,
			}
			_ = uploadSingleClient.Send(&req)
		}

		_, err = uploadSingleClient.CloseAndRecv()
		if err != nil {
			log.Fatalf("RouteList get response err: %v", err)
		}
	} else {
		// +++++++ 多文件 ++++++
		// 分片上传
		chunkSize := 1024.0 * 1024
		currentChunk := int64(1)
		totalChunk := int64(math.Ceil(float64(fileSize) / chunkSize))
		var wg sync.WaitGroup
		ch := make(chan struct{}, 5)
		for currentChunk <= totalChunk {

			start := (currentChunk - 1) * int64(chunkSize)
			end := min(fileSize, start+int64(chunkSize))
			fmt.Println(currentChunk, totalChunk, end-start)
			buffer := make([]byte, end-start)
			n, err := file.Read(buffer)
			if err != nil && err != io.EOF {
				fmt.Println("读取文件长度失败", err)
				break
			}
			fmt.Println("当前read长度", n)
			md5Part, _ := base.CalculateByteMd5(buffer)

			// 多协程上传
			ch <- struct{}{}
			wg.Add(1)
			go func(data []byte, md5V string, chunkNum int64, wg *sync.WaitGroup) {
				defer wg.Done()
				uploadMultiClient, _ := uploadClient.UploadMultiPartHandler(ctx)
				req := proto.UploadMultiReq{
					Uid:       linkResp.Data[0].Uid,
					File:      data,
					Md5:       md5V,
					Date:      linkResp.Data[0].Date,
					Expire:    linkResp.Data[0].Expire,
					Signature: linkResp.Data[0].Signature,
					ChunkNum:  fmt.Sprintf("%d", chunkNum),
				}
				err = uploadMultiClient.Send(&req)
				if err != nil && err != io.EOF {
					fmt.Println("分片上传失败", err)
					panic(err)
				}
				multiResp, err := uploadMultiClient.CloseAndRecv()
				if err != nil {
					fmt.Println("分片上传失败", err)
					//panic(err)
				} else {
					fmt.Println("分片上传成功", multiResp)
				}

				<-ch
			}(buffer, md5Part, currentChunk, &wg)
			currentChunk += 1
		}
		fmt.Println("等待")
		wg.Wait()

		// 合并文件
		resp, err := uploadClient.UploadMergeHandler(ctx, &proto.UploadMergeReq{
			Uid:       linkResp.Data[0].Uid,
			Md5:       md5Str,
			Date:      linkResp.Data[0].Date,
			Expire:    linkResp.Data[0].Expire,
			Signature: linkResp.Data[0].Signature,
			Num:       fmt.Sprintf("%d", totalChunk),
			Size:      fmt.Sprintf("%d", fileSize),
		})
		if err != nil {
			fmt.Println("合并失败", err)
			panic(err)
		}
		fmt.Println("合并成功", resp)
	}

	// ##################### 获取下载链接 ######################
	fmt.Println("获取下载链接")

	downLinkResp, _ := linkClient.DownloadLinkHandler(ctx,
		&proto.GenDownloadReq{
			Uid:    []string{linkResp.Data[0].Uid},
			Expire: 86400,
		})
	// ##################### 下载文件 ######################
	fmt.Println("下载文件", downLinkResp.Data[0].Url)

	downloadClient := proto.NewDownloadClient(clientConn)
	stream, err := downloadClient.DownloadHandler(ctx, &proto.DownloadReq{
		Uid:       downLinkResp.Data[0].Uid,
		Name:      downLinkResp.Data[0].Meta.SrcName,
		Online:    "0",
		Date:      downLinkResp.Data[0].Date,
		Expire:    downLinkResp.Data[0].Expire,
		Bucket:    downLinkResp.Data[0].Bucket,
		Object:    downLinkResp.Data[0].Object,
		Signature: downLinkResp.Data[0].Signature,
	})
	if err != nil {
		log.Fatalf("download get response err: %v", err)
	}
	// 创建本地文件并将数据流写入本地文件
	LocalFile := downLinkResp.Data[0].Meta.DstName
	filePoint, err := os.Create(LocalFile)
	if err != nil {
		log.Fatalf("failed to create local file: %v", err)
	}
	defer filePoint.Close()
	for {
		// 从服务器返回的数据流中读取数据
		fileData, err := stream.Recv()
		if err == io.EOF {
			break
		}
		fmt.Println("从服务端获取数据", len(fileData.Data))

		if err != nil {
			log.Fatalf("failed to receive file data: %v", err)
		}

		// 将数据写入本地文件
		_, err = filePoint.Write(fileData.GetData())
		if err != nil {
			log.Fatalf("failed to write file data: %v", err)
		}
	}
	// 计算md5
	md5New, _ := base.CalculateFileMd5(LocalFile)
	if md5New == md5Str {
		fmt.Println("single test whole process.")
	} else {
		fmt.Println("测试失败", md5New, md5Str)
	}
	_ = filePoint.Close()
	_ = os.Remove(LocalFile)
}
