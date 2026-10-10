package logger

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

const (
	envLocal = "local"
	envDev   = "dev"
	envProd  = "prod"
)

type GraylogConfig struct {
	Enabled  bool   `yaml:"enabled" env-default:"true"`
	Host     string `yaml:"host" env-default:"graylog"`
	Port     int    `yaml:"port" env-default:"12201"`
	Facility string `yaml:"facility" env-default:"post-service"`
}

type gelfMessage struct {
	Version      string         `json:"version"`
	Host         string         `json:"host"`
	ShortMessage string         `json:"short_message"`
	Timestamp    float64        `json:"timestamp"`
	Level        int            `json:"level"`
	Facility     string         `json:"facility,omitempty"`
	Env          string         `json:"_env,omitempty"`
	Component    string         `json:"_component,omitempty"`
	Fields       map[string]any `json:"-"`
}

type gelfHandler struct {
	conn     *net.Conn
	facility string
	env      string
}

func (h *gelfHandler) Enabled(_ context.Context, level slog.Level) bool {
	return true
}

func (h *gelfHandler) Handle(ctx context.Context, r slog.Record) error {
	msg := map[string]any{
		"version":       "1.1",
		"host":          "graylog",
		"short_message": r.Message,
		"timestamp":     float64(time.Now().UnixNano()) / 1000000000,
		"level":         int(levelToGraylog(r.Level)),
		"facility":      h.facility,
		"_env":          h.env,
	}

	r.Attrs(func(a slog.Attr) bool {
		msg[a.Key] = a.Value.Any()
		return true
	})

	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	_, err = (*h.conn).Write(b)
	return err
}

func (h *gelfHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *gelfHandler) WithGroup(name string) slog.Handler {
	return h
}

func levelToGraylog(level slog.Level) int {
	switch {
	case level >= slog.LevelError:
		return 3
	case level >= slog.LevelWarn:
		return 4
	case level >= slog.LevelInfo:
		return 6
	default:
		return 7
	}
}

type multiHandler struct {
	handlers []slog.Handler
}

func (h multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, handler := range h.handlers {
		if err := handler.Handle(ctx, r.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func (h multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newHandlers := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		newHandlers = append(newHandlers, handler.WithAttrs(attrs))
	}
	return multiHandler{handlers: newHandlers}
}

func (h multiHandler) WithGroup(name string) slog.Handler {
	newHandlers := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		newHandlers = append(newHandlers, handler.WithGroup(name))
	}
	return multiHandler{handlers: newHandlers}
}

func SetupLogger(env string, cfg GraylogConfig) *slog.Logger {
	stdoutHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	handlers := []slog.Handler{stdoutHandler}

	if cfg.Enabled {
		conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", cfg.Host, cfg.Port))
		if err == nil {
			handlers = append(handlers, &gelfHandler{
				conn:     &conn,
				facility: cfg.Facility,
				env:      env,
			})
		} else {
			slog.Error("Failed to connect to Graylog", slog.String("host", cfg.Host), slog.Int("port", cfg.Port))
		}
	}

	return slog.New(multiHandler{handlers: handlers})
}

func LoggingMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Info("HTTP request:",
			slog.String("method", r.Method),
			slog.String("url", r.URL.String()),
		)
		next.ServeHTTP(w, r)
	})
}

func CustomLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			log.Info("HTTP response",
				slog.String("method", r.Method),
				slog.String("url", r.URL.String()),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("duration", time.Since(start)),
				slog.String("remote", r.RemoteAddr),
			)
		})
	}
}
