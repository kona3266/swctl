package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "switchctl.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func minimalConfig(dataDir string) string {
	return `
storage:
  data_dir: ` + dataDir + `
devices:
  - name: sw1
    host: 10.0.0.1
    dialect: cisco_ios
    credentials:
      username_env: TEST_USER
      password_env: TEST_PASS
`
}

func TestLoadDefaultsAndResolve(t *testing.T) {
	t.Setenv("TEST_USER", "admin")
	t.Setenv("TEST_PASS", "secret")

	dataDir := t.TempDir()
	cfg, err := Load(writeConfig(t, minimalConfig(dataDir)))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.Listen != "127.0.0.1:8080" || cfg.Server.MaxConfigBytes != 1<<20 {
		t.Fatalf("server defaults = %+v", cfg.Server)
	}
	if cfg.Execution.MaxQueueDepth != 128 {
		t.Fatalf("execution defaults = %+v", cfg.Execution)
	}

	if cfg.Marking.Value != "managed-by-switchctl" {
		t.Fatalf("marking defaults = %+v", cfg.Marking)
	}

	if len(cfg.Devices) != 1 || cfg.Devices[0].Port != 22 {
		t.Fatalf("devices = %+v", cfg.Devices)
	}

	cred, err := cfg.Devices[0].Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if cred.Username != "admin" || cred.Password != "secret" {
		t.Fatalf("credential = %+v", cred)
	}
}

func TestLoadRejectsUnknownAndInvalidFields(t *testing.T) {
	dataDir := t.TempDir()
	body := strings.Replace(minimalConfig(dataDir), "storage:",
		"session:\n  prompt_search_depth: 1024\nstorage:", 1)
	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("未知配置字段应被拒绝")
	}

}

func TestReadSecretFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretFile(path); err == nil {
		t.Fatal("0644 的秘密文件应被拒绝")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readSecretFile(path)
	if err != nil || got != "secret" {
		t.Fatalf("readSecretFile = %q, %v", got, err)
	}
}
