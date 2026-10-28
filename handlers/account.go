package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"optiq/metrics"
	"optiq/models"
)

type AccountHandler struct {
	*Base
	loc *time.Location
}

func NewAccountHandler(b *Base) *AccountHandler {
	// Store hours are local German time regardless of where the server runs.
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		loc = time.UTC
	}
	return &AccountHandler{Base: b, loc: loc}
}

// ---------------------------------------------------------------------------
// profile
// ---------------------------------------------------------------------------

func (h *AccountHandler) Profile(c *gin.Context) {
	ctx := c.Request.Context()
	userID := c.GetString("userID")

	var u models.User
	err := h.db.QueryRowContext(ctx, `
		SELECT email, first_name, last_name, phone, street, postal_code, city, country, created_at
		FROM users WHERE id = $1`, userID,
	).Scan(&u.Email, &u.FirstName, &u.LastName, &u.Phone, &u.Street, &u.PostalCode, &u.City, &u.Country, &u.CreatedAt)
	if err != nil {
		h.serverError(c, err)
		return
	}

	var orderCount int
	var spent float64
	h.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(total), 0) FROM orders WHERE user_id = $1 AND status <> 'cancelled'`, userID,
	).Scan(&orderCount, &spent)

	h.render(c, http.StatusOK, "account.html", gin.H{
		"Title":      "Your account",
		"User":       u,
		"OrderCount": orderCount,
		"Spent":      spent,
		"Saved":      c.Query("saved") == "1",
	})
}

func (h *AccountHandler) UpdateProfile(c *gin.Context) {
	first := strings.TrimSpace(c.PostForm("first_name"))
	if first == "" {
		h.fail(c, http.StatusBadRequest, "First name required", "Please tell us your first name.")
		return
	}
	_, err := h.db.ExecContext(c.Request.Context(), `
		UPDATE users SET first_name = $1, last_name = $2, phone = $3, street = $4, postal_code = $5, city = $6,
			updated_at = NOW()
		WHERE id = $7`,
		first, strings.TrimSpace(c.PostForm("last_name")), strings.TrimSpace(c.PostForm("phone")),
		strings.TrimSpace(c.PostForm("street")), strings.TrimSpace(c.PostForm("postal_code")),
		strings.TrimSpace(c.PostForm("city")), c.GetString("userID"))
	if err != nil {
		h.serverError(c, err)
		return
	}
	c.Redirect(http.StatusFound, "/account?saved=1")
}

// ---------------------------------------------------------------------------
// wishlist
// ---------------------------------------------------------------------------

func (h *AccountHandler) Wishlist(c *gin.Context) {
	products, err := h.queryProducts(c.Request.Context(), `
		SELECT `+productCols+` FROM wishlist_items w JOIN products p ON p.id = w.product_id `+productJoins+`
		WHERE w.user_id = $1 ORDER BY w.created_at DESC`, c.GetString("userID"))
	if err != nil {
		h.serverError(c, err)
		return
	}
	h.render(c, http.StatusOK, "wishlist.html", gin.H{"Title": "Wishlist", "Products": products})
}

// ToggleWishlist adds the product, or removes it when it's already saved.
func (h *AccountHandler) ToggleWishlist(c *gin.Context) {
	ctx := c.Request.Context()
	userID := c.GetString("userID")
	productID := c.Param("productID")
	if !validID(productID) {
		h.notFound(c)
		return
	}

	res, err := h.db.ExecContext(ctx, `DELETE FROM wishlist_items WHERE user_id = $1 AND product_id = $2`, userID, productID)
	if err != nil {
		h.serverError(c, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err := h.db.ExecContext(ctx, `
			INSERT INTO wishlist_items (user_id, product_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, productID)
		if pgCode(err) == "23503" {
			h.notFound(c)
			return
		}
		if err != nil {
			h.serverError(c, err)
			return
		}
	}
	c.Redirect(http.StatusFound, safeNext(c.PostForm("next"), "/wishlist"))
}

// ---------------------------------------------------------------------------
// stores & appointments
// ---------------------------------------------------------------------------

func (h *AccountHandler) loadStores(c *gin.Context, city string) ([]models.Store, error) {
	query := `SELECT id, slug, name, street, postal_code, city, phone, opening_hours FROM stores`
	var args []any
	if city != "" {
		query += ` WHERE city = $1`
		args = append(args, city)
	}
	rows, err := h.db.QueryContext(c.Request.Context(), query+` ORDER BY city, name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Store
	for rows.Next() {
		var s models.Store
		if err := rows.Scan(&s.ID, &s.Slug, &s.Name, &s.Street, &s.PostalCode, &s.City, &s.Phone, &s.OpeningHours); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (h *AccountHandler) Stores(c *gin.Context) {
	city := c.Query("city")
	stores, err := h.loadStores(c, city)
	if err != nil {
		h.serverError(c, err)
		return
	}
	all, err := h.loadStores(c, "")
	if err != nil {
		h.serverError(c, err)
		return
	}
	var cities []string
	for _, s := range all {
		if len(cities) == 0 || cities[len(cities)-1] != s.City {
			cities = append(cities, s.City)
		}
	}
	h.render(c, http.StatusOK, "stores.html", gin.H{
		"Title":  "Find a store",
		"Stores": stores,
		"Cities": cities,
		"City":   city,
	})
}

func (h *AccountHandler) Appointments(c *gin.Context) {
	h.renderAppointments(c, http.StatusOK, "")
}

func (h *AccountHandler) renderAppointments(c *gin.Context, status int, errMsg string) {
	rows, err := h.db.QueryContext(c.Request.Context(), `
		SELECT a.id, s.name, s.street, s.city, a.service, a.scheduled_at, a.status
		FROM appointments a JOIN stores s ON s.id = a.store_id
		WHERE a.user_id = $1 ORDER BY a.scheduled_at DESC LIMIT 30`, c.GetString("userID"))
	if err != nil {
		h.serverError(c, err)
		return
	}
	defer rows.Close()
	var appts []models.Appointment
	for rows.Next() {
		var a models.Appointment
		if err := rows.Scan(&a.ID, &a.StoreName, &a.StoreStreet, &a.StoreCity, &a.Service, &a.ScheduledAt, &a.Status); err != nil {
			h.serverError(c, err)
			return
		}
		a.ScheduledAt = a.ScheduledAt.In(h.loc)
		appts = append(appts, a)
	}

	stores, err := h.loadStores(c, "")
	if err != nil {
		h.serverError(c, err)
		return
	}

	tomorrow := time.Now().In(h.loc).AddDate(0, 0, 1)
	h.render(c, status, "appointments.html", gin.H{
		"Title":        "Appointments",
		"Appointments": appts,
		"Stores":       stores,
		"Services":     models.AppointmentServices,
		"Slots":        appointmentSlots(),
		"MinDate":      tomorrow.Format("2006-01-02"),
		"MaxDate":      tomorrow.AddDate(0, 0, 60).Format("2006-01-02"),
		"StoreID":      c.Query("store"),
		"Error":        errMsg,
		"Booked":       c.Query("booked") == "1",
	})
}

// appointmentSlots are the half-hour starts an optician can be booked for.
func appointmentSlots() []string {
	var out []string
	for h := 10; h < 18; h++ {
		out = append(out, strconv.Itoa(h)+":00", strconv.Itoa(h)+":30")
	}
	return out
}

func (h *AccountHandler) BookAppointment(c *gin.Context) {
	storeID, err := strconv.Atoi(c.PostForm("store_id"))
	service := c.PostForm("service")
	when, perr := time.ParseInLocation("2006-01-02 15:04", c.PostForm("date")+" "+c.PostForm("time"), h.loc)

	validSlot := false
	for _, s := range appointmentSlots() {
		validSlot = validSlot || (perr == nil && when.Format("15:04") == s)
	}
	validService := models.AppointmentServiceLabel(service) != service

	switch {
	case err != nil || !validService:
		h.renderAppointments(c, http.StatusBadRequest, "Please choose a store and a service.")
		return
	case perr != nil || !validSlot:
		h.renderAppointments(c, http.StatusBadRequest, "Please choose one of the available times.")
		return
	case when.Before(time.Now()) || when.After(time.Now().AddDate(0, 0, 62)):
		h.renderAppointments(c, http.StatusBadRequest, "Appointments can be booked from tomorrow up to two months ahead.")
		return
	case when.Weekday() == time.Sunday:
		h.renderAppointments(c, http.StatusBadRequest, "Our stores are closed on Sundays.")
		return
	}

	_, err = h.db.ExecContext(c.Request.Context(), `
		INSERT INTO appointments (user_id, store_id, service, scheduled_at) VALUES ($1, $2, $3, $4)`,
		c.GetString("userID"), storeID, service, when)
	switch pgCode(err) {
	case "":
	case "23505":
		h.renderAppointments(c, http.StatusConflict, "Sorry, that slot was just taken. Please pick another time.")
		return
	case "23503":
		h.renderAppointments(c, http.StatusBadRequest, "That store doesn't exist.")
		return
	default:
		h.serverError(c, err)
		return
	}

	metrics.AppointmentsTotal.WithLabelValues(service).Inc()
	c.Redirect(http.StatusFound, "/appointments?booked=1")
}

func (h *AccountHandler) CancelAppointment(c *gin.Context) {
	id := c.Param("id")
	if !validID(id) {
		h.notFound(c)
		return
	}
	res, err := h.db.ExecContext(c.Request.Context(), `
		UPDATE appointments SET status = 'cancelled'
		WHERE id = $1 AND user_id = $2 AND status = 'booked' AND scheduled_at > NOW()`, id, c.GetString("userID"))
	if err != nil {
		h.serverError(c, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		h.notFound(c)
		return
	}
	c.Redirect(http.StatusFound, "/appointments")
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	if err != nil {
		return "unknown"
	}
	return ""
}
