package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	Mode       string                        `json:"mode"`
	BalanceGap float64                       `json:"balance_gap"`
	Fit        map[string]map[string]float64 `json:"fit"`
	Args       map[string][]string           `json:"args"`
	Enabled    map[string]bool               `json:"enabled"`
}

func defaultConfig() Config {
	return Config{
		Mode:       "auto",
		BalanceGap: 25,
		Fit: map[string]map[string]float64{
			"claude": {classImplement: 80, classReview: 90, classResearch: 70, classQuick: 60},
			"codex":  {classImplement: 90, classReview: 75, classResearch: 55, classQuick: 70},
			"grok":   {classImplement: 75, classReview: 70, classResearch: 85, classQuick: 80},
		},
		Args: map[string][]string{},
		Enabled: map[string]bool{
			"claude": true,
			"codex":  true,
			"grok":   true,
		},
	}
}

func (c Config) enabled(name string) bool {
	if c.Enabled == nil {
		return true
	}
	on, ok := c.Enabled[name]
	if !ok {
		return true
	}
	return on
}

func configPath() string {
	if v := os.Getenv("BELLA_CONFIG"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".bella", "config.json")
}

func loadConfig() Config {
	cfg := defaultConfig()
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	var file Config
	if json.Unmarshal(data, &file) != nil {
		return cfg
	}
	if file.Mode == "auto" || file.Mode == policyBalance || file.Mode == policyBest {
		cfg.Mode = file.Mode
	}
	if file.BalanceGap > 0 {
		cfg.BalanceGap = file.BalanceGap
	}
	for name, fit := range file.Fit {
		if cfg.Fit[name] == nil {
			cfg.Fit[name] = map[string]float64{}
		}
		for class, value := range fit {
			cfg.Fit[name][class] = value
		}
	}
	if file.Args != nil {
		cfg.Args = file.Args
	}
	if file.Enabled != nil {
		cfg.Enabled = file.Enabled
	}
	return cfg
}

func (c Config) save() error {
	path := configPath()
	if path == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	return os.WriteFile(path, buf, 0o644)
}

func saveDefaultConfig() (string, error) {
	path := configPath()
	if path == "" {
		return "", os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	buf, err := json.MarshalIndent(defaultConfig(), "", "  ")
	if err != nil {
		return "", err
	}
	buf = append(buf, '\n')
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
