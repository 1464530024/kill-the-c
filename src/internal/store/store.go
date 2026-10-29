// Package store 负责应用配置（白名单、偏好）的持久化。
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Config struct {
	Version    int      `json:"version"`
	Whitelist  []string `json:"whitelist"`
	Theme      string   `json:"theme"`
	LastMode   string   `json:"lastMode"`
	MinDupSize int64    `json:"minDupSize"`
	SkipSystem bool     `json:"skipSystem"`
	DeepRoots  []string `json:"deepRoots"`
}

var (
	mu   sync.RWMutex
	cur  *Config
	path string
)

func baseDir() string {
	d := os.Getenv("APPDATA")
	if d == "" {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, "AppData", "Roaming")
	}
	return filepath.Join(d, "KillTheC")
}

// Dir 返回应用数据目录。
func Dir() string { return baseDir() }

func defaultConfig() *Config {
	return &Config{
		Version:    1,
		Whitelist:  []string{},
		Theme:      "light",
		LastMode:   "light",
		MinDupSize: 1 << 20, // 1MB
		SkipSystem: true,
	}
}

// Load 读取配置，不存在时创建默认配置。
func Load() *Config {
	mu.Lock()
	defer mu.Unlock()
	dir := baseDir()
	_ = os.MkdirAll(dir, 0o755)
	path = filepath.Join(dir, "config.json")

	c := defaultConfig()
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, c)
	}
	normalize(c)
	cur = c
	return clone(c)
}

func normalize(c *Config) {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Theme != "dark" {
		c.Theme = "light"
	}
	if c.LastMode != "deep" {
		c.LastMode = "light"
	}
	if c.MinDupSize < 0 {
		c.MinDupSize = 1 << 20
	}
	seen := map[string]bool{}
	out := c.Whitelist[:0]
	for _, w := range c.Whitelist {
		w = filepath.Clean(strings.TrimSpace(w))
		if w == "" || w == "." {
			continue
		}
		k := strings.ToLower(w)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, w)
	}
	c.Whitelist = out
}

func clone(c *Config) *Config {
	b, _ := json.Marshal(c)
	var n Config
	_ = json.Unmarshal(b, &n)
	return &n
}

// Get 返回当前配置副本。
func Get() *Config {
	mu.RLock()
	defer mu.RUnlock()
	if cur == nil {
		return defaultConfig()
	}
	return clone(cur)
}

func save(c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Update 以回调方式修改并落盘配置。
func Update(fn func(c *Config)) (*Config, error) {
	mu.Lock()
	defer mu.Unlock()
	if cur == nil {
		cur = defaultConfig()
	}
	fn(cur)
	normalize(cur)
	err := save(cur)
	return clone(cur), err
}

// AddWhitelist 添加白名单目录。
func AddWhitelist(dir string) (*Config, error) {
	return Update(func(c *Config) {
		c.Whitelist = append(c.Whitelist, dir)
	})
}

// RemoveWhitelist 移除白名单目录。
func RemoveWhitelist(dir string) (*Config, error) {
	target := strings.ToLower(filepath.Clean(dir))
	return Update(func(c *Config) {
		var out []string
		for _, w := range c.Whitelist {
			if strings.ToLower(filepath.Clean(w)) != target {
				out = append(out, w)
			}
		}
		c.Whitelist = out
	})
}

// IsWhitelisted 判断路径是否落在白名单内。
func IsWhitelisted(p string) bool {
	mu.RLock()
	defer mu.RUnlock()
	if cur == nil {
		return false
	}
	lp := strings.ToLower(filepath.Clean(p))
	for _, w := range cur.Whitelist {
		lw := strings.ToLower(filepath.Clean(w))
		if lp == lw {
			return true
		}
		if strings.HasPrefix(lp, lw+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}
