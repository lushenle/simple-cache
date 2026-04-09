package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/config"
	"github.com/lushenle/simple-cache/pkg/log"
	"github.com/lushenle/simple-cache/pkg/metrics"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/lushenle/simple-cache/pkg/raft"
	"github.com/lushenle/simple-cache/pkg/server"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/cors"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

//go:embed swagger/*
var content embed.FS

func main() {
	// Initialize Prometheus metrics
	metrics.Init()

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())

	plugin := log.NewStdoutPlugin(zapcore.InfoLevel)
	logger := log.NewLogger(plugin)

	cfgPath := "config.yaml"
	if p := os.Getenv("CONFIG_PATH"); p != "" {
		cfgPath = p
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err), zap.String("path", cfgPath))
	}
	acfg := config.NewAtomic(cfg)
	stop := make(chan struct{})
	if cfg.HotReload {
		w := config.NewWatcher(cfgPath, acfg) // Fix: use cfgPath instead of hardcoded "config.yaml"
		go w.Start(stop)
	}

	metricsServer := &http.Server{Addr: cfg.MetricsAddr, Handler: metricsMux}
	go func() {
		logger.Info("starting metrics server", zap.String("addr", cfg.MetricsAddr))
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server failed", zap.Error(err))
		}
	}()

	// Create a new gRPC server
	c := cache.New(30*time.Second, logger)
	srv := server.New(c, cfg.NodeID)

	// Auto-load from dump file on startup
	if cfg.LoadOnStartup {
		defaultPath := cache.DefaultDumpPath(cfg.NodeID, cfg.DumpFormat, cfg.DataDir)
		if _, err := os.Stat(defaultPath); err == nil {
			result, err := c.Load(cfg.NodeID, defaultPath)
			if err != nil {
				logger.Warn("failed to load cache from dump file", zap.Error(err), zap.String("path", defaultPath))
			} else if result != nil && result.LoadedKeys > 0 {
				logger.Info("loaded cache from dump file",
					zap.String("path", result.Path),
					zap.Int32("loaded", result.LoadedKeys),
					zap.Int32("skipped", result.SkippedKeys),
				)
			}
		}
	}

	var raftNode *raft.Node
	if cfg.Mode == "distributed" {
		st := raft.NewStorage(filepath.Join(cfg.DataDir, "raft-"+cfg.NodeID+".wal"))
		raftNode = raft.NewNode(cfg.NodeID, cfg.RaftHTTPAddr, cfg.Peers, st, srv, time.Duration(cfg.HeartbeatMS)*time.Millisecond, time.Duration(cfg.ElectionMS)*time.Millisecond, logger)
		srv.UseRaft(raftNode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	waitGroup, ctx := errgroup.WithContext(ctx)
	waitGroup.Go(func() error {
		runGatewayServer(ctx, waitGroup, srv, logger, acfg)
		return nil
	})
	waitGroup.Go(func() error {
		<-ctx.Done()
		cancel()
		return nil
	})

	// Start the gRPC server
	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		logger.Fatal("failed to listen on gRPC address", zap.Error(err), zap.String("addr", cfg.GRPCAddr))
	}
	grpcServer := grpc.NewServer()
	pb.RegisterCacheServiceServer(grpcServer, srv)

	// Graceful shutdown on SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go grpcServer.Serve(lis)

	sig := <-sigCh
	logger.Info("received shutdown signal", zap.String("signal", sig.String()))

	// Graceful shutdown sequence
	logger.Info("shutting down gracefully...")

	// 1. Stop accepting new gRPC requests
	grpcServer.GracefulStop()

	// 2. Cancel context to stop HTTP gateway
	cancel()

	// 3. Wait for gateway to stop
	if err := waitGroup.Wait(); err != nil {
		logger.Error("gateway shutdown error", zap.Error(err))
	}

	// 4. Close raft node
	if raftNode != nil {
		raftNode.Close()
	}

	// 5. Auto-dump cache before closing
	if cfg.DumpOnShutdown {
		defaultPath := cache.DefaultDumpPath(cfg.NodeID, cfg.DumpFormat, cfg.DataDir)
		if result, err := c.Dump(cfg.NodeID, cfg.DumpFormat, defaultPath); err != nil {
			logger.Warn("failed to dump cache on shutdown", zap.Error(err))
		} else if result != nil {
			logger.Info("cache dumped on shutdown",
				zap.String("path", result.Path),
				zap.Int32("keys", result.TotalKeys),
			)
		}
	}

	// 6. Close cache
	c.Close()

	// 7. Stop config watcher
	close(stop)

	// 8. Stop metrics server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("metrics server shutdown error", zap.Error(err))
	}

	metrics.Close()
	logger.Info("shutdown complete")
}

func runGatewayServer(ctx context.Context, waitGroup *errgroup.Group, svr *server.CacheService, logger *zap.Logger, cfg *config.AtomicConfig) {
	jsonOption := runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
		MarshalOptions: protojson.MarshalOptions{
			UseProtoNames: true,
		},
		UnmarshalOptions: protojson.UnmarshalOptions{
			DiscardUnknown: true,
		},
	})

	grpcMux := runtime.NewServeMux(jsonOption)
	err := pb.RegisterCacheServiceHandlerServer(ctx, grpcMux, svr)
	if err != nil {
		logger.Fatal("failed to register gateway server", zap.Error(err))
		return
	}

	mux := http.NewServeMux()
	mux.Handle("/", grpcMux)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(svr.Role()))
	})

	mux.HandleFunc("/cluster/join", func(w http.ResponseWriter, r *http.Request) {
		type req struct {
			ID   string `json:"id"`
			Addr string `json:"addr"`
		}
		var in req
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("invalid request body"))
			return
		}
		if in.Addr == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("missing addr"))
			return
		}
		ok := svr.AddPeer(in.Addr)
		if !ok {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte("exists"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("joined"))
	})
	mux.HandleFunc("/cluster/leave", func(w http.ResponseWriter, r *http.Request) {
		type req struct {
			Addr string `json:"addr"`
		}
		var in req
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("invalid request body"))
			return
		}
		if in.Addr == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("missing addr"))
			return
		}
		ok := svr.RemovePeer(in.Addr)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("not found"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("left"))
	})
	mux.HandleFunc("/cluster/peers", func(w http.ResponseWriter, r *http.Request) {
		out, _ := json.Marshal(svr.Peers())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(out)
	})

	// Access the embedded 'swagger' folder.
	swaggerFS, err := fs.Sub(content, "swagger")
	if err != nil {
		logger.Fatal("failed to load swagger files", zap.Error(err))
		return
	}

	// Create a file server to serve the embedded content.
	fileServer := http.FileServer(http.FS(swaggerFS))
	mux.Handle("/swagger/", http.StripPrefix("/swagger", fileServer))
	mux.Handle("/api/docs/", http.StripPrefix("/api/docs", fileServer))
	mux.HandleFunc("/api/docs", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/api/docs/", http.StatusFound) })

	c := cors.New(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{
			http.MethodHead,
			http.MethodOptions,
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
		},
		AllowedHeaders: []string{
			"Content-Type",
			"Authorization",
		},
		AllowCredentials: true,
	})
	handler := c.Handler(log.HttpLogger(mux, logger))

	httpServer := &http.Server{
		Handler: handler,
		Addr:    cfg.Get().HTTPAddr,
	}

	waitGroup.Go(func() error {
		logger.Info("start gateway server", zap.String("addr", httpServer.Addr))
		err = httpServer.ListenAndServe()
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			logger.Fatal("failed to start gateway server", zap.Error(err))
			return err
		}
		return nil
	})

	waitGroup.Go(func() error {
		<-ctx.Done()
		logger.Info("graceful shutdown HTTP gateway server", zap.String("addr", httpServer.Addr))

		err = httpServer.Shutdown(ctx)
		if err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				logger.Fatal("failed to shutdown HTTP server", zap.Error(err))
				return err
			}
			return nil
		}

		logger.Info("HTTP gateway server is stopped", zap.String("addr", httpServer.Addr))
		return nil
	})
}
