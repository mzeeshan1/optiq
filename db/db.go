package db

import (
	"context"
	"database/sql"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Advisory lock keys, so several replicas starting at once don't race each
// other through migrations and seeding.
const (
	migrateLockKey = 7_301_001
	seedLockKey    = 7_301_002
)

func Connect(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return db, db.Ping()
}

func Migrate(db *sql.DB) error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrateLockKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	return tx.Commit()
}

const schema = `
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE IF NOT EXISTS users (
	id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
	email         TEXT        UNIQUE NOT NULL,
	password_hash TEXT        NOT NULL,
	first_name    TEXT        NOT NULL DEFAULT '',
	last_name     TEXT        NOT NULL DEFAULT '',
	phone         TEXT        NOT NULL DEFAULT '',
	street        TEXT        NOT NULL DEFAULT '',
	postal_code   TEXT        NOT NULL DEFAULT '',
	city          TEXT        NOT NULL DEFAULT '',
	country       TEXT        NOT NULL DEFAULT 'DE',
	created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS categories (
	id          SERIAL PRIMARY KEY,
	slug        TEXT   UNIQUE NOT NULL,
	name        TEXT   NOT NULL,
	description TEXT   NOT NULL DEFAULT '',
	kind        TEXT   NOT NULL,
	position    INT    NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS brands (
	id      SERIAL  PRIMARY KEY,
	slug    TEXT    UNIQUE NOT NULL,
	name    TEXT    NOT NULL,
	country TEXT    NOT NULL DEFAULT '',
	premium BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS products (
	id               UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
	sku              TEXT          UNIQUE NOT NULL,
	slug             TEXT          UNIQUE NOT NULL,
	name             TEXT          NOT NULL,
	description      TEXT          NOT NULL DEFAULT '',
	category_id      INT           NOT NULL REFERENCES categories(id),
	brand_id         INT           NOT NULL REFERENCES brands(id),
	gender           TEXT          NOT NULL DEFAULT 'unisex',
	frame_shape      TEXT          NOT NULL DEFAULT '',
	frame_material   TEXT          NOT NULL DEFAULT '',
	color            TEXT          NOT NULL DEFAULT '',
	color_hex        TEXT          NOT NULL DEFAULT '#333333',
	lens_width       INT           NOT NULL DEFAULT 0,
	bridge_width     INT           NOT NULL DEFAULT 0,
	temple_length    INT           NOT NULL DEFAULT 0,
	price            NUMERIC(10,2) NOT NULL CHECK (price >= 0),
	compare_at_price NUMERIC(10,2),
	stock            INT           NOT NULL DEFAULT 0 CHECK (stock >= 0),
	rating_avg       NUMERIC(3,2)  NOT NULL DEFAULT 0,
	review_count     INT           NOT NULL DEFAULT 0,
	is_active        BOOLEAN       NOT NULL DEFAULT TRUE,
	created_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
	updated_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_products_category ON products(category_id);
CREATE INDEX IF NOT EXISTS idx_products_brand    ON products(brand_id);
CREATE INDEX IF NOT EXISTS idx_products_price    ON products(price);
CREATE INDEX IF NOT EXISTS idx_products_created  ON products(created_at DESC);

CREATE TABLE IF NOT EXISTS reviews (
	id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
	product_id UUID        NOT NULL REFERENCES products(id) ON DELETE CASCADE,
	user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	rating     SMALLINT    NOT NULL CHECK (rating BETWEEN 1 AND 5),
	title      TEXT        NOT NULL DEFAULT '',
	body       TEXT        NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (product_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_reviews_product ON reviews(product_id, created_at DESC);

CREATE TABLE IF NOT EXISTS cart_items (
	id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
	user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	product_id  UUID        NOT NULL REFERENCES products(id) ON DELETE CASCADE,
	lens_option TEXT        NOT NULL DEFAULT 'none',
	quantity    INT         NOT NULL CHECK (quantity > 0),
	created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (user_id, product_id, lens_option)
);

CREATE TABLE IF NOT EXISTS wishlist_items (
	user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	product_id UUID        NOT NULL REFERENCES products(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	PRIMARY KEY (user_id, product_id)
);

CREATE SEQUENCE IF NOT EXISTS order_number_seq START 100001;

CREATE TABLE IF NOT EXISTS orders (
	id               UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
	order_number     TEXT          UNIQUE NOT NULL,
	user_id          UUID          NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	status           TEXT          NOT NULL DEFAULT 'paid',
	subtotal         NUMERIC(10,2) NOT NULL,
	shipping         NUMERIC(10,2) NOT NULL,
	total            NUMERIC(10,2) NOT NULL,
	payment_method   TEXT          NOT NULL,
	ship_name        TEXT          NOT NULL,
	ship_street      TEXT          NOT NULL,
	ship_postal_code TEXT          NOT NULL,
	ship_city        TEXT          NOT NULL,
	ship_country     TEXT          NOT NULL,
	tracking_number  TEXT          NOT NULL DEFAULT '',
	created_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
	updated_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
	shipped_at       TIMESTAMPTZ,
	delivered_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_orders_user   ON orders(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status, updated_at);

CREATE TABLE IF NOT EXISTS order_items (
	id           UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
	order_id     UUID          NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
	product_id   UUID          NOT NULL REFERENCES products(id),
	product_name TEXT          NOT NULL,
	sku          TEXT          NOT NULL,
	lens_option  TEXT          NOT NULL DEFAULT 'none',
	unit_price   NUMERIC(10,2) NOT NULL,
	lens_price   NUMERIC(10,2) NOT NULL DEFAULT 0,
	quantity     INT           NOT NULL CHECK (quantity > 0)
);

CREATE INDEX IF NOT EXISTS idx_order_items_order   ON order_items(order_id);
CREATE INDEX IF NOT EXISTS idx_order_items_product ON order_items(product_id);

CREATE TABLE IF NOT EXISTS stores (
	id            SERIAL PRIMARY KEY,
	slug          TEXT   UNIQUE NOT NULL,
	name          TEXT   NOT NULL,
	street        TEXT   NOT NULL,
	postal_code   TEXT   NOT NULL,
	city          TEXT   NOT NULL,
	phone         TEXT   NOT NULL,
	opening_hours TEXT   NOT NULL
);

CREATE TABLE IF NOT EXISTS appointments (
	id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
	user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	store_id     INT         NOT NULL REFERENCES stores(id),
	service      TEXT        NOT NULL,
	scheduled_at TIMESTAMPTZ NOT NULL,
	status       TEXT        NOT NULL DEFAULT 'booked',
	notes        TEXT        NOT NULL DEFAULT '',
	created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_appointments_user ON appointments(user_id, scheduled_at);
-- One optician per store per slot: a second booking for the same slot is rejected.
CREATE UNIQUE INDEX IF NOT EXISTS uq_appointments_slot ON appointments(store_id, scheduled_at) WHERE status = 'booked';
`
