package thirdparty

import (
	"context"
	"fmt"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/base"
	"github.com/qinguoyi/ObjectStorageProxy/proto"
	"google.golang.org/grpc"
	"io"
	"net/http"
)

// https://www.cnblogs.com/voipman/p/15352001.html

type storageRPCService struct{}

// NewStorageRPCService .
func NewStorageRPCService() *storageRPCService { return &storageRPCService{} }

// Locate .
func (s *storageRPCService) Locate(target string, req *proto.ProxyReq) (*proto.ProxyResp, error) {
	clientConn := base.NewRPCClient(target)
	client := proto.NewProxyClient(clientConn)
	resp, err := client.ProxyHandler(context.Background(), req)
	return resp, err
}

// UploadForward . 基于原始ServerStream和ClientStream
func (s *storageRPCService) UploadForward(target string, single bool, cacheReq interface{}, stream grpc.ServerStream) (interface{}, error) {
	fmt.Println("forward...")
	clientConn := base.NewRPCClient(target)
	client := proto.NewUploadClient(clientConn)
	var uploadClient grpc.ClientStream
	var err error
	if single {
		uploadClient, err = client.UploadSingleHandler(context.Background())
		if err != nil {
			return nil, err
		}
	} else {
		uploadClient, err = client.UploadMultiPartHandler(context.Background())
		if err != nil {
			return nil, err
		}
	}
	fmt.Println("forward send before...")

	// 如果有部分数据已接收，需要先转发
	if cacheReq != nil {
		err = uploadClient.SendMsg(cacheReq)
		if err != nil && err != io.EOF {
			return nil, err
		}
	}
	fmt.Println("forward send after...")

	// 这里没有用send和closeandrecv方法，因为grpc.ClientStream的数据没有这两个方法
	for {
		var req interface{}
		if single {
			req = new(proto.UploadSingleReq)
		} else {
			req = new(proto.UploadMultiReq)
		}
		fmt.Println("proxy recv before...", err)
		err := stream.RecvMsg(req)
		fmt.Println("proxy recv after...", err)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		err = uploadClient.SendMsg(req)
		if err != nil && err != io.EOF {
			return nil, err
		}
	}

	if err := uploadClient.CloseSend(); err != nil {
		return nil, err
	}
	var resp interface{}
	if single {
		resp = new(proto.UploadSingleResp)
	} else {
		resp = new(proto.UploadMultiResp)
	}
	if err := uploadClient.RecvMsg(resp); err != nil {
		fmt.Println("client recv ...", err)
		return nil, err
	}
	return resp, err
}

// UploadWebForward .
func (s *storageRPCService) UploadWebForward(target string, single bool, r *http.Request) (interface{}, error) {
	fmt.Println("forward...")
	clientConn := base.NewRPCClient(target)
	client := proto.NewUploadClient(clientConn)
	var uploadClient grpc.ClientStream
	var err error
	if single {
		uploadClient, err = client.UploadSingleHandler(context.Background())
		if err != nil {
			return nil, err
		}
	} else {
		uploadClient, err = client.UploadMultiPartHandler(context.Background())
		if err != nil {
			return nil, err
		}
	}
	fmt.Println("forward send before...")
	query := r.URL.Query()
	uidStr := base.Query(query, "uid")
	md5 := base.Query(query, "md5")
	date := base.Query(query, "date")
	expireStr := base.Query(query, "expire")
	signature := base.Query(query, "signature")
	chunkNumStr := base.Query(query, "chunkNum")
	file, _, _ := r.FormFile("file")
	defer file.Close()

	// 这里没有用send和closeandrecv方法，因为grpc.ClientStream的数据没有这两个方法
	for {
		buffer := make([]byte, 1024*1024)
		bytesRead, err := file.Read(buffer)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		var req interface{}
		if single {
			req = &proto.UploadSingleReq{
				Uid:       uidStr,
				File:      buffer[:bytesRead],
				Md5:       md5,
				Date:      date,
				Expire:    expireStr,
				Signature: signature,
			}
		} else {
			req = &proto.UploadMultiReq{
				Uid:       uidStr,
				File:      buffer[:bytesRead],
				Md5:       md5,
				Date:      date,
				Expire:    expireStr,
				Signature: signature,
				ChunkNum:  chunkNumStr,
			}
		}
		err = uploadClient.SendMsg(req)
		if err != nil && err != io.EOF {
			return nil, err
		}
	}

	if err := uploadClient.CloseSend(); err != nil {
		return nil, err
	}
	var resp interface{}
	if single {
		resp = new(proto.UploadSingleResp)
	} else {
		resp = new(proto.UploadMultiResp)
	}
	if err := uploadClient.RecvMsg(resp); err != nil {
		fmt.Println("client recv ...", err)
		return nil, err
	}
	return resp, err
}

// MergeForward .
func (s *storageRPCService) MergeForward(target string, req *proto.UploadMergeReq) (*proto.UploadMergeResp, error) {
	clientConn := base.NewRPCClient(target)
	client := proto.NewUploadClient(clientConn)
	resp, err := client.UploadMergeHandler(context.Background(), req)
	return resp, err
}

// DownloadForward .
func (s *storageRPCService) DownloadForward(target string, req *proto.DownloadReq) (proto.Download_DownloadHandlerClient, error) {
	clientConn := base.NewRPCClient(target)
	client := proto.NewDownloadClient(clientConn)
	stream, err := client.DownloadHandler(context.Background(),
		&proto.DownloadReq{
			Uid:       req.Uid,
			Name:      req.Name,
			Online:    req.Online,
			Date:      req.Date,
			Expire:    req.Expire,
			Bucket:    req.Bucket,
			Object:    req.Object,
			Signature: req.Signature,
		})
	return stream, err
}

// DownloadWebForward .
func (s *storageRPCService) DownloadWebForward(target string, r *http.Request) (proto.Download_DownloadHandlerClient, error) {
	clientConn := base.NewRPCClient(target)
	client := proto.NewDownloadClient(clientConn)
	query := r.URL.Query()
	stream, err := client.DownloadHandler(
		context.Background(),
		&proto.DownloadReq{
			Uid:       base.Query(query, "uid"),
			Name:      base.Query(query, "name"),
			Online:    base.Query(query, "online"),
			Date:      base.Query(query, "date"),
			Expire:    base.Query(query, "expire"),
			Bucket:    base.Query(query, "bucket"),
			Object:    base.Query(query, "object"),
			Signature: base.Query(query, "signature"),
		})
	return stream, err
}
