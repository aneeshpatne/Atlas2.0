package grpcserver

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func UnaryInterceptor(logger *slog.Logger, defaultTimeout time.Duration) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		if _, ok := ctx.Deadline(); !ok && defaultTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
			defer cancel()
		}
		started := time.Now()
		requestID := ""
		if values := metadata.ValueFromIncomingContext(ctx, "x-request-id"); len(values) > 0 {
			requestID = values[0]
		}
		if requestID == "" {
			requestID = operationID()
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("grpc panic recovered", "method", info.FullMethod, "request_id", requestID, "panic", recovered, "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal server error")
			}
			logger.Info("grpc request completed", "method", info.FullMethod, "request_id", requestID, "code", status.Code(err).String(), "duration", time.Since(started))
		}()
		return handler(ctx, req)
	}
}
