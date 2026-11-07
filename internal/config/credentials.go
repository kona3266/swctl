package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Credential 是解析后的明文凭据，只应存在于内存中。
// 实现 String() 脱敏，防止被意外打进日志/错误信息。
type Credential struct {
	Username             string
	Password             string
	EnablePassword       string
	PrivateKeyPEM        []byte
	PrivateKeyPassphrase string
}

func (c Credential) String() string { return "Credential{<redacted>}" }

// GoString 防止 %#v 泄露。
func (c Credential) GoString() string { return "Credential{<redacted>}" }

// validate 校验凭据引用配置是否自洽（不解析具体环境变量值）。
func (c *Credentials) validate(prefix string) error {
	var errs []error
	if c.UsernameEnv == "" && c.UsernameFile == "" {
		errs = append(errs, fmt.Errorf("%s.credentials 需要 username_env 或 username_file", prefix))
	}
	if c.PasswordEnv == "" && c.PasswordFile == "" && c.PrivateKeyFile == "" {
		errs = append(errs, fmt.Errorf("%s.credentials 需要 password_env/password_file 或 private_key_file 之一", prefix))
	}
	if c.PrivateKeyPassphraseFile != "" && c.PrivateKeyFile == "" {
		errs = append(errs, fmt.Errorf("%s.credentials.private_key_passphrase_* 需要配合 private_key_file 使用", prefix))
	}
	return errors.Join(errs...)
}

// Resolve 解析设备的凭据：环境变量优先于文件，私钥优先于密码。
func (d *Device) Resolve() (Credential, error) {
	var cred Credential

	user, err := resolveSecret(d.Credentials.UsernameEnv, d.Credentials.UsernameFile)
	if err != nil {
		return cred, fmt.Errorf("设备 %s 用户名解析失败: %w", d.Name, err)
	}
	if user == "" {
		return cred, fmt.Errorf("设备 %s 用户名未配置或为空", d.Name)
	}
	cred.Username = user

	if d.Credentials.PrivateKeyFile != "" {
		pem, err := readSecretFile(d.Credentials.PrivateKeyFile)
		if err != nil {
			return cred, fmt.Errorf("设备 %s 私钥读取失败: %w", d.Name, err)
		}
		cred.PrivateKeyPEM = []byte(pem)
		if pass, err := resolveSecret(d.Credentials.PrivateKeyPassphraseEnv, d.Credentials.PrivateKeyPassphraseFile); err != nil {
			return cred, fmt.Errorf("设备 %s 私钥口令解析失败: %w", d.Name, err)
		} else {
			cred.PrivateKeyPassphrase = pass
		}
	} else {
		pass, err := resolveSecret(d.Credentials.PasswordEnv, d.Credentials.PasswordFile)
		if err != nil {
			return cred, fmt.Errorf("设备 %s 密码解析失败: %w", d.Name, err)
		}
		if pass == "" {
			return cred, fmt.Errorf("设备 %s 密码未配置或为空", d.Name)
		}
		cred.Password = pass
	}

	enable, err := resolveSecret(d.Credentials.EnablePasswordEnv, d.Credentials.EnablePasswordFile)
	if err != nil {
		return cred, fmt.Errorf("设备 %s enable 密码解析失败: %w", d.Name, err)
	}
	cred.EnablePassword = enable
	return cred, nil
}

// resolveSecret 按 env → file 顺序解析一个秘密值；两者都未配置时返回空串。
// env 值允许写成 ${VAR} 形式。
func resolveSecret(envName, fileName string) (string, error) {
	switch {
	case envName != "":
		name := strings.TrimSpace(envName)
		if strings.HasPrefix(name, "${") && strings.HasSuffix(name, "}") {
			name = name[2 : len(name)-1]
		}
		if name == "" {
			return "", errors.New("环境变量名为空")
		}
		v := os.Getenv(name)
		if v == "" {
			return "", fmt.Errorf("环境变量 %s 未设置或为空", name)
		}
		return v, nil
	case fileName != "":
		return readSecretFile(fileName)
	default:
		return "", nil
	}
}

// readSecretFile 读取秘密文件，要求权限不含组/其他位（0600 或更严）。
func readSecretFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("文件 %s 权限为 %#o，必须为 0600 或更严（chmod 600）", path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- 路径来自管理员配置
	if err != nil {
		return "", err
	}
	// 去掉末尾换行（kubectl/docker secret 常见尾随换行）
	return strings.TrimRight(string(raw), "\r\n"), nil
}
