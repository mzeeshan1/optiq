package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"optiq/metrics"
	"optiq/models"
)

const pageSize = 24

type CatalogHandler struct {
	*Base
}

func NewCatalogHandler(b *Base) *CatalogHandler {
	return &CatalogHandler{Base: b}
}

// ---------------------------------------------------------------------------
// home
// ---------------------------------------------------------------------------

type homeShelves struct {
	Bestsellers []models.Product
	NewArrivals []models.Product
	Sale        []models.Product
}

func (h *CatalogHandler) Home(c *gin.Context) {
	ctx := c.Request.Context()

	var shelves homeShelves
	if !h.cache.GetJSON(ctx, "home", "shelves", &shelves) {
		var err error
		// Bestsellers rank by units sold in the last 30 days, falling back to
		// review volume so the shelf is full on a brand new shop.
		shelves.Bestsellers, err = h.queryProducts(ctx, productSelect+`
			LEFT JOIN (
				SELECT oi.product_id, SUM(oi.quantity) AS sold
				FROM order_items oi JOIN orders o ON o.id = oi.order_id
				WHERE o.created_at > NOW() - INTERVAL '30 days' AND o.status <> 'cancelled'
				GROUP BY oi.product_id
			) s ON s.product_id = p.id
			WHERE p.is_active AND p.stock > 0 AND c.kind IN ('frame', 'sun')
			ORDER BY COALESCE(s.sold, 0) DESC, p.review_count DESC LIMIT 8`)
		if err != nil {
			h.serverError(c, err)
			return
		}
		shelves.NewArrivals, err = h.queryProducts(ctx, productSelect+`
			WHERE p.is_active AND c.kind IN ('frame', 'sun') ORDER BY p.created_at DESC LIMIT 8`)
		if err != nil {
			h.serverError(c, err)
			return
		}
		shelves.Sale, err = h.queryProducts(ctx, productSelect+`
			WHERE p.is_active AND p.compare_at_price > p.price AND p.stock > 0
			ORDER BY (p.compare_at_price - p.price) / p.compare_at_price DESC LIMIT 4`)
		if err != nil {
			h.serverError(c, err)
			return
		}
		h.cache.SetJSON(ctx, "home", "shelves", shelves, 2*time.Minute)
	}

	data := gin.H{
		"Title":       "Glasses, sunglasses & contact lenses",
		"Bestsellers": shelves.Bestsellers,
		"NewArrivals": shelves.NewArrivals,
		"Sale":        shelves.Sale,
	}
	if ids, err := h.cache.TrendingProducts(ctx, 4); err == nil {
		data["Trending"], _ = h.productsByIDs(ctx, ids)
	}
	if userID := c.GetString("userID"); userID != "" {
		if ids, err := h.cache.RecentlyViewed(ctx, userID); err == nil {
			data["Recent"], _ = h.productsByIDs(ctx, ids)
		}
	}
	h.render(c, http.StatusOK, "home.html", data)
}

// ---------------------------------------------------------------------------
// listing & search
// ---------------------------------------------------------------------------

var sortOrders = map[string]string{
	"popular":    "p.review_count DESC, p.rating_avg DESC, p.sku",
	"newest":     "p.created_at DESC",
	"price_asc":  "p.price ASC, p.sku",
	"price_desc": "p.price DESC, p.sku",
	"rating":     "p.rating_avg DESC, p.review_count DESC, p.sku",
}

var sortLabels = []struct{ Code, Label string }{
	{"popular", "Most popular"}, {"newest", "Newest"}, {"price_asc", "Price: low to high"},
	{"price_desc", "Price: high to low"}, {"rating", "Top rated"},
}

var priceBands = []struct{ Label, Min, Max string }{
	{"Under €50", "", "50"}, {"€50 – €100", "50", "100"}, {"€100 – €200", "100", "200"}, {"Over €200", "200", ""},
}

// filterKeys are the query parameters that affect results; anything else is
// dropped so stray params can't fragment the listing cache.
var filterKeys = []string{"category", "brand", "gender", "shape", "material", "color", "q", "min", "max", "sale", "sort", "page"}

type listing struct {
	Products []models.Product
	Total    int
}

type filterChip struct{ Label, Key string }

type facets struct {
	Brands    []models.Brand
	Shapes    []string
	Materials []string
	Colors    []struct{ Name, Hex string }
}

func (h *CatalogHandler) Shop(c *gin.Context) {
	ctx := c.Request.Context()

	q := url.Values{}
	for _, k := range filterKeys {
		if v := strings.TrimSpace(c.Query(k)); v != "" {
			q.Set(k, v)
		}
	}
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	sort := q.Get("sort")
	if _, ok := sortOrders[sort]; !ok {
		sort = "popular"
	}

	var res listing
	cacheKey := q.Encode()
	if !h.cache.GetJSON(ctx, "list", cacheKey, &res) {
		where, args := buildFilter(q)
		if err := h.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM products p `+productJoins+` WHERE `+where, args...).Scan(&res.Total); err != nil {
			h.serverError(c, err)
			return
		}
		var err error
		res.Products, err = h.queryProducts(ctx, fmt.Sprintf("%s WHERE %s ORDER BY %s LIMIT %d OFFSET %d",
			productSelect, where, sortOrders[sort], pageSize, (page-1)*pageSize), args...)
		if err != nil {
			h.serverError(c, err)
			return
		}
		h.cache.SetJSON(ctx, "list", cacheKey, res, 2*time.Minute)
	}

	if term := q.Get("q"); term != "" && page == 1 {
		result := "found"
		if res.Total == 0 {
			result = "empty"
		}
		metrics.SearchesTotal.WithLabelValues(result).Inc()
	}

	fs, err := h.facets(c, q.Get("category"))
	if err != nil {
		h.serverError(c, err)
		return
	}

	heading := "All products"
	var category models.Category
	if cat, ok := h.category(q.Get("category")); ok {
		heading, category = cat.Name, cat
	}
	if term := q.Get("q"); term != "" {
		heading = fmt.Sprintf("Results for “%s”", term)
	}

	totalPages := max((res.Total+pageSize-1)/pageSize, 1)
	var chips []filterChip
	for _, k := range []string{"q", "brand", "gender", "shape", "material", "color", "min", "max", "sale"} {
		v := q.Get(k)
		if v == "" {
			continue
		}
		label := v
		switch k {
		case "q":
			label = "“" + v + "”"
		case "min":
			label = "From €" + v
		case "max":
			label = "Under €" + v
		case "sale":
			label = "On sale"
		case "brand":
			for _, b := range fs.Brands {
				if b.Slug == v {
					label = b.Name
				}
			}
		}
		chips = append(chips, filterChip{Label: label, Key: k})
	}

	h.render(c, http.StatusOK, "shop.html", gin.H{
		"Title":      heading,
		"Heading":    heading,
		"Category":   category,
		"Products":   res.Products,
		"Total":      res.Total,
		"Query":      q,
		"Search":     q.Get("q"),
		"Sort":       sort,
		"Sorts":      sortLabels,
		"PriceBands": priceBands,
		"Facets":     fs,
		"Chips":      chips,
		"Page":       page,
		"TotalPages": totalPages,
		"PrevPage":   strconv.Itoa(page - 1),
		"NextPage":   strconv.Itoa(page + 1),
	})
}

func buildFilter(q url.Values) (string, []any) {
	conds := []string{"p.is_active"}
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}

	if v := q.Get("category"); v != "" {
		add("c.slug = $%d", v)
	}
	if v := q.Get("brand"); v != "" {
		add("b.slug = $%d", v)
	}
	switch v := q.Get("gender"); v {
	case "women", "men":
		add("p.gender IN ($%d, 'unisex')", v)
	case "unisex", "kids":
		add("p.gender = $%d", v)
	}
	if v := q.Get("shape"); v != "" {
		add("p.frame_shape = $%d", v)
	}
	if v := q.Get("material"); v != "" {
		add("p.frame_material = $%d", v)
	}
	if v := q.Get("color"); v != "" {
		add("p.color = $%d", v)
	}
	if v, err := strconv.ParseFloat(q.Get("min"), 64); err == nil {
		add("p.price >= $%d", v)
	}
	if v, err := strconv.ParseFloat(q.Get("max"), 64); err == nil {
		add("p.price < $%d", v)
	}
	if q.Get("sale") == "1" {
		conds = append(conds, "p.compare_at_price > p.price")
	}
	// Every word must match somewhere, so "black round" narrows rather than widens.
	for _, word := range strings.Fields(q.Get("q")) {
		add(`(p.name ILIKE $%[1]d OR b.name ILIKE $%[1]d OR c.name ILIKE $%[1]d OR p.frame_shape ILIKE $%[1]d
			OR p.frame_material ILIKE $%[1]d OR p.color ILIKE $%[1]d OR p.sku ILIKE $%[1]d)`, "%"+word+"%")
	}
	return strings.Join(conds, " AND "), args
}

func (h *CatalogHandler) facets(c *gin.Context, category string) (facets, error) {
	ctx := c.Request.Context()
	var fs facets
	if h.cache.GetJSON(ctx, "facets", category, &fs) {
		return fs, nil
	}

	where, args := "p.is_active", []any{}
	if category != "" {
		where, args = "p.is_active AND c.slug = $1", []any{category}
	}
	from := ` FROM products p ` + productJoins + ` WHERE ` + where

	rows, err := h.db.QueryContext(ctx, `SELECT DISTINCT b.id, b.slug, b.name`+from+` ORDER BY b.name`, args...)
	if err != nil {
		return fs, err
	}
	for rows.Next() {
		var b models.Brand
		if err := rows.Scan(&b.ID, &b.Slug, &b.Name); err != nil {
			rows.Close()
			return fs, err
		}
		fs.Brands = append(fs.Brands, b)
	}
	rows.Close()

	for _, f := range []struct {
		col string
		dst *[]string
	}{{"p.frame_shape", &fs.Shapes}, {"p.frame_material", &fs.Materials}} {
		rows, err := h.db.QueryContext(ctx, `SELECT DISTINCT `+f.col+from+` AND `+f.col+` <> '' AND c.kind IN ('frame', 'sun') ORDER BY 1`, args...)
		if err != nil {
			return fs, err
		}
		for rows.Next() {
			var v string
			rows.Scan(&v)
			*f.dst = append(*f.dst, v)
		}
		rows.Close()
	}

	rows, err = h.db.QueryContext(ctx, `SELECT p.color, MIN(p.color_hex)`+from+` GROUP BY p.color ORDER BY COUNT(*) DESC LIMIT 16`, args...)
	if err != nil {
		return fs, err
	}
	defer rows.Close()
	for rows.Next() {
		var col struct{ Name, Hex string }
		rows.Scan(&col.Name, &col.Hex)
		fs.Colors = append(fs.Colors, col)
	}

	h.cache.SetJSON(ctx, "facets", category, fs, 10*time.Minute)
	return fs, rows.Err()
}

// ---------------------------------------------------------------------------
// product detail
// ---------------------------------------------------------------------------

type productPage struct {
	Product models.Product
	Related []models.Product
}

func (h *CatalogHandler) Product(c *gin.Context) {
	ctx := c.Request.Context()
	slug := c.Param("slug")

	var pp productPage
	if !h.cache.GetJSON(ctx, "product", slug, &pp) {
		p, err := scanProduct(h.db.QueryRowContext(ctx, productSelect+` WHERE p.slug = $1 AND p.is_active`, slug))
		if errors.Is(err, sql.ErrNoRows) {
			h.notFound(c)
			return
		}
		if err != nil {
			h.serverError(c, err)
			return
		}
		pp.Product = p
		pp.Related, err = h.queryProducts(ctx, productSelect+`
			WHERE p.category_id = $1 AND p.id <> $2 AND p.is_active AND p.stock > 0
			ORDER BY (p.frame_shape = $3) DESC, (p.brand_id = $4) DESC, p.review_count DESC LIMIT 4`,
			p.CategoryID, p.ID, p.FrameShape, p.BrandID)
		if err != nil {
			h.serverError(c, err)
			return
		}
		h.cache.SetJSON(ctx, "product", slug, pp, 5*time.Minute)
	}
	p := pp.Product

	reviews, err := h.reviews(c, p.ID)
	if err != nil {
		h.serverError(c, err)
		return
	}

	metrics.ProductViewsTotal.WithLabelValues(p.CategorySlug).Inc()
	h.cache.RecordProductView(ctx, p.ID)

	wishlisted := false
	if userID := c.GetString("userID"); userID != "" {
		h.cache.PushRecentlyViewed(ctx, userID, p.ID)
		h.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM wishlist_items WHERE user_id = $1 AND product_id = $2)`,
			userID, p.ID).Scan(&wishlisted)
	}

	h.render(c, http.StatusOK, "product.html", gin.H{
		"Title":      p.Name,
		"Product":    p,
		"Related":    pp.Related,
		"Reviews":    reviews,
		"Wishlisted": wishlisted,
		"Added":      c.Query("reviewed") == "1",
	})
}

func (h *CatalogHandler) reviews(c *gin.Context, productID string) ([]models.Review, error) {
	rows, err := h.db.QueryContext(c.Request.Context(), `
		SELECT r.id, u.first_name || ' ' || LEFT(u.last_name, 1) || '.', r.rating, r.title, r.body, r.created_at
		FROM reviews r JOIN users u ON u.id = r.user_id
		WHERE r.product_id = $1 ORDER BY r.created_at DESC LIMIT 10`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Review
	for rows.Next() {
		var r models.Review
		if err := rows.Scan(&r.ID, &r.UserName, &r.Rating, &r.Title, &r.Body, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (h *CatalogHandler) CreateReview(c *gin.Context) {
	ctx := c.Request.Context()
	userID := c.GetString("userID")
	slug := c.Param("slug")

	var productID string
	err := h.db.QueryRowContext(ctx, `SELECT id FROM products WHERE slug = $1 AND is_active`, slug).Scan(&productID)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(c)
		return
	}
	if err != nil {
		h.serverError(c, err)
		return
	}

	rating, err := strconv.Atoi(c.PostForm("rating"))
	title := strings.TrimSpace(c.PostForm("title"))
	body := strings.TrimSpace(c.PostForm("body"))
	if err != nil || rating < 1 || rating > 5 || len(title) > 120 || len(body) > 2000 {
		h.fail(c, http.StatusBadRequest, "Invalid review", "Please choose a rating from 1 to 5 and keep your review under 2000 characters.")
		return
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.serverError(c, err)
		return
	}
	defer tx.Rollback()

	// One review per customer per product; writing again replaces the old one.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO reviews (product_id, user_id, rating, title, body) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (product_id, user_id) DO UPDATE
		SET rating = EXCLUDED.rating, title = EXCLUDED.title, body = EXCLUDED.body, created_at = NOW()`,
		productID, userID, rating, title, body); err != nil {
		h.serverError(c, err)
		return
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE products SET
			rating_avg   = (SELECT ROUND(AVG(rating)::numeric, 2) FROM reviews WHERE product_id = $1),
			review_count = (SELECT COUNT(*) FROM reviews WHERE product_id = $1)
		WHERE id = $1`, productID); err != nil {
		h.serverError(c, err)
		return
	}
	if err := tx.Commit(); err != nil {
		h.serverError(c, err)
		return
	}

	metrics.ReviewsTotal.Inc()
	h.cache.Delete(ctx, "product", slug)
	c.Redirect(http.StatusFound, "/products/"+slug+"?reviewed=1#reviews")
}
