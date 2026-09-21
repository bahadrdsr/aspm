package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type assessmentPoolLimits struct {
	Lease, Authorization, Timeout, Window time.Duration
	Concurrent, Requests, Input, Output   int
	Response                              int64
}

func assessmentLimits(c AssessmentWorkerConfig) assessmentPoolLimits {
	return assessmentPoolLimits{c.LeaseDuration, c.AuthorizationInterval, c.RequestTimeout, c.RequestWindow,
		c.MaxConcurrent, c.RequestsPerWindow, c.MaxInputBytes, c.MaxOutputTokens, c.MaxResponseBytes}
}

func intervalMicros(duration time.Duration) int64 {
	return int64((duration + time.Microsecond - 1) / time.Microsecond)
}

// The singleton prevents another process or profile from creating a private
// allocation. Job dispatch timestamps, not refundable counters, spend requests.
func (db *database) registerAssessmentScope(ctx context.Context, scope string, limits *assessmentPoolLimits) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return errUnavailable
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `INSERT INTO `+db.table("assessment_quotas")+`
		(singleton,scope) VALUES(true,$1) ON CONFLICT(singleton) DO NOTHING`, scope); err != nil {
		return errUnavailable
	}
	var storedScope string
	var storedLimits []byte
	if err = tx.QueryRow(ctx, `SELECT scope,worker_limits FROM `+db.table("assessment_quotas")+`
		WHERE singleton FOR UPDATE`).Scan(&storedScope, &storedLimits); err != nil {
		return errUnavailable
	}
	conflict := errors.New("assessment pool scope or limits conflict")
	if storedScope != scope {
		return conflict
	}
	if limits != nil {
		if len(storedLimits) != 0 {
			var existing assessmentPoolLimits
			if json.Unmarshal(storedLimits, &existing) != nil || existing != *limits {
				return conflict
			}
		} else {
			raw, err := json.Marshal(limits)
			if err != nil {
				return errUnavailable
			}
			if _, err = tx.Exec(ctx, `UPDATE `+db.table("assessment_quotas")+`
				SET max_concurrent=$2,requests_per_window=$3,request_window_us=$4,worker_limits=$5
				WHERE singleton AND scope=$1`, scope, limits.Concurrent, limits.Requests,
				intervalMicros(limits.Window), raw); err != nil {
				return errUnavailable
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return errUnavailable
	}
	return nil
}
