package cache

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
	"optiq/metrics"
)

type Cache struct {
	client *redis.Client
}

func New(redisURL string) (*Cache, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	c := redis.NewClient(opts)
	if err := c.Ping(context.Background()).Err(); err != nil {
		return nil, err
	}
	return &Cache{client: c}, nil
}

func (c *Cache) Ping(ctx context.Context) error { return c.client.Ping(ctx).Err() }

// BlacklistToken marks a JWT as invalid until expiry (used on logout).
func (c *Cache) BlacklistToken(ctx context.Context, token string, ttl time.Duration) error {
	return c.client.Set(ctx, "bl:"+token, 1, ttl).Err()
}

func (c *Cache) IsTokenBlacklisted(ctx context.Context, token string) (bool, error) {
	n, err := c.client.Exists(ctx, "bl:"+token).Result()
	return n > 0, err
}

// IncrLoginAttempts increments the rate-limit counter for an IP and returns the new count.
// The window is fixed: the TTL is only set by the first attempt, so a steady stream of
// retries cannot keep an IP locked out forever.
func (c *Cache) IncrLoginAttempts(ctx context.Context, ip string) (int64, error) {
	key := "rl:login:" + ip
	n, err := c.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	if n == 1 {
		c.client.Expire(ctx, key, time.Minute)
	}
	return n, nil
}

// GetJSON loads a cached value into dst. kind is the key prefix and doubles as
// the metrics label, so hit rates can be compared per cache.
func (c *Cache) GetJSON(ctx context.Context, kind, key string, dst any) bool {
	data, err := c.client.Get(ctx, kind+":"+key).Bytes()
	if err != nil || json.Unmarshal(data, dst) != nil {
		metrics.CacheRequestsTotal.WithLabelValues(kind, "miss").Inc()
		return false
	}
	metrics.CacheRequestsTotal.WithLabelValues(kind, "hit").Inc()
	return true
}

func (c *Cache) SetJSON(ctx context.Context, kind, key string, v any, ttl time.Duration) {
	if data, err := json.Marshal(v); err == nil {
		c.client.Set(ctx, kind+":"+key, data, ttl)
	}
}

func (c *Cache) Delete(ctx context.Context, kind string, keys ...string) {
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = kind + ":" + k
	}
	if len(full) > 0 {
		c.client.Del(ctx, full...)
	}
}

// trendingKey buckets product views per UTC day; yesterday's bucket is kept
// so the "trending" shelf isn't empty just after midnight.
func trendingKey(t time.Time) string { return "trending:" + t.UTC().Format("20060102") }

func (c *Cache) RecordProductView(ctx context.Context, productID string) {
	key := trendingKey(time.Now())
	pipe := c.client.Pipeline()
	pipe.ZIncrBy(ctx, key, 1, productID)
	pipe.Expire(ctx, key, 48*time.Hour)
	pipe.Exec(ctx)
}

func (c *Cache) TrendingProducts(ctx context.Context, n int) ([]string, error) {
	now := time.Now()
	dst := "trending:merged:" + now.UTC().Format("2006010215")
	keys := []string{trendingKey(now), trendingKey(now.Add(-24 * time.Hour))}
	pipe := c.client.Pipeline()
	pipe.ZUnionStore(ctx, dst, &redis.ZStore{Keys: keys, Weights: []float64{1, 0.5}})
	pipe.Expire(ctx, dst, 5*time.Minute)
	ids := pipe.ZRevRange(ctx, dst, 0, int64(n-1))
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	return ids.Val(), nil
}

// PushRecentlyViewed keeps the last 8 distinct products a user looked at.
func (c *Cache) PushRecentlyViewed(ctx context.Context, userID, productID string) {
	key := "recent:" + userID
	pipe := c.client.Pipeline()
	pipe.LRem(ctx, key, 0, productID)
	pipe.LPush(ctx, key, productID)
	pipe.LTrim(ctx, key, 0, 7)
	pipe.Expire(ctx, key, 30*24*time.Hour)
	pipe.Exec(ctx)
}

func (c *Cache) RecentlyViewed(ctx context.Context, userID string) ([]string, error) {
	return c.client.LRange(ctx, "recent:"+userID, 0, -1).Result()
}
