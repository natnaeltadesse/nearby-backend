// Package config loads all runtime configuration from the environment.
//
// Nothing else in the codebase reads os.Getenv: if a knob exists, it is a
// field here with a documented default.
package config

import (
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config is the whole of the API's runtime configuration.
type Config struct {
	// ---- server
	Env      string `envconfig:"ENV" default:"development"`
	HTTPPort int    `envconfig:"HTTP_PORT" default:"8081"`
	LogLevel string `envconfig:"LOG_LEVEL" default:"info"`
	// auto | always | never. `auto` colours only when stdout is a terminal,
	// so piping to a file or a log shipper stays clean.
	LogColor string `envconfig:"LOG_COLOR" default:"auto"`
	// Serves GET /docs and GET /openapi.yaml. The spec contains no secrets,
	// but it does enumerate the whole surface, so it is one switch away from
	// being off in a hostile environment.
	DocsEnabled bool `envconfig:"DOCS_ENABLED" default:"true"`
	// Browsers enforce CORS; the mobile app does not. "*" is fine in
	// development and must be a real origin list in production.
	CORSAllowedOrigins []string `envconfig:"CORS_ALLOWED_ORIGINS" default:"*"`

	// ---- database
	DatabaseURL      string `envconfig:"DATABASE_URL" required:"true"`
	DatabaseMaxConns int32  `envconfig:"DATABASE_MAX_CONNS" default:"10"`

	// ---- auth
	JWTSecret string `envconfig:"JWT_SECRET" required:"true"`
	JWTIssuer string `envconfig:"JWT_ISSUER" default:"booking-api"`
	// Access tokens are verified locally, so a short TTL is cheap.
	AccessTokenTTL time.Duration `envconfig:"ACCESS_TOKEN_TTL" default:"15m"`
	// Refresh TTLs differ by client: a phone is not re-authenticated as often
	// as a browser. 720h = 30d, 2160h = 90d.
	RefreshTokenTTLWeb    time.Duration `envconfig:"REFRESH_TOKEN_TTL_WEB" default:"720h"`
	RefreshTokenTTLMobile time.Duration `envconfig:"REFRESH_TOKEN_TTL_MOBILE" default:"2160h"`

	// ---- scheduling
	// Fallback zone for providers created without one; slot math always uses
	// the provider's own timezone column.
	DefaultTimezone      string        `envconfig:"DEFAULT_TIMEZONE" default:"Africa/Addis_Ababa"`
	SlotStepMinutes      int           `envconfig:"SLOT_STEP_MINUTES" default:"15"`
	MinLeadMinutes       int           `envconfig:"MIN_LEAD_MINUTES" default:"30"`
	AvailabilityCacheTTL time.Duration `envconfig:"AVAILABILITY_CACHE_TTL" default:"60s"`

	// ---- media
	// Where uploads are kept when no hosted provider is configured. Ephemeral
	// in a container and not shared between replicas — development only.
	MediaLocalDir string `envconfig:"MEDIA_LOCAL_DIR" default:"./uploads"`
	// Setting all three Cloudinary values switches storage over; leaving any
	// of them blank keeps uploads on local disk.
	CloudinaryCloudName    string `envconfig:"CLOUDINARY_CLOUD_NAME"`
	CloudinaryAPIKey       string `envconfig:"CLOUDINARY_API_KEY"`
	CloudinaryAPISecret    string `envconfig:"CLOUDINARY_API_SECRET"`
	CloudinaryUploadFolder string `envconfig:"CLOUDINARY_UPLOAD_FOLDER" default:"booking"`

	// ---- payments (milestone 11)
	ChapaSecretKey         string `envconfig:"CHAPA_SECRET_KEY"`
	ChapaBaseURL           string `envconfig:"CHAPA_BASE_URL" default:"https://api.chapa.co/v1"`
	TelebirrAppID          string `envconfig:"TELEBIRR_APP_ID"`
	TelebirrAppKey         string `envconfig:"TELEBIRR_APP_KEY"`
	TelebirrPublicKey      string `envconfig:"TELEBIRR_PUBLIC_KEY"`
	PaymentCallbackBaseURL string `envconfig:"PAYMENT_CALLBACK_BASE_URL"`
}

// Load reads .env (without overriding anything already exported) and then the
// process environment. Reading .env here is what lets `go run ./cmd/api` work
// the same as `make run`, which exports the file itself.
func Load() (Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return Config{}, fmt.Errorf("read .env: %w", err)
	}

	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if len(c.JWTSecret) < 16 {
		return fmt.Errorf("JWT_SECRET must be at least 16 characters")
	}
	if c.SlotStepMinutes <= 0 {
		return fmt.Errorf("SLOT_STEP_MINUTES must be positive, got %d", c.SlotStepMinutes)
	}
	if _, err := time.LoadLocation(c.DefaultTimezone); err != nil {
		return fmt.Errorf("DEFAULT_TIMEZONE %q is not a known IANA zone: %w", c.DefaultTimezone, err)
	}
	return nil
}

// IsDevelopment reports whether verbose, human-readable behaviour is wanted.
func (c Config) IsDevelopment() bool { return c.Env == "development" }

// Addr is the listen address for the HTTP server.
func (c Config) Addr() string { return fmt.Sprintf(":%d", c.HTTPPort) }
