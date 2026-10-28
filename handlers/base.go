package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"optiq/cache"
	"optiq/models"
)

// Base carries what every handler needs: storage, and the bits of page chrome
// (category nav, cart badge, signed-in user) that every template renders.
type Base struct {
	db         *sql.DB
	cache      *cache.Cache
	categories []models.Category
}

func NewBase(db *sql.DB, c *cache.Cache) (*Base, error) {
	b := &Base{db: db, cache: c}
	rows, err := db.Query(`SELECT id, slug, name, description, kind FROM categories ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var cat models.Category
		if err := rows.Scan(&cat.ID, &cat.Slug, &cat.Name, &cat.Description, &cat.Kind); err != nil {
			return nil, err
		}
		b.categories = append(b.categories, cat)
	}
	return b, rows.Err()
}

func (b *Base) render(c *gin.Context, status int, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	userID := c.GetString("userID")
	data["Categories"] = b.categories
	data["LoggedIn"] = userID != ""
	data["FirstName"] = c.GetString("firstName")
	data["Email"] = c.GetString("email")
	if _, ok := data["Search"]; !ok {
		data["Search"] = ""
	}
	if userID != "" {
		var n int
		b.db.QueryRowContext(c.Request.Context(),
			`SELECT COALESCE(SUM(quantity), 0) FROM cart_items WHERE user_id = $1`, userID).Scan(&n)
		data["CartCount"] = n
	}
	c.HTML(status, name, data)
}

func (b *Base) fail(c *gin.Context, status int, heading, message string) {
	b.render(c, status, "error.html", gin.H{
		"Title":   heading,
		"Status":  status,
		"Heading": heading,
		"Message": message,
	})
}

func (b *Base) notFound(c *gin.Context) {
	b.fail(c, http.StatusNotFound, "Page not found", "We couldn't find what you were looking for. It may have sold out or moved.")
}

func (b *Base) serverError(c *gin.Context, err error) {
	c.Error(err)
	b.fail(c, http.StatusInternalServerError, "Something went wrong", "Please try again in a moment.")
}

func (b *Base) NotFoundPage(c *gin.Context) { b.notFound(c) }

func (b *Base) category(slug string) (models.Category, bool) {
	for _, cat := range b.categories {
		if cat.Slug == slug {
			return cat, true
		}
	}
	return models.Category{}, false
}

// ---------------------------------------------------------------------------
// shared product queries
// ---------------------------------------------------------------------------

const productCols = `p.id, p.sku, p.slug, p.name, p.description, p.category_id, c.slug, c.name, c.kind,
	p.brand_id, b.slug, b.name, p.gender, p.frame_shape, p.frame_material, p.color, p.color_hex,
	p.lens_width, p.bridge_width, p.temple_length, p.price, COALESCE(p.compare_at_price, 0),
	p.stock, p.rating_avg, p.review_count, p.created_at`

const productJoins = `JOIN categories c ON c.id = p.category_id JOIN brands b ON b.id = p.brand_id`

const productSelect = `SELECT ` + productCols + ` FROM products p ` + productJoins

type scanner interface{ Scan(dest ...any) error }

// scanProduct reads productCols, followed by any extra columns the query appended.
func scanProduct(s scanner, extra ...any) (models.Product, error) {
	var p models.Product
	dest := append([]any{&p.ID, &p.SKU, &p.Slug, &p.Name, &p.Description, &p.CategoryID,
		&p.CategorySlug, &p.CategoryName, &p.CategoryKind, &p.BrandID, &p.BrandSlug, &p.BrandName,
		&p.Gender, &p.FrameShape, &p.FrameMaterial, &p.Color, &p.ColorHex, &p.LensWidth,
		&p.BridgeWidth, &p.TempleLength, &p.Price, &p.CompareAtPrice, &p.Stock, &p.RatingAvg,
		&p.ReviewCount, &p.CreatedAt}, extra...)
	err := s.Scan(dest...)
	return p, err
}

func (b *Base) queryProducts(ctx context.Context, query string, args ...any) ([]models.Product, error) {
	rows, err := b.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// productsByIDs loads products and returns them in the order of ids.
func (b *Base) productsByIDs(ctx context.Context, ids []string) ([]models.Product, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	found, err := b.queryProducts(ctx, productSelect+` WHERE p.id = ANY($1::uuid[]) AND p.is_active`, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]models.Product, len(found))
	for _, p := range found {
		byID[p.ID] = p
	}
	out := make([]models.Product, 0, len(ids))
	for _, id := range ids {
		if p, ok := byID[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validID(id string) bool { return uuidPattern.MatchString(id) }

// safeNext only allows same-site relative redirects.
func safeNext(next, fallback string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") && !strings.HasPrefix(next, "/\\") {
		return next
	}
	return fallback
}
