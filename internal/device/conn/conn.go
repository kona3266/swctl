// Package conn 是设备后端的通用装配胶水：按设备清单与方言注册表构造 southbound
// 后端（driver.Device）。这里不含任何平台知识——连接方式、渲染、解析都由
// 各平台后端（dialect 注册的 NewDevice）自己实现。
package conn

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"

	"switchctl/internal/config"
	"switchctl/internal/device/dialect"
	"switchctl/internal/device/driver"
)

// Factory 按设备名构造设备后端。
type Factory struct {
	cfg *config.Config
	log *slog.Logger
}

// New 构造 Factory。
func New(cfg *config.Config, log *slog.Logger) *Factory {
	return &Factory{cfg: cfg, log: log}
}

// Probe 建立后端、完成一次 Open 后立即关闭。
// 用于启动阶段确认设备与南向通道可用；返回的错误可用于判断是否值得重试。
func (f *Factory) Probe(ctx context.Context, name string) error {
	dev, _, err := f.Open(ctx, name)
	if err != nil {
		return err
	}
	defer func() { _ = dev.Close() }()
	return dev.Open(ctx)
}

// Open 解析凭据并构造该设备平台的后端。
func (f *Factory) Open(ctx context.Context, name string) (driver.Device, *config.Device, error) {
	cfgDev, ok := f.cfg.DeviceByName(name)
	if !ok {
		return nil, nil, fmt.Errorf("设备 %q 不在配置清单中", name)
	}
	impl, ok := dialect.Get(cfgDev.Dialect)
	if !ok {
		return nil, nil, fmt.Errorf("设备 %s 的平台后端未实现: %q", name, cfgDev.Dialect)
	}
	cred, err := cfgDev.Resolve()
	if err != nil {
		return nil, nil, err
	}
	dev, err := impl.NewDevice(driver.Config{
		Name:                 cfgDev.Name,
		Endpoint:             net.JoinHostPort(cfgDev.Host, strconv.Itoa(cfgDev.Port)),
		Username:             cred.Username,
		Password:             cred.Password,
		PrivateKeyPEM:        cred.PrivateKeyPEM,
		PrivateKeyPassphrase: cred.PrivateKeyPassphrase,
		OperationTimeout:     f.cfg.Execution.OperationTimeout.Std(),
		Marker:               f.cfg.Marking.Value,
		Logger:               f.log.With("device", name),
	})
	if err != nil {
		return nil, nil, err
	}
	return dev, cfgDev, nil
}
