// Package valkeykv binds stream.Client to a Valkey server (TLS 1.3 unless
// plaintext is allowed for development). It is exercised by the integration
// suite against a real server; unit tests use stream.Memory.
package valkeykv

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/stream"
)

// Config connects to Valkey. TLS is required unless AllowPlaintext.
type Config struct {
	Addresses      []string
	Username       string
	Password       string
	AllowPlaintext bool
	CAPEM          []byte
}

type client struct{ c valkey.Client }

// New returns a Client backed by Valkey (TLS 1.3 enforced when TLS is used).
func New(cfg Config) (stream.Client, error) {
	if len(cfg.Addresses) == 0 {
		return nil, errors.New("valkey: addresses required")
	}
	opt := valkey.ClientOption{InitAddress: cfg.Addresses, Username: cfg.Username, Password: cfg.Password, DisableCache: true}
	if !cfg.AllowPlaintext {
		opt.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13}
		if len(cfg.CAPEM) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(cfg.CAPEM) {
				return nil, errors.New("valkey: ca is not valid PEM")
			}
			opt.TLSConfig.RootCAs = pool
		}
	}
	c, err := valkey.NewClient(opt)
	if err != nil {
		return nil, fmt.Errorf("valkey: %w", err)
	}
	return &client{c: c}, nil
}

func (v *client) XAdd(ctx context.Context, key string, fields map[string]string, maxLen int64) (string, error) {
	b := v.c.B().Xadd().Key(key).Maxlen().Almost().Threshold(strconv.FormatInt(maxLen, 10)).Id("*").FieldValue()
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b = b.FieldValue(k, fields[k])
	}
	return v.c.Do(ctx, b.Build()).ToString()
}

func (v *client) XRead(ctx context.Context, key, afterID string, block time.Duration, count int64) ([]stream.Entry, error) {
	if afterID == "" {
		afterID = "$"
	}
	res, err := v.c.Do(ctx, v.c.B().Xread().Count(count).Block(block.Milliseconds()).Streams().Key(key).Id(afterID).Build()).AsXRead()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, nil
		}
		return nil, err
	}
	return convert(res[key]), nil
}

func (v *client) XRange(ctx context.Context, key, afterID string, count int64) ([]stream.Entry, error) {
	start := "-"
	if afterID != "" {
		start = "(" + afterID
	}
	res, err := v.c.Do(ctx, v.c.B().Xrange().Key(key).Start(start).End("+").Count(count).Build()).AsXRange()
	if err != nil {
		return nil, err
	}
	return convert(res), nil
}

func convert(in []valkey.XRangeEntry) []stream.Entry {
	out := make([]stream.Entry, 0, len(in))
	for _, e := range in {
		out = append(out, stream.Entry{ID: e.ID, Fields: e.FieldValues})
	}
	return out
}

func (v *client) XLast(ctx context.Context, key string) (string, error) {
	res, err := v.c.Do(ctx, v.c.B().Xrevrange().Key(key).End("+").Start("-").Count(1).Build()).AsXRange()
	if err != nil {
		return "", err
	}
	if len(res) == 0 {
		return "", nil
	}
	return res[0].ID, nil
}

func (v *client) XTrimMinID(ctx context.Context, key, minID string) error {
	return v.c.Do(ctx, v.c.B().Xtrim().Key(key).Minid().Threshold(minID).Build()).Error()
}

func (v *client) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	n, err := v.c.Do(ctx, v.c.B().Incr().Key(key).Build()).AsInt64()
	if err != nil {
		return 0, err
	}
	if n == 1 && ttl > 0 {
		_ = v.c.Do(ctx, v.c.B().Pexpire().Key(key).Milliseconds(ttl.Milliseconds()).Build()).Error()
	}
	return n, nil
}

func (v *client) Ping(ctx context.Context) error {
	return v.c.Do(ctx, v.c.B().Ping().Build()).Error()
}

func (v *client) Close() { v.c.Close() }
