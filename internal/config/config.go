// Package config 读写 %APPDATA%\ipsend 下的设置（config.json）和 IP 历史（history.json）。
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ipsend/internal/proto"
)

// Dir 返回配置目录（%APPDATA%\ipsend），并确保其存在。
func Dir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	d := filepath.Join(base, "ipsend")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// DefaultRecvDir 是默认接收目录：%USERPROFILE%\Downloads\ipsend。
func DefaultRecvDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "received"
	}
	return filepath.Join(home, "Downloads", "ipsend")
}

type Config struct {
	Port             int      `json:"port"`
	RecvDir          string   `json:"recv_dir"`
	AutoStart        bool     `json:"auto_start"`
	PIN              string   `json:"pin,omitempty"` // 为空表示不需要 PIN
	Blocked          []string `json:"blocked,omitempty"`
	FirewallDeclined bool     `json:"firewall_declined,omitempty"` // 用户拒绝过添加防火墙规则，不再主动询问
}

func Default() Config {
	return Config{Port: proto.DefaultPort, RecvDir: DefaultRecvDir()}
}

func configPath() string { return filepath.Join(Dir(), "config.json") }

// Load 读取配置；文件不存在或损坏时返回默认值（损坏时同时返回错误）。
func Load() (Config, error) {
	c := Default()
	b, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Default(), err
	}
	c.normalize()
	return c, nil
}

func (c *Config) normalize() {
	if c.Port < 1 || c.Port > 65535 {
		c.Port = proto.DefaultPort
	}
	if strings.TrimSpace(c.RecvDir) == "" {
		c.RecvDir = DefaultRecvDir()
	}
}

// Save 原子地写入配置。
func (c Config) Save() error {
	c.normalize()
	return writeJSON(configPath(), c)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------- IP 历史 ----------

const maxHistory = 30

type HistoryEntry struct {
	Addr string    `json:"addr"`          // 用户输入的地址（可带端口）
	PIN  string    `json:"pin,omitempty"` // 对方要求 PIN 时记住
	Name string    `json:"name,omitempty"`
	Last time.Time `json:"last"`
}

type History struct {
	mu      sync.Mutex
	Entries []HistoryEntry `json:"entries"`
}

func historyPath() string { return filepath.Join(Dir(), "history.json") }

func LoadHistory() *History {
	h := &History{}
	if b, err := os.ReadFile(historyPath()); err == nil {
		_ = json.Unmarshal(b, h)
	}
	return h
}

// Addrs 返回按最近使用排序的地址列表。
func (h *History) Addrs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.Entries))
	for i, e := range h.Entries {
		out[i] = e.Addr
	}
	return out
}

// List 返回按最近使用排序的记录副本。
func (h *History) List() []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]HistoryEntry(nil), h.Entries...)
}

// Get 返回地址对应的记录。
func (h *History) Get(addr string) (HistoryEntry, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.Entries {
		if strings.EqualFold(e.Addr, addr) {
			return e, true
		}
	}
	return HistoryEntry{}, false
}

// Touch 记录一次成功使用（移到最前），并保存。pin/name 为空时保留旧值。
func (h *History) Touch(addr, pin, name string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	h.mu.Lock()
	e := HistoryEntry{Addr: addr}
	kept := h.Entries[:0:0]
	for _, old := range h.Entries {
		if strings.EqualFold(old.Addr, addr) {
			e = old
			e.Addr = addr
			continue
		}
		kept = append(kept, old)
	}
	if pin != "" {
		e.PIN = pin
	}
	if name != "" {
		e.Name = name
	}
	e.Last = time.Now()
	h.Entries = append([]HistoryEntry{e}, kept...)
	sort.SliceStable(h.Entries, func(i, j int) bool { return h.Entries[i].Last.After(h.Entries[j].Last) })
	if len(h.Entries) > maxHistory {
		h.Entries = h.Entries[:maxHistory]
	}
	snapshot := History{Entries: append([]HistoryEntry(nil), h.Entries...)}
	h.mu.Unlock()
	return writeJSON(historyPath(), &snapshot)
}

// Remove 删除一条记录并保存。
func (h *History) Remove(addr string) error {
	h.mu.Lock()
	kept := h.Entries[:0:0]
	for _, e := range h.Entries {
		if !strings.EqualFold(e.Addr, addr) {
			kept = append(kept, e)
		}
	}
	h.Entries = kept
	snapshot := History{Entries: append([]HistoryEntry(nil), h.Entries...)}
	h.mu.Unlock()
	return writeJSON(historyPath(), &snapshot)
}
