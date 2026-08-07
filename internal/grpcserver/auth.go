package grpcserver

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// BearerAuthInterceptor requires an exact bearer token on every unary RPC.
func BearerAuthInterceptor(configuredToken string) grpc.UnaryServerInterceptor {
	want := strings.TrimSpace(configuredToken)
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		values := metadata.ValueFromIncomingContext(ctx, "authorization")
		if want == "" || len(values) != 1 || !validBearerToken(values[0], want) {
			return nil, status.Error(codes.Unauthenticated, "valid bearer token required")
		}
		return handler(ctx, req)
	}
}

func validBearerToken(header, token string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return len(got) == len(token) && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
