package handlers

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"optiq/metrics"
	"optiq/models"
)

type OrderHandler struct {
	*Base
}

func NewOrderHandler(b *Base) *OrderHandler {
	return &OrderHandler{Base: b}
}

func (h *OrderHandler) List(c *gin.Context) {
	rows, err := h.db.QueryContext(c.Request.Context(), `
		SELECT o.id, o.order_number, o.status, o.total, o.created_at, COALESCE(SUM(oi.quantity), 0)
		FROM orders o LEFT JOIN order_items oi ON oi.order_id = o.id
		WHERE o.user_id = $1
		GROUP BY o.id ORDER BY o.created_at DESC LIMIT 50`, c.GetString("userID"))
	if err != nil {
		h.serverError(c, err)
		return
	}
	defer rows.Close()

	var orders []models.Order
	for rows.Next() {
		var o models.Order
		if err := rows.Scan(&o.ID, &o.Number, &o.Status, &o.Total, &o.CreatedAt, &o.ItemCount); err != nil {
			h.serverError(c, err)
			return
		}
		orders = append(orders, o)
	}
	h.render(c, http.StatusOK, "orders.html", gin.H{"Title": "Your orders", "Orders": orders})
}

func (h *OrderHandler) Show(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	if !validID(id) {
		h.notFound(c)
		return
	}

	var o models.Order
	err := h.db.QueryRowContext(ctx, `
		SELECT id, order_number, status, subtotal, shipping, total, payment_method, ship_name, ship_street,
			ship_postal_code, ship_city, ship_country, tracking_number, created_at, shipped_at, delivered_at
		FROM orders WHERE id = $1 AND user_id = $2`, id, c.GetString("userID"),
	).Scan(&o.ID, &o.Number, &o.Status, &o.Subtotal, &o.Shipping, &o.Total, &o.PaymentMethod, &o.ShipName,
		&o.ShipStreet, &o.ShipPostalCode, &o.ShipCity, &o.ShipCountry, &o.TrackingNumber, &o.CreatedAt,
		&o.ShippedAt, &o.DeliveredAt)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(c)
		return
	}
	if err != nil {
		h.serverError(c, err)
		return
	}

	rows, err := h.db.QueryContext(ctx, `
		SELECT oi.product_id, p.slug, oi.product_name, oi.sku, oi.lens_option, oi.unit_price, oi.lens_price,
			oi.quantity, c.kind, p.frame_shape, p.color_hex
		FROM order_items oi
		JOIN products p ON p.id = oi.product_id
		JOIN categories c ON c.id = p.category_id
		WHERE oi.order_id = $1`, o.ID)
	if err != nil {
		h.serverError(c, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var it models.OrderItem
		if err := rows.Scan(&it.ProductID, &it.ProductSlug, &it.ProductName, &it.SKU, &it.LensOption,
			&it.UnitPrice, &it.LensPrice, &it.Quantity, &it.CategoryKind, &it.FrameShape, &it.ColorHex); err != nil {
			h.serverError(c, err)
			return
		}
		opt, _ := models.LookupLensOption(it.CategoryKind, it.LensOption)
		it.LensLabel = opt.Label
		o.Items = append(o.Items, it)
	}

	h.render(c, http.StatusOK, "order.html", gin.H{
		"Title":  "Order " + o.Number,
		"Order":  o,
		"Placed": c.Query("placed") == "1",
		"Steps":  []string{"Paid", "In our workshop", "Shipped", "Delivered"},
	})
}

func (h *OrderHandler) Cancel(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	if !validID(id) {
		h.notFound(c)
		return
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.serverError(c, err)
		return
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		UPDATE orders SET status = 'cancelled', updated_at = NOW()
		WHERE id = $1 AND user_id = $2 AND status IN ('paid', 'processing')`, id, c.GetString("userID"))
	if err != nil {
		h.serverError(c, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists bool
		tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM orders WHERE id = $1 AND user_id = $2)`,
			id, c.GetString("userID")).Scan(&exists)
		if !exists {
			h.notFound(c)
			return
		}
		h.fail(c, http.StatusConflict, "Can't cancel this order", "This order has already shipped. You can return it free of charge within 30 days of delivery.")
		return
	}

	// Put the reserved units back on the shelf.
	if _, err := tx.ExecContext(ctx, `
		UPDATE products p SET stock = p.stock + oi.qty, updated_at = NOW()
		FROM (SELECT product_id, SUM(quantity) AS qty FROM order_items WHERE order_id = $1 GROUP BY product_id) oi
		WHERE p.id = oi.product_id`, id); err != nil {
		h.serverError(c, err)
		return
	}
	if err := tx.Commit(); err != nil {
		h.serverError(c, err)
		return
	}

	metrics.OrderCancellationsTotal.Inc()
	c.Redirect(http.StatusFound, "/orders/"+id)
}
