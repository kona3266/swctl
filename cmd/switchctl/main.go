// switchctl 是交换机配置下发控制器：REST API 接收配置，安全地下发到交换机。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"switchctl/internal/config"
	"switchctl/internal/device/conn"
	"switchctl/internal/device/dialect"
	"switchctl/internal/httpapi"
	"switchctl/internal/logx"
	"switchctl/internal/orchestrator"
	"switchctl/internal/service"
	"switchctl/internal/state"
	"switchctl/internal/store"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "switchctl 启动失败:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", "configs/switchctl.yaml", "配置文件路径")
		showVersion = flag.Bool("version", false, "输出版本后退出")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("switchctl", version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	logger, logCloser, err := setupLogger(cfg.Logging)
	if err != nil {
		return err
	}
	defer logCloser()

	// 打开数据库；失败直接退出（不做只读降级）。
	db, err := store.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	logger.Info("switchctl 启动", "version", version, "config", *configPath, "listen", cfg.Server.Listen)

	ctx := context.Background()

	// YAML 是设备清单的 source of truth，启动时同步进 DB（保留运行时状态）
	for i := range cfg.Devices {
		d := &cfg.Devices[i]
		if _, ok := dialect.Get(d.Dialect); !ok {
			// 南向后端尚未实现：启动初始化会失败，控制器将直接退出。
			logger.Warn("方言后端未实现，启动初始化将失败", "device", d.Name, "dialect", d.Dialect)
		}
		if _, err := db.SyncDevice(ctx, d.Name, d.Host, d.Port, d.Dialect); err != nil {
			return fmt.Errorf("同步设备清单: %w", err)
		}
		// 启动即解析凭据：环境变量缺失/文件权限不对时立刻失败，而不是等首次下发
		if _, err := d.Resolve(); err != nil {
			return fmt.Errorf("设备凭据检查: %w", err)
		}
	}
	logger.Info("设备清单已同步", "devices", len(cfg.Devices))

	// 权威配置状态（内存为主，DB 持久化）
	stateMgr, err := state.New(ctx, db, logger)
	if err != nil {
		return err
	}
	// 新设备默认空模型
	for _, d := range cfg.Devices {
		devRow, err := db.GetDeviceByName(ctx, d.Name)
		if err != nil {
			return fmt.Errorf("读取设备记录: %w", err)
		}
		if err := stateMgr.Ensure(ctx, devRow.ID); err != nil {
			return fmt.Errorf("初始化设备配置模型: %w", err)
		}
	}

	// 编排器（执行引擎）；设备的 southbound 后端由 conn 按平台注册表构造。
	// 注意：启动时会为每台设备做初始化探测，任一设备失败都会导致进程退出，
	// 因此没有可用的 southbound 后端时控制器不会启动。
	switchDriver := conn.New(cfg, logger)
	runner := orchestrator.New(orchestrator.Deps{
		Config: cfg,
		Store:  db,
		State:  stateMgr,
		Logger: logger,
		Open:   switchDriver.Open,
		Probe:  switchDriver.Probe,
	})

	svc := service.New(cfg, db, stateMgr, runner, logger)

	defer func() {
		done := make(chan struct{})
		go func() { runner.Stop(); close(done) }()
		select {
		case <-done:
		case <-time.After(cfg.Server.ShutdownGrace.Std()):
			logger.Warn("等待在途任务超时，强制退出")
		}
	}()

	// 初始化所有设备并触发首轮对账；任一设备初始化失败则直接退出。
	if err := runner.Start(ctx); err != nil {
		return err
	}

	api := httpapi.New(httpapi.Deps{
		Config:  cfg,
		Service: svc,
		Logger:  logger,
	})

	srv := &http.Server{
		Addr:         cfg.Server.Listen,
		Handler:      api.Handler(),
		ReadTimeout:  cfg.Server.ReadTimeout.Std(),
		WriteTimeout: cfg.Server.WriteTimeout.Std(),
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	logger.Info("HTTP 服务已启动", "listen", cfg.Server.Listen)

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return fmt.Errorf("HTTP 服务异常退出: %w", err)
	case <-sigCtx.Done():
	}

	logger.Info("收到退出信号，停止接收新请求", "grace", cfg.Server.ShutdownGrace.Std().String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownGrace.Std())
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP 优雅退出超时", "error", err)
	}
	logger.Info("switchctl 已退出")
	return nil
}

// setupLogger 按配置构造日志器；file 为空则写 stderr。
func setupLogger(cfg config.Logging) (*slog.Logger, func(), error) {
	var w io.Writer = os.Stderr
	closer := func() {}
	if cfg.File != "" {
		if dir := filepath.Dir(cfg.File); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, nil, fmt.Errorf("创建日志目录: %w", err)
			}
		}
		f, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640) // #nosec G304
		if err != nil {
			return nil, nil, fmt.Errorf("打开日志文件: %w", err)
		}
		w = f
		closer = func() { _ = f.Close() }
	}
	logger, err := logx.New(cfg.Level, cfg.Format, w, cfg.RedactPatterns)
	if err != nil {
		closer()
		return nil, nil, err
	}
	return logger, closer, nil
}
