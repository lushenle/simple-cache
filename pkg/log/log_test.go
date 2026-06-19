package log

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// testWriter is a helper to capture log output
type testWriter struct {
	bytes.Buffer
}

func (tw *testWriter) Sync() error {
	return nil // No-op for buffer
}

func TestNewLogger(t *testing.T) {
	plugin := NewStdoutPlugin(zapcore.DebugLevel)
	logger := NewLogger(plugin)
	assert.NotNil(t, logger)

	// Test with options
	loggerWithOptions := NewLogger(plugin, zap.Fields(zap.String("test_field", "test_value")))
	assert.NotNil(t, loggerWithOptions)
	// Further checks would require inspecting the logger's core or output, which is complex for a unit test.
}

func TestNewPlugin(t *testing.T) {
	var buf testWriter
	plugin := NewPlugin(&buf, zapcore.InfoLevel)
	assert.NotNil(t, plugin)

	logger := NewLogger(plugin)
	logger.Info("test info message")
	logger.Debug("test debug message") // Should not be logged

	output := buf.String()
	assert.Contains(t, output, "test info message")
	assert.NotContains(t, output, "test debug message")
}

func TestNewStdoutPlugin(t *testing.T) {
	// Hard to test stdout directly, but we can check if it creates a plugin
	plugin := NewStdoutPlugin(zapcore.DebugLevel)
	assert.NotNil(t, plugin)
}

func TestNewStderrPlugin(t *testing.T) {
	// Hard to test stderr directly, but we can check if it creates a plugin
	plugin := NewStderrPlugin(zapcore.ErrorLevel)
	assert.NotNil(t, plugin)
}

func TestNewFilePlugin(t *testing.T) {
	tempFile, err := os.CreateTemp("", "logtest_*.log")
	require.NoError(t, err)
	filePath := tempFile.Name()
	defer os.Remove(filePath)
	tempFile.Close() // Close it so the logger can write

	plugin, closer := NewFilePlugin(filePath, zapcore.InfoLevel)
	assert.NotNil(t, plugin)
	require.NotNil(t, closer)
	defer closer.Close()

	logger := NewLogger(plugin)
	logger.Info("hello from file logger")

	// Re-open and read the file to check contents
	// Need to ensure logger flushes, zap usually does this on close or with Sync
	logger.Sync() // Ensure logs are written

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "hello from file logger")
}

func TestDefaultEncoder(t *testing.T) {
	encoder := DefaultEncoder()
	assert.NotNil(t, encoder)
	// Type assertion to check it's a JSON encoder
	_, ok := encoder.(zapcore.PrimitiveArrayEncoder) // JSONEncoder embeds this
	assert.True(t, ok, "DefaultEncoder should return a JSON encoder")
}

func TestDefaultOption(t *testing.T) {
	opts := DefaultOption()
	assert.Len(t, opts, 2) // AddCaller and AddStacktrace
}

func TestDefaultLumberjackLogger(t *testing.T) {
	l := DefaultLumberjackLogger()
	assert.NotNil(t, l)
	// Check some default values if they are exposed or important
	// For example, if common.MaxLogFileSize was accessible and set
}

func TestGrpcLogger_Success(t *testing.T) {
	var buf testWriter
	plugin := NewPlugin(&buf, zapcore.InfoLevel)
	logger := NewLogger(plugin)

	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "response", nil
	}

	_, err := GrpcLogger(context.Background(), "request", info, handler, logger)
	assert.NoError(t, err)

	logOutput := buf.String()
	assert.Contains(t, logOutput, "received a gRPC request")
	assert.Contains(t, logOutput, info.FullMethod)
	assert.Contains(t, logOutput, fmt.Sprintf("\"status_code\":%d", codes.OK))
	assert.Contains(t, logOutput, fmt.Sprintf("\"status_text\":\"%s\"", codes.OK.String()))
}

func TestGrpcLogger_Error(t *testing.T) {
	var buf testWriter
	plugin := NewPlugin(&buf, zapcore.InfoLevel)
	logger := NewLogger(plugin)

	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethodError"}
	expectedErr := status.Error(codes.InvalidArgument, "invalid argument")
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, expectedErr
	}

	_, err := GrpcLogger(context.Background(), "request", info, handler, logger)
	assert.Error(t, err)
	assert.Equal(t, expectedErr, err)

	logOutput := buf.String()
	assert.Contains(t, logOutput, "grpc logger error")
	assert.Contains(t, logOutput, info.FullMethod)
	assert.Contains(t, logOutput, fmt.Sprintf("\"status_code\":%d", codes.InvalidArgument))
	assert.Contains(t, logOutput, fmt.Sprintf("\"status_text\":\"%s\"", codes.InvalidArgument.String()))
	assert.Contains(t, logOutput, "invalid argument")
}

func TestHttpLogger(t *testing.T) {
	var buf testWriter
	plugin := NewPlugin(&buf, zapcore.InfoLevel)
	logger := NewLogger(plugin)

	baseHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/error" {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal error"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	loggedHandler := HttpLogger(baseHandler, logger)

	t.Run("Success", func(t *testing.T) {
		buf.Reset()
		req := httptest.NewRequest("GET", "/success", nil)
		rr := httptest.NewRecorder()
		loggedHandler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "ok", rr.Body.String())

		logOutput := buf.String()
		assert.Contains(t, logOutput, "received a HTTP request")
		assert.Contains(t, logOutput, "GET")
		assert.Contains(t, logOutput, "/success")
		assert.Contains(t, logOutput, fmt.Sprintf("\"status_code\":%d", http.StatusOK))
		assert.Contains(t, logOutput, fmt.Sprintf("\"status_text\":\"%s\"", http.StatusText(http.StatusOK)))
	})

	t.Run("Error", func(t *testing.T) {
		buf.Reset()
		req := httptest.NewRequest("POST", "/error?param=1", nil)
		rr := httptest.NewRecorder()
		loggedHandler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
		assert.Equal(t, "internal error", rr.Body.String())

		logOutput := buf.String()
		assert.Contains(t, logOutput, "received a HTTP request") // Still logs as info, but with error status
		assert.Contains(t, logOutput, "POST")
		assert.Contains(t, logOutput, "/error?param=1")
		assert.Contains(t, logOutput, fmt.Sprintf("\"status_code\":%d", http.StatusInternalServerError))
		assert.Contains(t, logOutput, fmt.Sprintf("\"status_text\":\"%s\"", http.StatusText(http.StatusInternalServerError)))
	})
}

func TestResponseRecorder(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := &ResponseRecorder{
		ResponseWriter: rr,
		StatusCode:     http.StatusOK, // Default
	}

	t.Run("WriteHeader", func(t *testing.T) {
		rec.WriteHeader(http.StatusAccepted)
		assert.Equal(t, http.StatusAccepted, rec.StatusCode)
		assert.Equal(t, http.StatusAccepted, rr.Code)
	})

	t.Run("Write", func(t *testing.T) {
		body := []byte("test body")
		n, err := rec.Write(body)
		assert.NoError(t, err)
		assert.Equal(t, len(body), n)
		assert.Equal(t, body, rec.Body)
		assert.Equal(t, body, rr.Body.Bytes())
	})

	// Test initial status code if WriteHeader is not called before Write
	t.Run("WriteImplicitStatusOK", func(t *testing.T) {
		rrFresh := httptest.NewRecorder()
		recFresh := &ResponseRecorder{
			ResponseWriter: rrFresh,
			StatusCode:     http.StatusOK, // Explicitly set default for clarity
		}
		body := []byte("implicit ok")
		recFresh.Write(body)
		assert.Equal(t, http.StatusOK, recFresh.StatusCode) // Should remain OK
		assert.Equal(t, http.StatusOK, rrFresh.Code)        // httptest.Recorder defaults to 200 if WriteHeader not called
		assert.Equal(t, body, recFresh.Body)
		assert.Equal(t, body, rrFresh.Body.Bytes())
	})
}

// TestMain can be used for global setup/teardown if needed
// func TestMain(m *testing.M) {
// 	// setup
// 	code := m.Run()
// 	// teardown
// 	os.Exit(code)
// }
