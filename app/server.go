package app

import (
	"context"
	assetfs "github.com/elazarl/go-bindata-assetfs"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/qinguoyi/ObjectStorageProxy/api/http1"
	"github.com/qinguoyi/ObjectStorageProxy/api/rpc"
	"github.com/qinguoyi/ObjectStorageProxy/app/middleware/server"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/base"
	"github.com/qinguoyi/ObjectStorageProxy/app/pkg/event/dispatch"
	"github.com/qinguoyi/ObjectStorageProxy/config"
	"github.com/qinguoyi/ObjectStorageProxy/docs/swagger"
	"github.com/qinguoyi/ObjectStorageProxy/proto"
	"github.com/rs/cors"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"
)

var (
	CertName    string = "object-storage-proxy"
	CertPemPath string = "conf/certs/server.pem"
	CertKeyPath string = "conf/certs/server.key"
)

func runGrpcServer() *grpc.Server {
	// 注册服务端拦截器
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			server.Recovery,
			server.AccessLog,
		),
		grpc.ChainStreamInterceptor(
			server.LoggingInterceptor,
		),
	}

	// grpc server
	creds, err := credentials.NewServerTLSFromFile(CertPemPath, CertKeyPath)
	if err != nil {
		log.Printf("Failed to create server TLS credentials %v", err)
	}
	opts = append(opts, grpc.Creds(creds))
	// 创建gRPC server对象
	s := grpc.NewServer(opts...)
	// 注册service到server
	proto.RegisterHealthCheckServer(s, &rpc.HealthCheckServer{})
	proto.RegisterGreeterServer(s, &rpc.GreeterServer{})
	proto.RegisterLinkServer(s, &rpc.LinkServer{})
	proto.RegisterResumeServer(s, &rpc.ResumeServer{})
	proto.RegisterCheckPointServer(s, &rpc.CheckPointServer{})
	proto.RegisterDownloadServer(s, &rpc.DownloadServer{})
	proto.RegisterUploadServer(s, &rpc.UploadServer{})
	proto.RegisterProxyServer(s, &rpc.ProxyServer{})
	// 启动gRPC Server
	log.Println("Serving gRPC on 0.0.0.0:8888")
	return s
}

func runHttpServer() *http.ServeMux {

	r := http.NewServeMux()
	prefix := "/swagger-ui/"
	fileServer := http.FileServer(&assetfs.AssetFS{
		Asset:    swagger.Asset,
		AssetDir: swagger.AssetDir,
		Prefix:   "docs/swagger-ui",
	})
	r.Handle(prefix, http.StripPrefix(prefix, fileServer))

	r.HandleFunc("/swagger/", func(w http.ResponseWriter, r *http.Request) {
		// 判断后缀
		if !strings.HasSuffix(r.URL.Path, "swagger.json") {
			http.NotFound(w, r)
			return
		}
		// 判断前缀
		p := strings.TrimPrefix(r.URL.Path, "/swagger/")
		p = path.Join("proto", p)

		http.ServeFile(w, r, p)
	})
	r.HandleFunc("/api/storage/rpc/download", http1.DownloadHandler)
	r.HandleFunc("/api/storage/rpc/upload", http1.UploadSingleHandler)
	r.HandleFunc("/api/storage/rpc/upload/multi", http1.UploadMultiPartHandler)
	return r
}
func runGrpcGateWayServer(port string) *runtime.ServeMux {
	// gRPC-Gateway mux
	ctx := context.Background()
	dCreds, err := credentials.NewClientTLSFromFile(CertPemPath, CertName)
	if err != nil {
		log.Printf("Failed to create client TLS credentials %v", err)
	}
	dOps := []grpc.DialOption{grpc.WithTransportCredentials(dCreds)}
	gwMux := runtime.NewServeMux()
	endPoint := "0.0.0.0:" + port
	// 注册endpoint到mux
	_ = proto.RegisterGreeterHandlerFromEndpoint(ctx, gwMux, endPoint, dOps)
	_ = proto.RegisterHealthCheckHandlerFromEndpoint(ctx, gwMux, endPoint, dOps)
	_ = proto.RegisterLinkHandlerFromEndpoint(ctx, gwMux, endPoint, dOps)
	_ = proto.RegisterResumeHandlerFromEndpoint(ctx, gwMux, endPoint, dOps)
	_ = proto.RegisterCheckPointHandlerFromEndpoint(ctx, gwMux, endPoint, dOps)
	_ = proto.RegisterDownloadHandlerFromEndpoint(ctx, gwMux, endPoint, dOps)
	_ = proto.RegisterUploadHandlerFromEndpoint(ctx, gwMux, endPoint, dOps)
	_ = proto.RegisterProxyHandlerFromEndpoint(ctx, gwMux, endPoint, dOps)
	return gwMux
}

// grpcHandlerFunc 将gRPC请求和HTTP请求分别调用不同的handler处理。
func grpcHandlerFunc(grpcServer *grpc.Server, otherHandler http.Handler) http.Handler {
	if otherHandler == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			grpcServer.ServeHTTP(w, r)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.Contains(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
		} else {
			otherHandler.ServeHTTP(w, r)
		}
	})
}

// grpcH2cHandlerFunc 不需要 TLS 建立安全链接
func grpcH2cHandlerFunc(grpcServer *grpc.Server, otherHandler http.Handler) http.Handler {
	return h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.Contains(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
		} else {
			otherHandler.ServeHTTP(w, r)
		}
	}), &http2.Server{})
}

// RunServer .
func RunServer(lgConfig *config.Configuration) {
	httpMux := runHttpServer()
	grpcServer := runGrpcServer()
	gatewayMux := runGrpcGateWayServer(lgConfig.App.Port)

	httpMux.Handle("/", gatewayMux)
	// 配置CORS
	withCors := cors.New(cors.Options{
		AllowOriginFunc:  func(origin string) bool { return true },
		AllowedMethods:   []string{"*"},
		AllowedHeaders:   []string{"*"},
		ExposedHeaders:   []string{"*"},
		AllowCredentials: true,
		MaxAge:           300,
	}).Handler(httpMux)
	tlsConfig := base.GetTLSConfig(CertPemPath, CertKeyPath)
	// 定义HTTP server配置
	gwServer := &http.Server{
		Addr:      "0.0.0.0:" + lgConfig.App.Port,
		Handler:   grpcHandlerFunc(grpcServer, withCors), // 请求的统一入口
		TLSConfig: tlsConfig,
	}
	// 启动HTTP服务
	go func() {
		if err := gwServer.ListenAndServeTLS(CertPemPath, CertKeyPath); err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()
	go base.NewServiceRegister().HeartBeat()

	// 启动 任务
	p, consumers := dispatch.RunTask()

	// 等待中断信号以优雅地关闭应用
	quit := make(chan os.Signal)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	// 关闭任务
	dispatch.StopTask(p, consumers)

	// 设置 5 秒的超时时间
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 关闭应用
	if err := gwServer.Shutdown(ctx); err != nil {
		panic(err)
	}
}
