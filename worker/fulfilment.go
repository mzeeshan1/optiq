// Package worker plays the part of the warehouse: it moves paid orders through
// the workshop and out to the courier, and reorders stock that is running low.
package worker

import (
	"context"
	"database/sql"
	"log"
	"time"

	"optiq/metrics"
)

// Orders advance through these stages on a compressed clock so a demo shop
// shows the whole lifecycle within the hour rather than over a week.
var transitions = []struct {
	from, to string
	after    time.Duration
	query    string
}{
	{"paid", "processing", 2 * time.Minute, `
		UPDATE orders SET status = 'processing', updated_at = NOW()
		WHERE status = 'paid' AND updated_at < NOW() - $1::interval`},
	{"processing", "shipped", 10 * time.Minute, `
		UPDATE orders SET status = 'shipped', shipped_at = NOW(), updated_at = NOW(),
			tracking_number = '00340' || LPAD((FLOOR(RANDOM() * 1e13))::bigint::text, 13, '0')
		WHERE status = 'processing' AND updated_at < NOW() - $1::interval`},
	{"shipped", "delivered", 20 * time.Minute, `
		UPDATE orders SET status = 'delivered', delivered_at = NOW(), updated_at = NOW()
		WHERE status = 'shipped' AND updated_at < NOW() - $1::interval`},
}

const (
	restockBelow = 3
	restockUnits = 40
)

type Worker struct {
	db       *sql.DB
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

func New(db *sql.DB, interval time.Duration) *Worker {
	return &Worker{db: db, interval: interval, done: make(chan struct{})}
}

func (w *Worker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		w.run(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.run(ctx)
			}
		}
	}()
	log.Printf("fulfilment worker started (every %s)", w.interval)
}

func (w *Worker) Stop() {
	if w.cancel != nil {
		w.cancel()
		<-w.done
	}
}

func (w *Worker) run(ctx context.Context) {
	for _, t := range transitions {
		res, err := w.db.ExecContext(ctx, t.query, t.after.String())
		if err != nil {
			log.Printf("fulfilment: %s -> %s: %v", t.from, t.to, err)
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			metrics.OrderStatusTransitionsTotal.WithLabelValues(t.to).Add(float64(n))
		}
	}

	// Supplier deliveries: anything nearly sold out gets a fresh batch, so
	// sustained traffic never empties the catalog.
	var restocked sql.NullInt64
	err := w.db.QueryRowContext(ctx, `
		WITH r AS (
			UPDATE products SET stock = stock + $2, updated_at = NOW()
			WHERE is_active AND stock < $1 AND RANDOM() < 0.25
			RETURNING 1
		) SELECT COUNT(*) * $2 FROM r`, restockBelow, restockUnits).Scan(&restocked)
	if err != nil {
		log.Printf("fulfilment: restock: %v", err)
	} else if restocked.Int64 > 0 {
		metrics.RestockedUnitsTotal.Add(float64(restocked.Int64))
	}

	var low int
	if err := w.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM products WHERE is_active AND stock <= 5`).Scan(&low); err == nil {
		metrics.LowStockProducts.Set(float64(low))
	}

	if _, err := w.db.ExecContext(ctx, `
		UPDATE appointments SET status = 'completed'
		WHERE status = 'booked' AND scheduled_at < NOW() - INTERVAL '1 hour'`); err != nil {
		log.Printf("fulfilment: appointments: %v", err)
	}
}
