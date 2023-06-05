package client

import (
	"context"
	"google.golang.org/grpc"
)

// TODO: stream超时拦截器
// TODO: 日志打印，增加request-id
func StreamContextTimeout() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx, cancel := defaultContextTimeout(ctx)
		if cancel != nil {
			defer cancel()
		}

		return streamer(ctx, desc, cc, method, opts...)
	}
}
