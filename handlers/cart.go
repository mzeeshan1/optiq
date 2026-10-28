package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"optiq/metrics"
	"optiq/models"
)

type CartHandler struct {
	*Base
}

func NewCartHandler(b *Base) *CartHandler {
	return &CartHandler{Base: b}
}

func (h *CartHandler) loadCart(c *gin.Context, userID string) (models.Cart, error) {
	rows, err := h.db.QueryContext(c.Request.Context(), `
		SELECT `+productCols+`, ci.id, ci.lens_option, ci.quantity
		FROM cart_items ci JOIN products p ON p.id = ci.product_id `+productJoins+`
		WHERE ci.user_id = $1 ORDER BY ci.created_at`, userID)
	if err != nil {
		return models.Cart{}, err
	}
	defer rows.Close()

	var items []models.CartItem
	for rows.Next() {
		var item models.CartItem
		var lens string
		item.Product, err = scanProduct(rows, &item.ID, &lens, &item.Quantity)
		if err != nil {
			return models.Cart{}, err
		}
		item.Lens, _ = models.LookupLensOption(item.Product.CategoryKind, lens)
		items = append(items, item)
	}
	return models.NewCart(items), rows.Err()
}

func (h *CartHandler) View(c *gin.Context) {
	cart, err := h.loadCart(c, c.GetString("userID"))
	if err != nil {
		h.serverError(c, err)
		return
	}
	h.render(c, http.StatusOK, "cart.html", gin.H{
		"Title":     "Your basket",
		"Cart":      cart,
		"Threshold": models.FreeShippingThreshold,
	})
}

func (h *CartHandler) Add(c *gin.Context) {
	ctx := c.Request.Context()
	userID := c.GetString("userID")
	productID := c.PostForm("product_id")
	lensCode := c.DefaultPostForm("lens_option", models.LensNone)
	qty, err := strconv.Atoi(c.DefaultPostForm("quantity", "1"))
	if err != nil || qty < 1 || qty > models.MaxLineQuantity {
		h.fail(c, http.StatusBadRequest, "Invalid quantity", fmt.Sprintf("You can add between 1 and %d of an item.", models.MaxLineQuantity))
		return
	}
	if !validID(productID) {
		h.notFound(c)
		return
	}

	var kind, category string
	var stock int
	err = h.db.QueryRowContext(ctx, `
		SELECT c.kind, c.slug, p.stock FROM products p JOIN categories c ON c.id = p.category_id
		WHERE p.id = $1 AND p.is_active`, productID).Scan(&kind, &category, &stock)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(c)
		return
	}
	if err != nil {
		h.serverError(c, err)
		return
	}
	if stock == 0 {
		h.fail(c, http.StatusConflict, "Sold out", "Sorry, this item just sold out. We restock most frames within a few days.")
		return
	}
	if _, ok := models.LookupLensOption(kind, lensCode); !ok {
		h.fail(c, http.StatusBadRequest, "Lens option unavailable", "That lens option isn't available for this product.")
		return
	}

	_, err = h.db.ExecContext(ctx, `
		INSERT INTO cart_items (user_id, product_id, lens_option, quantity) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, product_id, lens_option) DO UPDATE
		SET quantity = LEAST(cart_items.quantity + EXCLUDED.quantity, $5), updated_at = NOW()`,
		userID, productID, lensCode, qty, models.MaxLineQuantity)
	if err != nil {
		h.serverError(c, err)
		return
	}

	metrics.CartAdditionsTotal.WithLabelValues(category).Inc()
	c.Redirect(http.StatusFound, "/cart")
}

func (h *CartHandler) Update(c *gin.Context) {
	userID := c.GetString("userID")
	id := c.Param("id")
	qty, err := strconv.Atoi(c.PostForm("quantity"))
	if err != nil || qty < 0 || qty > models.MaxLineQuantity || !validID(id) {
		h.fail(c, http.StatusBadRequest, "Invalid quantity", fmt.Sprintf("Quantity must be between 0 and %d.", models.MaxLineQuantity))
		return
	}
	if qty == 0 {
		h.Remove(c)
		return
	}
	if _, err := h.db.ExecContext(c.Request.Context(),
		`UPDATE cart_items SET quantity = $1, updated_at = NOW() WHERE id = $2 AND user_id = $3`,
		qty, id, userID); err != nil {
		h.serverError(c, err)
		return
	}
	c.Redirect(http.StatusFound, "/cart")
}

func (h *CartHandler) Remove(c *gin.Context) {
	id := c.Param("id")
	if !validID(id) {
		h.notFound(c)
		return
	}
	if _, err := h.db.ExecContext(c.Request.Context(),
		`DELETE FROM cart_items WHERE id = $1 AND user_id = $2`, id, c.GetString("userID")); err != nil {
		h.serverError(c, err)
		return
	}
	c.Redirect(http.StatusFound, "/cart")
}

// ---------------------------------------------------------------------------
// checkout
// ---------------------------------------------------------------------------

type checkoutForm struct {
	Name, Street, PostalCode, City, Country, Payment, CardNumber string
	SaveAddress                                                  bool
}

var (
	shippingCountries = []struct{ Code, Name string }{
		{"DE", "Germany"}, {"AT", "Austria"}, {"CH", "Switzerland"}, {"NL", "Netherlands"},
		{"BE", "Belgium"}, {"LU", "Luxembourg"}, {"DK", "Denmark"}, {"FR", "France"},
	}
	postalPattern = regexp.MustCompile(`^[0-9A-Za-z -]{4,10}$`)
)

func (h *CartHandler) CheckoutPage(c *gin.Context) {
	userID := c.GetString("userID")
	cart, err := h.loadCart(c, userID)
	if err != nil {
		h.serverError(c, err)
		return
	}
	if len(cart.Items) == 0 {
		c.Redirect(http.StatusFound, "/cart")
		return
	}

	var u models.User
	h.db.QueryRowContext(c.Request.Context(), `
		SELECT first_name, last_name, street, postal_code, city, country FROM users WHERE id = $1`, userID,
	).Scan(&u.FirstName, &u.LastName, &u.Street, &u.PostalCode, &u.City, &u.Country)

	h.renderCheckout(c, http.StatusOK, cart, checkoutForm{
		Name: u.FullName(), Street: u.Street, PostalCode: u.PostalCode, City: u.City,
		Country: u.Country, Payment: "card", SaveAddress: u.Street == "",
	}, "")
}

func (h *CartHandler) renderCheckout(c *gin.Context, status int, cart models.Cart, form checkoutForm, errMsg string) {
	h.render(c, status, "checkout.html", gin.H{
		"Title":     "Checkout",
		"Cart":      cart,
		"Form":      form,
		"Countries": shippingCountries,
		"Payments":  models.PaymentMethods,
		"Error":     errMsg,
	})
}

func (h *CartHandler) Checkout(c *gin.Context) {
	ctx := c.Request.Context()
	userID := c.GetString("userID")

	form := checkoutForm{
		Name:        strings.TrimSpace(c.PostForm("name")),
		Street:      strings.TrimSpace(c.PostForm("street")),
		PostalCode:  strings.TrimSpace(c.PostForm("postal_code")),
		City:        strings.TrimSpace(c.PostForm("city")),
		Country:     c.PostForm("country"),
		Payment:     c.PostForm("payment_method"),
		CardNumber:  strings.NewReplacer(" ", "", "-", "").Replace(c.PostForm("card_number")),
		SaveAddress: c.PostForm("save_address") == "on",
	}

	cart, err := h.loadCart(c, userID)
	if err != nil {
		h.serverError(c, err)
		return
	}
	if len(cart.Items) == 0 {
		metrics.CheckoutFailuresTotal.WithLabelValues("empty_cart").Inc()
		c.Redirect(http.StatusFound, "/cart")
		return
	}

	if msg := form.validate(); msg != "" {
		metrics.CheckoutFailuresTotal.WithLabelValues("invalid_details").Inc()
		h.renderCheckout(c, http.StatusBadRequest, cart, form, msg)
		return
	}
	// Simulated payment provider: the well-known "declined" test card fails.
	if form.Payment == "card" && strings.HasSuffix(form.CardNumber, "0002") {
		metrics.CheckoutFailuresTotal.WithLabelValues("payment_declined").Inc()
		h.renderCheckout(c, http.StatusPaymentRequired, cart, form, "Your card was declined. Please use a different card or payment method.")
		return
	}

	order, slugs, soldOut, err := h.placeOrder(c, userID, form)
	if err != nil {
		h.serverError(c, err)
		return
	}
	if len(soldOut) > 0 {
		metrics.CheckoutFailuresTotal.WithLabelValues("out_of_stock").Inc()
		h.renderCheckout(c, http.StatusConflict, cart, form,
			"Sorry, not enough stock left for: "+strings.Join(soldOut, ", ")+". Please adjust your basket.")
		return
	}

	metrics.OrdersTotal.WithLabelValues(form.Payment).Inc()
	metrics.OrderRevenueTotal.Add(order.Total)
	metrics.OrderValue.Observe(order.Total)
	h.cache.Delete(ctx, "product", slugs...)
	c.Redirect(http.StatusFound, "/orders/"+order.ID+"?placed=1")
}

func (f checkoutForm) validate() string {
	switch {
	case f.Name == "" || f.Street == "" || f.City == "":
		return "Please fill in your name and full delivery address."
	case !postalPattern.MatchString(f.PostalCode):
		return "Please enter a valid postal code."
	}
	countryOK := false
	for _, c := range shippingCountries {
		countryOK = countryOK || c.Code == f.Country
	}
	if !countryOK {
		return "We don't deliver to that country yet."
	}
	switch f.Payment {
	case "paypal", "invoice":
		return ""
	case "card":
		if !luhnValid(f.CardNumber) {
			return "That card number doesn't look right."
		}
		return ""
	}
	return "Please choose a payment method."
}

// placeOrder reserves stock and turns the cart into an order atomically.
// Products are locked in id order so concurrent checkouts can't deadlock.
func (h *CartHandler) placeOrder(c *gin.Context, userID string, form checkoutForm) (models.Order, []string, []string, error) {
	ctx := c.Request.Context()
	var order models.Order

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return order, nil, nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT p.id, p.slug, p.name, p.sku, p.price, p.stock, c.kind, ci.lens_option, ci.quantity
		FROM cart_items ci
		JOIN products p ON p.id = ci.product_id
		JOIN categories c ON c.id = p.category_id
		WHERE ci.user_id = $1
		ORDER BY p.id
		FOR UPDATE OF p`, userID)
	if err != nil {
		return order, nil, nil, err
	}

	type line struct {
		productID, slug, name, sku, lens string
		price, lensPrice                 float64
		qty                              int
	}
	var lines []line
	stock := map[string]int{}
	need := map[string]int{}
	names := map[string]string{}
	for rows.Next() {
		var l line
		var kind string
		var s int
		if err := rows.Scan(&l.productID, &l.slug, &l.name, &l.sku, &l.price, &s, &kind, &l.lens, &l.qty); err != nil {
			rows.Close()
			return order, nil, nil, err
		}
		opt, _ := models.LookupLensOption(kind, l.lens)
		l.lensPrice = opt.Price
		lines = append(lines, l)
		stock[l.productID] = s
		need[l.productID] += l.qty
		names[l.productID] = l.name
		order.Subtotal += (l.price + l.lensPrice) * float64(l.qty)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return order, nil, nil, err
	}

	var soldOut []string
	for id, n := range need {
		if stock[id] < n {
			soldOut = append(soldOut, names[id])
		}
	}
	if len(soldOut) > 0 {
		return order, nil, soldOut, nil
	}

	var slugs []string
	for id, n := range need {
		if _, err := tx.ExecContext(ctx,
			`UPDATE products SET stock = stock - $1, updated_at = NOW() WHERE id = $2`, n, id); err != nil {
			return order, nil, nil, err
		}
	}

	order.Shipping = models.ShippingFor(order.Subtotal)
	order.Total = order.Subtotal + order.Shipping
	err = tx.QueryRowContext(ctx, `
		INSERT INTO orders (order_number, user_id, subtotal, shipping, total, payment_method,
			ship_name, ship_street, ship_postal_code, ship_city, ship_country)
		VALUES ('OQ-' || nextval('order_number_seq'), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, order_number`,
		userID, order.Subtotal, order.Shipping, order.Total, form.Payment,
		form.Name, form.Street, form.PostalCode, form.City, form.Country,
	).Scan(&order.ID, &order.Number)
	if err != nil {
		return order, nil, nil, err
	}

	for _, l := range lines {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO order_items (order_id, product_id, product_name, sku, lens_option, unit_price, lens_price, quantity)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			order.ID, l.productID, l.name, l.sku, l.lens, l.price, l.lensPrice, l.qty); err != nil {
			return order, nil, nil, err
		}
		slugs = append(slugs, l.slug)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM cart_items WHERE user_id = $1`, userID); err != nil {
		return order, nil, nil, err
	}

	if form.SaveAddress {
		if _, err := tx.ExecContext(ctx, `
			UPDATE users SET street = $1, postal_code = $2, city = $3, country = $4, updated_at = NOW()
			WHERE id = $5`, form.Street, form.PostalCode, form.City, form.Country, userID); err != nil {
			return order, nil, nil, err
		}
	}

	return order, slugs, nil, tx.Commit()
}

func luhnValid(number string) bool {
	if len(number) < 12 || len(number) > 19 {
		return false
	}
	sum := 0
	for i := 0; i < len(number); i++ {
		d := int(number[len(number)-1-i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if i%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}
