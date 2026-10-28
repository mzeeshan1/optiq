package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests by method, path, and status code.",
		},
		[]string{"method", "path", "status"},
	)

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency by method and path.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	SignupsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shop_signups_total",
		Help: "Total customer accounts created.",
	})

	LoginsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "shop_logins_total",
			Help: "Login attempts by result (success, failure).",
		},
		[]string{"result"},
	)

	ProductViewsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "shop_product_views_total",
			Help: "Product detail page views by category.",
		},
		[]string{"category"},
	)

	SearchesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "shop_searches_total",
			Help: "Catalog searches by whether they returned results.",
		},
		[]string{"result"},
	)

	CartAdditionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "shop_cart_additions_total",
			Help: "Items added to carts by category.",
		},
		[]string{"category"},
	)

	OrdersTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "shop_orders_total",
			Help: "Orders placed by payment method.",
		},
		[]string{"payment_method"},
	)

	OrderRevenueTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shop_order_revenue_euros_total",
		Help: "Gross revenue of placed orders in euros.",
	})

	OrderValue = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "shop_order_value_euros",
		Help:    "Distribution of order totals in euros.",
		Buckets: []float64{10, 25, 50, 100, 150, 200, 300, 500, 800, 1200},
	})

	CheckoutFailuresTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "shop_checkout_failures_total",
			Help: "Failed checkouts by reason.",
		},
		[]string{"reason"},
	)

	OrderCancellationsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shop_order_cancellations_total",
		Help: "Orders cancelled by customers.",
	})

	OrderStatusTransitionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "shop_order_status_transitions_total",
			Help: "Orders moved to a new status by the fulfilment worker.",
		},
		[]string{"status"},
	)

	AppointmentsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "shop_appointments_booked_total",
			Help: "In-store appointments booked by service.",
		},
		[]string{"service"},
	)

	ReviewsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shop_reviews_total",
		Help: "Product reviews written or updated.",
	})

	CacheRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "shop_cache_requests_total",
			Help: "Redis cache lookups by cache and result (hit, miss).",
		},
		[]string{"cache", "result"},
	)

	LowStockProducts = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "shop_low_stock_products",
		Help: "Active products with five or fewer units in stock.",
	})

	RestockedUnitsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shop_restocked_units_total",
		Help: "Units added back to stock by the replenishment worker.",
	})
)

func init() {
	prometheus.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDuration,
		SignupsTotal,
		LoginsTotal,
		ProductViewsTotal,
		SearchesTotal,
		CartAdditionsTotal,
		OrdersTotal,
		OrderRevenueTotal,
		OrderValue,
		CheckoutFailuresTotal,
		OrderCancellationsTotal,
		OrderStatusTransitionsTotal,
		AppointmentsTotal,
		ReviewsTotal,
		CacheRequestsTotal,
		LowStockProducts,
		RestockedUnitsTotal,
	)
}
