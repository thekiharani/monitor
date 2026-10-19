package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/cloud/httpx"
	"github.com/thekiharani/monitor/internal/cloud/usage"
)

func eToHTTP(code string) httpx.Error { return httpx.NewError(code) }

func readBody(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func formOrJSONBody(w http.ResponseWriter, r *http.Request, fields map[string]string) ([]byte, error) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		return json.Marshal(fieldsFromForm(r, fields))
	}
	return readBody(w, r, 64<<10)
}

func boundedFormValue(w http.ResponseWriter, r *http.Request, name string) (string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		if err := r.ParseMultipartForm(64 << 10); err != nil {
			return "", err
		}
	} else if err := r.ParseForm(); err != nil {
		return "", err
	}
	return r.FormValue(name), nil
}

func fieldsFromForm(r *http.Request, fields map[string]string) map[string]string {
	out := make(map[string]string, len(fields))
	for k := range fields {
		out[k] = r.FormValue(k)
	}
	return out
}

func decode(data []byte, dst any) error {
	return json.UnmarshalRead(bytes.NewReader(data), dst, json.RejectUnknownMembers(true))
}

func normalizedEmail(v string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	if len(s) > 254 {
		return "", fmt.Errorf("email")
	}
	m, e := mail.ParseAddress(s)
	if e != nil || m.Address != s || !strings.Contains(s, "@") {
		return "", fmt.Errorf("email")
	}
	return s, nil
}

func ascii(v string) bool {
	for i := range len(v) {
		if v[i] < 0x20 || v[i] > 0x7e {
			return false
		}
	}
	return true
}

func (a *App) checkLimit(key string, l usage.Limits) bool { return a.limiters.allow(key, l) }

func (a *App) incrementAPIUsage(ctx context.Context, aid uuid.UUID, max int64) (bool, error) {
	period := time.Now().UTC().Format("2006-01") + "-01"
	var ok bool
	err := a.withTenant(ctx, aid.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO account_monthly_usage(account_id,period,api_requests)
			 VALUES($1,$2::date,1)
			 ON CONFLICT(account_id,period)
			 DO UPDATE SET api_requests=account_monthly_usage.api_requests+1,updated_at=now()
			 WHERE account_monthly_usage.api_requests<$3
			 RETURNING true`,
			aid.String(), period, max,
		).Scan(&ok)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return ok, err
}

func bodyHasKey(body []byte, key string) bool {
	var present map[string]jsontext.Value
	if err := json.Unmarshal(body, &present); err != nil {
		return false
	}
	_, ok := present[key]
	return ok
}

func randomToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
