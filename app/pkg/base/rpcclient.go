package base

import (
	"context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"log"
)

func NewRPCClient(target string) *grpc.ClientConn {
	ctx := context.Background()
	creds, err := credentials.NewClientTLSFromFile("/storage/conf/certs/server.pem", "object-storage-proxy")
	if err != nil {
		log.Println("Failed to create TLS credentials %v", err)
		return nil
	}
	var opts []grpc.DialOption
	// 超时控制
	opts = append(opts,
		//grpc.WithChainUnaryInterceptor(
		//	client.UnaryContextTimeout(),
		//),
		//grpc.WithChainStreamInterceptor(
		//	client.StreamContextTimeout(),
		//),
		//grpc.WithInsecure(),
		grpc.WithTransportCredentials(creds),
	)
	//opts = append(opts)
	conn, _ := grpc.DialContext(ctx, target, opts...)
	return conn
}
