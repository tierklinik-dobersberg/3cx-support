package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ghodss/yaml"
	"github.com/sethvargo/go-envconfig"
)

type Config struct {
	IdmURL                     string   `env:"IDM_URL" json:"idmURL"`
	RosterdURL                 string   `env:"ROSTERD_URL" json:"rosterdUrl"`
	CustomerServiceURL         string   `env:"CUSTOMERD_URL" json:"customerdUrl"`
	Country                    string   `env:"COUNTRY,default=AT" json:"country"`
	MongoURL                   string   `env:"MONGO_URL" json:"mongoUrl"`
	Database                   string   `env:"DATABASE" json:"database"`
	AllowedOrigins             []string `env:"ALLOWED_ORIGINS" json:"allowedOrigins"`
	ListenAddress              string   `env:"LISTEN" json:"listenAddress"`
	RosterTypeName             string   `env:"ROSTER_TYPE" json:"rosterType"`
	UserPhoneExtensionKeys     []string `env:"PHONE_EXTENSION_KEYS" json:"phoneExtensionKeys"`
	FailoverTransferTarget     string   `env:"FAILOVER_TRANSFER_TARGET" json:"failoverTransferTarget"`
	DefaultOnCallInboundNumber string   `env:"DEFAULT_INBOUND_NUMBER" json:"defaultInboundNumber"`
	VoiceMailStoragePath       string   `env:"STORAGE_PATH" json:"storagePath"`
	NotificationSenderId       string   `env:"NOTIFICATION_SENDER_ID" json:"notificationSenderId"`
}

func LoadConfig(ctx context.Context, path string) (*Config, error) {
	var cfg Config

	if path != "" {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file at path %q: %w", path, err)
		}

		switch filepath.Ext(path) {
		case ".yaml", ".yml":
			content, err = yaml.YAMLToJSON(content)
			if err != nil {
				return nil, fmt.Errorf("failed to convert YAML to JSON: %w", err)
			}

			fallthrough
		case ".json":
			dec := json.NewDecoder(bytes.NewReader(content))
			dec.DisallowUnknownFields()

			if err := dec.Decode(&cfg); err != nil {
				return nil, fmt.Errorf("failed to decode JSON: %w", err)
			}
		}
	}

	if err := envconfig.Process(ctx, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse configuration from environment: %w", err)
	}

	if cfg.ListenAddress == "" {
		cfg.ListenAddress = ":8080"
	}

	if len(cfg.AllowedOrigins) == 0 {
		cfg.AllowedOrigins = []string{"*"}
	}

	if cfg.IdmURL == "" {
		return nil, fmt.Errorf("missing idmUrl config setting")
	}

	if cfg.RosterdURL == "" {
		return nil, fmt.Errorf("missing rosterdUrl config tetting")
	}

	if cfg.MongoURL == "" {
		return nil, fmt.Errorf("missing mongoUrl config setting")
	}

	if cfg.CustomerServiceURL == "" {
		return nil, fmt.Errorf("missing customerdUrl config setting")
	}

	if cfg.VoiceMailStoragePath == "" {
		return nil, fmt.Errorf("missing voice-mail storage path")
	}

	return &cfg, nil
}
