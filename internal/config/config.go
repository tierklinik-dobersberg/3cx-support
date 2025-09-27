package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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
	EventsServiceURL           string   `env:"EVENTS_SERVICE_URL" json:"eventsServiceUrl"`
	NotificationSenderId       string   `env:"NOTIFICATION_SENDER_ID" json:"notificationSenderId"`

	CDRMode string `env:"CDR_MODE, default=OFF" json:"cdrMode"` // ACTIVE, PASSIVE, OFF (default)
	CDRAddr string `env:"CDR_ADDR" json:"cdrAddr"`              // either bind socket (CDR_MODE=ACTIVE) or addr to connect to (CDR_MODE=PASSIVE)
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

	if cfg.EventsServiceURL == "" {
		return nil, fmt.Errorf("missing events-service URL")
	}

	// validate CDR settings
	switch strings.ToLower(cfg.CDRMode) {
	case "active": // ACTIVE Socket mode in 3cx means they will connect, we can use a default here
		if cfg.CDRAddr == "" {
			slog.Info("CDR configured in 3CX ACTIVE mode, using default listen-address :3031")
			cfg.CDRAddr = ":3031"
		}

	case "passive": // PASSIVE Socket mode in 3cx means they are listening so we __need__ an address
		if cfg.CDRAddr == "" {
			return nil, fmt.Errorf("missing CDR_ADDR if CDR_MODE != OFF")
		}
		slog.Info("CDR configured in 3CX PASSIVE mode, connecting to " + cfg.CDRAddr)

	case "", "off":
		slog.Info("CDR disabled")
	default:
		return nil, fmt.Errorf("invalid setting for CDR_MODE, allowed values are ACTIVE, PASSIVE and OFF (default)")
	}

	return &cfg, nil
}
