package log

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func GrpcLogger(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler, logger *zap.Logger) (resp interface{}, err error) {
	startTime := time.Now()
	result, err := handler(ctx, req)
	duration := time.Since(startTime)

	statusCode := codes.Unknown
	if st, ok := status.FromError(err); ok {
		statusCode = st.Code()
	}

	if err != nil {
		logger.Error("grpc logger error",
			zap.String("method", info.FullMethod),
			zap.Duration("duration", duration),
			zap.Int("status_code", int(statusCode)),
			zap.String("status_text", statusCode.String()),
			zap.Error(err))
	} else {
		logger.Info("received a gRPC request",
			zap.String("method", info.FullMethod),
			zap.Int("status_code", int(statusCode)),
			zap.String("status_text", statusCode.String()),
			zap.Duration("duration", duration),
		)
	}

	return result, err
}

type ResponseRecorder struct {
	http.ResponseWriter
	StatusCode int
	Body       []byte
}

func (rec *ResponseRecorder) WriteHeader(statusCode int) {
	rec.StatusCode = statusCode
	rec.ResponseWriter.WriteHeader(statusCode)
}

func (rec *ResponseRecorder) Write(body []byte) (int, error) {
	rec.Body = body
	return rec.ResponseWriter.Write(body)
}

func HttpLogger(handler http.Handler, logger *zap.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		rec := &ResponseRecorder{
			ResponseWriter: w,
			StatusCode:     http.StatusOK,
		}
		handler.ServeHTTP(rec, r)
		duration := time.Since(startTime)

		logger.Info("received a HTTP request",
			zap.String("protocol", "HTTP"),
			zap.String("method", r.Method),
			zap.String("path", r.RequestURI),
			zap.Int("status_code", rec.StatusCode),
			zap.String("status_text", http.StatusText(rec.StatusCode)),
			zap.Duration("duration", duration))
	})
}
