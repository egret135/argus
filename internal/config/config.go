package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type SourceConfig struct {
	Type      string `yaml:"type"`
	Path      string `yaml:"path,omitempty"`
	Container string `yaml:"container,omitempty"`
}

type FeishuConfig struct {
	WebhookURL string `yaml:"webhook_url"`
	Secret     string `yaml:"secret"`
}

type AlertConfig struct {
	Feishu     FeishuConfig  `yaml:"feishu"`
	Cooldown   time.Duration `yaml:"cooldown"`
	MaxRetries int           `yaml:"max_retries"`
}

type StorageConfig struct {
	MaxEntries            int           `yaml:"max_entries"`
	DataDir               string        `yaml:"data_dir"`
	WALCompactThreshold   int           `yaml:"wal_compact_threshold"`
	CheckpointInterval    time.Duration `yaml:"checkpoint_interval"`
	IndexRebuildInterval  int           `yaml:"index_rebuild_interval"`
	IndexRebuildMaxInterval time.Duration `yaml:"index_rebuild_max_interval"`
}

type ServerConfig struct {
	Addr    string `yaml:"addr"`
	BaseURL string `yaml:"base_url"`
}

type AuthConfig struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
	JWTSecret    string `yaml:"jwt_secret"`
}

type Config struct {
	Sources []SourceConfig `yaml:"sources"`
	Storage StorageConfig  `yaml:"storage"`
	Alert   AlertConfig    `yaml:"alert"`
	Server  ServerConfig   `yaml:"server"`
	Auth    AuthConfig     `yaml:"auth"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := &Config{
		Storage: StorageConfig{
			MaxEntries:              50000,
			DataDir:                 "./data",
			WALCompactThreshold:     60000,
			CheckpointInterval:      5 * time.Second,
			IndexRebuildInterval:    10000,
			IndexRebuildMaxInterval: 30 * time.Second,
		},
		Alert: AlertConfig{
			Cooldown:   60 * time.Second,
			MaxRetries: 5,
		},
		Server: ServerConfig{
			Addr: ":8080",
		},
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	if err := c.validateStorage(); err != nil {
		return err
	}
	if err := c.validateSources(); err != nil {
		return err
	}
	if err := c.validateAlert(); err != nil {
		return err
	}
	if err := c.validateAuth(); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateStorage() error {
	if c.Storage.MaxEntries <= 0 {
		return fmt.Errorf("storage.max_entries must be > 0, got %d", c.Storage.MaxEntries)
	}
	if c.Storage.CheckpointInterval <= 0 {
		return fmt.Errorf("storage.checkpoint_interval must be > 0, got %s", c.Storage.CheckpointInterval)
	}
	if c.Storage.IndexRebuildInterval <= 0 {
		return fmt.Errorf("storage.index_rebuild_interval must be > 0, got %d", c.Storage.IndexRebuildInterval)
	}

	if err := os.MkdirAll(c.Storage.DataDir, 0o755); err != nil {
		return fmt.Errorf("storage.data_dir: cannot create directory %q: %w", c.Storage.DataDir, err)
	}

	testFile := filepath.Join(c.Storage.DataDir, ".argus_write_test")
	f, err := os.Create(testFile)
	if err != nil {
		return fmt.Errorf("storage.data_dir: directory %q is not writable: %w", c.Storage.DataDir, err)
	}
	f.Close()
	os.Remove(testFile)

	return nil
}

func (c *Config) validateSources() error {
	seenFiles := make(map[string]bool)
	seenContainers := make(map[string]bool)

	for i := range c.Sources {
		src := &c.Sources[i]
		switch src.Type {
		case "file":
			if src.Path == "" {
				return fmt.Errorf("sources[%d]: file source requires a path", i)
			}
			absPath, err := filepath.Abs(src.Path)
			if err != nil {
				return fmt.Errorf("sources[%d]: cannot resolve absolute path for %q: %w", i, src.Path, err)
			}
			resolved, err := filepath.EvalSymlinks(absPath)
			if err != nil {
				if !os.IsNotExist(err) {
					return fmt.Errorf("sources[%d]: cannot evaluate symlinks for %q: %w", i, absPath, err)
				}
				resolved = absPath
			}
			src.Path = resolved

			if seenFiles[resolved] {
				return fmt.Errorf("sources[%d]: duplicate file source path %q", i, resolved)
			}
			seenFiles[resolved] = true

		case "docker":
			if src.Container == "" {
				return fmt.Errorf("sources[%d]: docker source requires a container name", i)
			}
			if seenContainers[src.Container] {
				return fmt.Errorf("sources[%d]: duplicate docker container name %q", i, src.Container)
			}
			seenContainers[src.Container] = true

		default:
			return fmt.Errorf("sources[%d]: unknown source type %q", i, src.Type)
		}
	}

	if len(seenContainers) > 0 {
		if _, err := os.Stat("/var/run/docker.sock"); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: docker sources configured but /var/run/docker.sock not found: %v\n", err)
		}
	}

	return nil
}

func (c *Config) validateAlert() error {
	if c.Alert.Cooldown <= 0 {
		return fmt.Errorf("alert.cooldown must be > 0, got %s", c.Alert.Cooldown)
	}

	webhookURL := c.Alert.Feishu.WebhookURL
	if webhookURL == "" {
		return fmt.Errorf("alert.feishu.webhook_url must not be empty")
	}
	u, err := url.Parse(webhookURL)
	if err != nil {
		return fmt.Errorf("alert.feishu.webhook_url is not a valid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("alert.feishu.webhook_url must use https scheme, got %q", u.Scheme)
	}

	return nil
}

func (c *Config) validateAuth() error {
	if c.Auth.PasswordHash == "" {
		return fmt.Errorf("auth.password_hash must not be empty")
	}
	if !strings.HasPrefix(c.Auth.PasswordHash, "$2a$") && !strings.HasPrefix(c.Auth.PasswordHash, "$2b$") {
		return fmt.Errorf("auth.password_hash must be a bcrypt hash (starting with $2a$ or $2b$)")
	}
	return nil
}
