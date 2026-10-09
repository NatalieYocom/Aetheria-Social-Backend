package delivery

import (
	"basisvr-social-service/internal/actorcrypto"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"basisvr-social-service/internal/activitypub/legacysig"
	"basisvr-social-service/internal/activitypub/messagesig"
	"basisvr-social-service/internal/activitypub/resolver"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type Job struct {
	ID             uuid.UUID
	ActivityID     uuid.UUID
	TargetInboxURL string
	Attempts       int
	RawJSON        []byte
	ActivityURI    string
	ActorURI       string
	PrivateKeyPEM  string
}

type Worker struct {
	db                    *sql.DB
	client                *http.Client
	maxAttempts           int
	actorKeyEncryptionKey string
}

const blockedDomainExistsSQL = `
SELECT EXISTS (
  SELECT 1
  FROM domain_blocks
  WHERE domain = $1 AND severity IN ('suspend', 'reject_all')
)`

func NewWorker(db *sql.DB, client *http.Client, maxAttempts int, actorKeyEncryptionKey string) Worker {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	clientCopy := *client
	transport := clientCopy.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clientCopy.Transport = otelhttp.NewTransport(transport)
	if maxAttempts <= 0 {
		maxAttempts = 6
	}
	return Worker{actorKeyEncryptionKey: actorKeyEncryptionKey, db: db, client: &clientCopy, maxAttempts: maxAttempts}
}

func (w Worker) DeliverDueOne(ctx context.Context) (bool, error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	job, err := loadDueJob(ctx, tx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.Commit(); err != nil {
				return false, err
			}
			return false, nil
		}
		return false, err
	}

	blocked, err := isTargetDomainBlocked(ctx, tx, job.TargetInboxURL)
	if err != nil {
		return false, err
	}
	if blocked {
		if err := markFailed(ctx, tx, job, fmt.Errorf("target inbox domain is blocked")); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}

	job.PrivateKeyPEM, err = actorcrypto.Decrypt(w.actorKeyEncryptionKey, job.ActorURI, job.PrivateKeyPEM)
	if err != nil {
		return false, err
	} // Keep the queued job intact for an operator to repair configuration/migrate keys.
	req, err := NewSignedActivityRequest(ctx, job.TargetInboxURL, job.ActorURI, job.PrivateKeyPEM, job.RawJSON)
	if err != nil {
		if updateErr := markFailedOrRetry(ctx, tx, job, w.maxAttempts, err); updateErr != nil {
			return false, updateErr
		}
		return false, err
	}

	res, err := w.client.Do(req)
	if err != nil {
		if updateErr := markFailedOrRetry(ctx, tx, job, w.maxAttempts, err); updateErr != nil {
			return false, updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, commitErr
		}
		return true, nil
	}
	if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnauthorized {
		_ = res.Body.Close()
		modernRequest, signErr := NewMessageSignedActivityRequest(ctx, job.TargetInboxURL, job.ActorURI, job.PrivateKeyPEM, job.RawJSON)
		if signErr != nil {
			if updateErr := markFailedOrRetry(ctx, tx, job, w.maxAttempts, signErr); updateErr != nil {
				return false, updateErr
			}
			return false, signErr
		}
		res, err = w.client.Do(modernRequest)
		if err != nil {
			if updateErr := markFailedOrRetry(ctx, tx, job, w.maxAttempts, err); updateErr != nil {
				return false, updateErr
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return false, commitErr
			}
			return true, nil
		}
	}
	defer res.Body.Close()

	if res.StatusCode >= 200 && res.StatusCode <= 299 {
		if _, err := tx.ExecContext(ctx, `
UPDATE outbox_jobs
SET state = 'delivered', attempts = attempts + 1, last_error = NULL
WHERE id = $1`, job.ID); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}

	if err := markFailedOrRetry(ctx, tx, job, w.maxAttempts, fmt.Errorf("remote inbox returned status %d", res.StatusCode)); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func isTargetDomainBlocked(ctx context.Context, tx *sql.Tx, inboxURL string) (bool, error) {
	parsed, err := url.Parse(inboxURL)
	if err != nil {
		return false, err
	}
	domain := strings.ToLower(parsed.Hostname())
	if domain == "" {
		return false, nil
	}
	var blocked bool
	err = tx.QueryRowContext(ctx, blockedDomainExistsSQL, domain).Scan(&blocked)
	return blocked, err
}

func loadDueJob(ctx context.Context, tx *sql.Tx) (Job, error) {
	row := tx.QueryRowContext(ctx, `
SELECT j.id, j.activity_id, j.target_inbox_url, j.attempts,
       a.raw_json, a.activity_uri, actor.actor_uri, actor.private_key_pem_encrypted
FROM outbox_jobs j
JOIN activities a ON a.id = j.activity_id
JOIN actors actor ON actor.id = a.actor_id
WHERE j.state IN ('pending', 'retry')
  AND j.next_retry_at <= now()
ORDER BY j.created_at
LIMIT 1
FOR UPDATE SKIP LOCKED`)

	var job Job
	var privateKey sql.NullString
	if err := row.Scan(
		&job.ID,
		&job.ActivityID,
		&job.TargetInboxURL,
		&job.Attempts,
		&job.RawJSON,
		&job.ActivityURI,
		&job.ActorURI,
		&privateKey,
	); err != nil {
		return Job{}, err
	}
	if privateKey.Valid {
		job.PrivateKeyPEM = privateKey.String
	}
	return job, nil
}

func markFailedOrRetry(ctx context.Context, tx *sql.Tx, job Job, maxAttempts int, cause error) error {
	nextAttempts := job.Attempts + 1
	state := "retry"
	if nextAttempts >= maxAttempts {
		state = "failed"
	}
	_, err := tx.ExecContext(ctx, `
UPDATE outbox_jobs
SET state = $2,
    attempts = attempts + 1,
    next_retry_at = now() + ($3::text)::interval,
    last_error = $4
WHERE id = $1`,
		job.ID,
		state,
		retryDelay(nextAttempts),
		cause.Error(),
	)
	return err
}

func markFailed(ctx context.Context, tx *sql.Tx, job Job, cause error) error {
	_, err := tx.ExecContext(ctx, `
UPDATE outbox_jobs
SET state = 'failed',
    attempts = attempts + 1,
    last_error = $2
WHERE id = $1`,
		job.ID,
		cause.Error(),
	)
	return err
}

func retryDelay(attempts int) string {
	switch attempts {
	case 1:
		return "1 minute"
	case 2:
		return "5 minutes"
	case 3:
		return "30 minutes"
	case 4:
		return "2 hours"
	default:
		return "12 hours"
	}
}

func NewSignedActivityRequest(ctx context.Context, inboxURL string, actorURI string, privateKeyPEM string, body []byte) (*http.Request, error) {
	if !resolver.IsSafeRemoteURL(inboxURL) {
		return nil, fmt.Errorf("blocked target inbox URL: %s", inboxURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inboxURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/activity+json")
	req.Header.Set("User-Agent", "BasisVR-Social-Service/0.1")
	if err := SignLegacyRequest(req, actorURI, privateKeyPEM, body, time.Now()); err != nil {
		return nil, err
	}
	return req, nil
}

func NewMessageSignedActivityRequest(ctx context.Context, inboxURL string, actorURI string, privateKeyPEM string, body []byte) (*http.Request, error) {
	if !resolver.IsSafeRemoteURL(inboxURL) {
		return nil, fmt.Errorf("blocked target inbox URL: %s", inboxURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inboxURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/activity+json")
	req.Header.Set("User-Agent", "BasisVR-Social-Service/0.1")
	if err := messagesig.SignRequestPEM(req, body, actorURI+"#main-key", privateKeyPEM, time.Now()); err != nil {
		return nil, err
	}
	return req, nil
}

func SignLegacyRequest(req *http.Request, actorURI string, privateKeyPEM string, body []byte, now time.Time) error {
	return legacysig.SignRequest(req, actorURI, privateKeyPEM, body, now)
}
