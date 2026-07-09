package observability

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"basisvr-social-service/internal/common/httpx"

	"github.com/redis/go-redis/v9"
)

type Check struct {
	Name  string
	Check func(context.Context) error
}

type ReadinessConfig struct {
	Timeout time.Duration
	Checks  []Check
}

type ReadinessResponse struct {
	Status string                        `json:"status"`
	Checks map[string]ReadinessCheckBody `json:"checks"`
}

type ReadinessCheckBody struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func ReadinessHandler(config ReadinessConfig) http.Handler {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		body := ReadinessResponse{
			Status: "ok",
			Checks: map[string]ReadinessCheckBody{},
		}
		statusCode := http.StatusOK
		for _, check := range config.Checks {
			if check.Name == "" || check.Check == nil {
				continue
			}
			if err := check.Check(ctx); err != nil {
				body.Status = "degraded"
				statusCode = http.StatusServiceUnavailable
				body.Checks[check.Name] = ReadinessCheckBody{Status: "error", Error: err.Error()}
				continue
			}
			body.Checks[check.Name] = ReadinessCheckBody{Status: "ok"}
		}
		httpx.WriteJSON(w, statusCode, body)
	})
}

func DatabaseCheck(db *sql.DB) Check {
	return Check{
		Name: "postgres",
		Check: func(ctx context.Context) error {
			return db.PingContext(ctx)
		},
	}
}

func NewRedisCheck(redisURL string) (Check, func() error, error) {
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		return Check{}, nil, err
	}
	client := redis.NewClient(options)
	check := Check{
		Name: "redis",
		Check: func(ctx context.Context) error {
			return client.Ping(ctx).Err()
		},
	}
	return check, client.Close, nil
}
