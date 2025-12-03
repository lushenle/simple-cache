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
	cfg, _ := config.Load(cfgPath)
	acfg := config.NewAtomic(cfg)
	stop := make(chan struct{})
	if cfg.HotReload {
		w := config.NewWatcher("config.yaml", acfg)
		go w.Start(stop)
	}
	go http.ListenAndServe(cfg.MetricsAddr, metricsMux)
	// Create a new gRPC server
	c := cache.New(30*time.Second, logger)
	srv := server.New(c)

	if cfg.Mode == "distributed" {
		st := raft.NewStorage("data/raft-" + cfg.NodeID + ".wal")
		n := raft.NewNode(cfg.NodeID, cfg.RaftHTTPAddr, cfg.Peers, st, srv, time.Duration(cfg.HeartbeatMS)*time.Millisecond, time.Duration(cfg.ElectionMS)*time.Millisecond, logger)
		srv.UseRaft(n)
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
	lis, _ := net.Listen("tcp", cfg.GRPCAddr)
	grpcServer := grpc.NewServer()
	pb.RegisterCacheServiceServer(grpcServer, srv)
	grpcServer.Serve(lis)
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
		_ = dec.Decode(&in)
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
		_ = dec.Decode(&in)
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
