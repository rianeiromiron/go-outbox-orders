// Package config lee la configuración del worker desde variables de entorno.
package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	DatabaseURL  string        `env:"DATABASE_URL,required"`
	RedisAddr    string        `env:"REDIS_ADDR,required"`
	PollInterval time.Duration `env:"POLL_INTERVAL" envDefault:"1s"`
	BatchSize    int           `env:"BATCH_SIZE" envDefault:"50"`
	Concurrency  int           `env:"CONCURRENCY" envDefault:"10"`
}

func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if cfg.PollInterval <= 0 || cfg.BatchSize <= 0 || cfg.Concurrency <= 0 {
		return Config{}, errors.New("config: POLL_INTERVAL, BATCH_SIZE y CONCURRENCY deben ser positivos")
	}
	return cfg, nil
}
