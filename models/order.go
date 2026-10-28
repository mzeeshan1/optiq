package models

import "time"

const (
	FreeShippingThreshold = 49.0
	ShippingFee           = 4.95
	MaxLineQuantity       = 10
)

func ShippingFor(subtotal float64) float64 {
	if subtotal >= FreeShippingThreshold || subtotal == 0 {
		return 0
	}
	return ShippingFee
}

type CartItem struct {
	ID       string
	Product  Product
	Lens     LensOption
	Quantity int
}

func (i CartItem) UnitTotal() float64 { return i.Product.Price + i.Lens.Price }
func (i CartItem) LineTotal() float64 { return i.UnitTotal() * float64(i.Quantity) }

type Cart struct {
	Items    []CartItem
	Subtotal float64
	Shipping float64
	Total    float64
}

func NewCart(items []CartItem) Cart {
	c := Cart{Items: items}
	for _, i := range items {
		c.Subtotal += i.LineTotal()
	}
	c.Shipping = ShippingFor(c.Subtotal)
	c.Total = c.Subtotal + c.Shipping
	return c
}

func (c Cart) Count() int {
	n := 0
	for _, i := range c.Items {
		n += i.Quantity
	}
	return n
}

func (c Cart) AmountToFreeShipping() float64 {
	if c.Shipping == 0 {
		return 0
	}
	return FreeShippingThreshold - c.Subtotal
}

type OrderStatus string

const (
	OrderPaid       OrderStatus = "paid"
	OrderProcessing OrderStatus = "processing"
	OrderShipped    OrderStatus = "shipped"
	OrderDelivered  OrderStatus = "delivered"
	OrderCancelled  OrderStatus = "cancelled"
)

var PaymentMethods = []struct{ Code, Label string }{
	{"card", "Credit card"},
	{"paypal", "PayPal"},
	{"invoice", "Pay by invoice"},
}

func PaymentLabel(code string) string {
	for _, m := range PaymentMethods {
		if m.Code == code {
			return m.Label
		}
	}
	return code
}

type Order struct {
	ID             string
	Number         string
	Status         OrderStatus
	Subtotal       float64
	Shipping       float64
	Total          float64
	PaymentMethod  string
	ShipName       string
	ShipStreet     string
	ShipPostalCode string
	ShipCity       string
	ShipCountry    string
	TrackingNumber string
	ItemCount      int
	CreatedAt      time.Time
	ShippedAt      *time.Time
	DeliveredAt    *time.Time
	Items          []OrderItem
}

func (o Order) CanCancel() bool { return o.Status == OrderPaid || o.Status == OrderProcessing }

func (o Order) PaymentLabel() string { return PaymentLabel(o.PaymentMethod) }

// Progress returns how far along the fulfilment pipeline the order is (0–3).
func (o Order) Progress() int {
	switch o.Status {
	case OrderProcessing:
		return 1
	case OrderShipped:
		return 2
	case OrderDelivered:
		return 3
	}
	return 0
}

type OrderItem struct {
	ProductID    string
	ProductSlug  string
	ProductName  string
	SKU          string
	LensOption   string
	LensLabel    string
	UnitPrice    float64
	LensPrice    float64
	Quantity     int
	CategoryKind string
	FrameShape   string
	ColorHex     string
}

func (i OrderItem) LineTotal() float64 { return (i.UnitPrice + i.LensPrice) * float64(i.Quantity) }
