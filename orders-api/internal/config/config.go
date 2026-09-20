// Package config lee la configuración del servicio desde variables de entorno.
package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	DatabaseURL string `env:"DATABASE_URL,required"`
	HTTPAddr    string `env:"HTTP_ADDR" envDefault:":8080"`
	// EnableTestUI expone la página HTML de prueba en /ui/. Solo para desarrollo
	// local: por defecto está apagada.
	EnableTestUI bool `env:"ENABLE_TEST_UI" envDefault:"false"`
}

func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}
