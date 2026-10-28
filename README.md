# optiq — demo eyewear shop

An online optician in the style of a high-street eyewear chain: glasses, sunglasses, sports and kids' frames, contact lenses and accessories, plus a store finder and free eye-test booking. Built with the same stack and layout as `subscription-reminder`.

On first start it seeds its own catalog (1,000 products), and `loadgen` drives realistic shoppers against it to fill the database with customers, baskets, orders, reviews and appointments.

## Tech stack

- **Go + Gin**: HTTP server, server-rendered templates
- **PostgreSQL**: catalog, customers, carts, orders, reviews, stores, appointments
- **Redis**: JWT blacklist, login rate limiting, page/listing/product caches, "trending today" (sorted sets), recently viewed (lists)
- **Prometheus**: HTTP and business metrics at `/metrics`
- **Tailwind CSS**: UI (via CDN). Product images are SVG drawings generated from each frame's shape, material and colour.

## Quick start

```bash
docker compose up --build          # shop on http://localhost:8080
docker compose --profile loadgen up  # same, plus 20 simulated shoppers
```

First boot seeds the database (about 1 second). Log in as any seeded customer, e.g. look one up with
`docker compose exec postgres psql -U postgres optiq -c "select email from users limit 5"`. All seeded customers use the password `password123`.

## Running locally without Docker

```bash
createdb optiq
export DATABASE_URL=postgres://postgres:postgres@localhost:5432/optiq?sslmode=disable
export REDIS_URL=redis://localhost:6379
go run .
```

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | HTTP port |
| `DATABASE_URL` | `postgres://localhost/optiq?sslmode=disable` | Postgres DSN |
| `REDIS_URL` | `redis://localhost:6379` | Redis URL |
| `JWT_SECRET` | `change-me-in-production` | Session token signing key |
| `SEED_PRODUCTS` | `1000` | Products generated into an empty catalog (`0` disables seeding) |
| `SEED_CUSTOMERS` | `200` | Demo customers generated alongside, who write the seeded reviews |
| `FULFILMENT_INTERVAL` | `30s` | How often the fulfilment worker runs |

## Data model

| Table | Seeded with |
|---|---|
| `categories` | Glasses, Sunglasses, Sports glasses, Kids' glasses, Contact lenses, Accessories |
| `brands` | 19 invented brands across budget, mid-range and premium tiers |
| `products` | 1,000 products: 400 glasses, 250 sunglasses, 80 sports, 90 kids, 100 contact lenses, 80 accessories, each with shape, material, colour, measurements (e.g. `52□18-140`), price, sale price for ~15%, and stock |
| `users` | 200 customers with German names and addresses |
| `reviews` | ~6,500 reviews, popularity-skewed so a few products collect most of them |
| `stores` | 37 stores across 30 German cities |
| `cart_items`, `wishlist_items`, `orders`, `order_items`, `appointments` | Filled by real usage / `loadgen` |

Seeding uses a fixed random seed, so every fresh database has the same catalog. It only runs when `products` is empty, and both migration and seeding take a Postgres advisory lock so multiple replicas can start at once.

## Features

- **Catalog**: filter by category, gender, price band, shape, material, colour, brand and sale; multi-word search; five sort orders; pagination
- **Product pages**: lens options (frame only / single-vision €59 / blue-light €89 / varifocal €189; prescription sun lenses €99), stock levels, reviews, related products
- **Basket and checkout**: stock reserved atomically with row locks; free shipping over €49; simulated payments (any Luhn-valid card works, `4000 0000 0000 0002` is declined)
- **Orders**: tracking with progress; cancelling (before shipping) restores stock
- **Wishlist, reviews, profile, store finder, eye-test booking** (one booking per store per half-hour slot, Mon–Sat)
- **Fulfilment worker**: moves orders paid → processing (2 min) → shipped with a tracking number (10 min) → delivered (20 min), restocks products running low, completes past appointments

## Load generator

`loadgen` simulates people, not just requests. Each virtual shopper:

1. lands anonymously, browses categories with filters, searches and views products
2. signs up with a generated German identity (name, email at `example.com/.org/.net`, street, postcode, city), or logs back in as a returning customer from earlier in the run
3. fills a basket with lens options, checks out by card, PayPal or invoice, views the confirmation, and sometimes cancels
4. writes reviews, toggles wishlist items, books and cancels eye tests, updates their profile
5. leaves (sometimes logging out), and a new shopper takes their place

A configurable share of actions deliberately fail, the way they do in real life: declined cards, invalid checkout details, wrong passwords, double-booked slots, 404s, unauthenticated access.

```bash
go run ./loadgen -target http://localhost:8080 -users 20 -rps 15 -duration 10m
```

| Flag / env | Default | Purpose |
|---|---|---|
| `-target` / `TARGET` | `http://localhost:8080` | Shop URL |
| `-users` / `USERS` | `10` | Concurrent shoppers |
| `-rps` / `RPS` | `20` | Aggregate request rate |
| `-duration` / `DURATION` | `0` (until Ctrl-C) | Run time |
| `-error-rate` / `ERROR_RATE` | `0.04` | Share of deliberately failing actions |
| `-login-burst`, `-login-cooldown` | `8`, `65s` | Keeps logins under the shop's 10/min/IP limit |

It prints a progress line every 15s (latency percentiles, status classes, signups, orders) and a per-action breakdown on exit.

## Metrics

Besides `http_requests_total` and `http_request_duration_seconds`:

`shop_signups_total`, `shop_logins_total{result}`, `shop_product_views_total{category}`, `shop_searches_total{result}`, `shop_cart_additions_total{category}`, `shop_orders_total{payment_method}`, `shop_order_revenue_euros_total`, `shop_order_value_euros` (histogram), `shop_checkout_failures_total{reason}`, `shop_order_cancellations_total`, `shop_order_status_transitions_total{status}`, `shop_appointments_booked_total{service}`, `shop_reviews_total`, `shop_cache_requests_total{cache,result}`, `shop_low_stock_products`, `shop_restocked_units_total`.

## Kubernetes

```bash
docker build -t optiq .
docker build --target loadgen -t optiq-loadgen .
helm install optiq ./chart --set secrets.jwtSecret=$(openssl rand -hex 32) --set loadgen.enabled=true
```

The chart deploys the app (2 replicas), Postgres with a PVC, Redis, an Ingress, a ServiceMonitor for kube-prometheus-stack, and optionally the load generator.
