package models

import (
	"fmt"
	"time"
)

// Category kinds decide which lens options a product can be ordered with.
const (
	KindFrame     = "frame"
	KindSun       = "sun"
	KindContacts  = "contacts"
	KindAccessory = "accessory"
)

type Category struct {
	ID          int
	Slug        string
	Name        string
	Description string
	Kind        string
}

type Brand struct {
	ID      int
	Slug    string
	Name    string
	Country string
	Premium bool
}

type Product struct {
	ID             string
	SKU            string
	Slug           string
	Name           string
	Description    string
	CategoryID     int
	CategorySlug   string
	CategoryName   string
	CategoryKind   string
	BrandID        int
	BrandSlug      string
	BrandName      string
	Gender         string
	FrameShape     string
	FrameMaterial  string
	Color          string
	ColorHex       string
	LensWidth      int
	BridgeWidth    int
	TempleLength   int
	Price          float64
	CompareAtPrice float64 // 0 when the product is not on sale
	Stock          int
	RatingAvg      float64
	ReviewCount    int
	CreatedAt      time.Time
}

func (p Product) OnSale() bool { return p.CompareAtPrice > p.Price }

func (p Product) DiscountPercent() int {
	if !p.OnSale() {
		return 0
	}
	return int((1 - p.Price/p.CompareAtPrice) * 100)
}

func (p Product) InStock() bool  { return p.Stock > 0 }
func (p Product) LowStock() bool { return p.Stock > 0 && p.Stock <= 5 }

func (p Product) IsNew() bool { return time.Since(p.CreatedAt) < 30*24*time.Hour }

// Measurements renders the lens□bridge-temple notation printed inside frames.
func (p Product) Measurements() string {
	if p.LensWidth == 0 {
		return ""
	}
	return fmt.Sprintf("%d□%d-%d", p.LensWidth, p.BridgeWidth, p.TempleLength)
}

func (p Product) LensOptions() []LensOption { return LensOptionsFor(p.CategoryKind) }

type LensOption struct {
	Code  string
	Label string
	Price float64
}

const LensNone = "none"

var (
	lensNone        = LensOption{LensNone, "Frame only", 0}
	lensSingle      = LensOption{"single_vision", "Single-vision lenses", 59}
	lensBlueLight   = LensOption{"blue_light", "Single-vision + blue-light filter", 89}
	lensVarifocal   = LensOption{"varifocal", "Varifocal lenses", 189}
	lensSunStandard = LensOption{LensNone, "Non-prescription sun lenses", 0}
	lensSunRx       = LensOption{"prescription_sun", "Prescription sun lenses", 99}
	lensIncluded    = LensOption{LensNone, "Standard", 0}
)

func LensOptionsFor(kind string) []LensOption {
	switch kind {
	case KindFrame:
		return []LensOption{lensNone, lensSingle, lensBlueLight, lensVarifocal}
	case KindSun:
		return []LensOption{lensSunStandard, lensSunRx}
	}
	return []LensOption{lensIncluded}
}

func LookupLensOption(kind, code string) (LensOption, bool) {
	for _, o := range LensOptionsFor(kind) {
		if o.Code == code {
			return o, true
		}
	}
	return LensOption{}, false
}

type Review struct {
	ID        string
	ProductID string
	UserName  string
	Rating    int
	Title     string
	Body      string
	CreatedAt time.Time
}
