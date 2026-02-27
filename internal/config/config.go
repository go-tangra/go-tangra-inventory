package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

// Config holds the collector daemon configuration.
type Config struct {
	Listen         string        `mapstructure:"listen"`
	HTTPListen     string        `mapstructure:"http_listen"`
	EnableSwagger  bool          `mapstructure:"enable_swagger"`
	DatabasePath   string        `mapstructure:"database"`
	RetentionDays  int           `mapstructure:"retention_days"`
	PurgeInterval  time.Duration `mapstructure:"purge_interval"`
	ClientSecret   string        `mapstructure:"client_secret"`
	ApiSecret      string        `mapstructure:"api_secret"`
}

// Load reads configuration from file and environment.
func Load(cfgFile string) (*Config, error) {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("collector")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		viper.AddConfigPath("./configs")
		viper.AddConfigPath("/etc/inventory-collector")
	}

	viper.SetDefault("listen", ":9550")
	viper.SetDefault("http_listen", ":9551")
	viper.SetDefault("enable_swagger", true)
	viper.SetDefault("database", "inventory.db")
	viper.SetDefault("retention_days", 0)
	viper.SetDefault("purge_interval", "24h")

	viper.SetEnvPrefix("COLLECTOR")
	viper.AutomaticEnv()

	_ = viper.ReadInConfig()

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	return &cfg, nil
}
