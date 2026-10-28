// Package render holds the template helpers, including the SVG "photography"
// drawn for every product from its shape, material and colour.
package render

import (
	"fmt"
	"html/template"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"optiq/models"
)

func FuncMap(version string) template.FuncMap {
	return template.FuncMap{
		"appVersion":   func() string { return version },
		"money":        Money,
		"frame":        Frame,
		"stars":        Stars,
		"qs":           QueryWith,
		"date":         func(t time.Time) string { return t.Format("2 Jan 2006") },
		"datetime":     func(t time.Time) string { return t.Format("Mon 2 Jan 2006, 15:04") },
		"title":        Title,
		"add":          func(a, b int) int { return a + b },
		"seq":          Seq,
		"statusClass":  StatusClass,
		"serviceLabel": models.AppointmentServiceLabel,
		"payLabel":     models.PaymentLabel,
		"pct":          func(a, b float64) int { return int(math.Min(100, a/b*100)) },
		"tileShape":    tileShape,
		"dict":         Dict,
		"list":         func(xs ...string) []string { return xs },
		"ternary": func(cond bool, a, b string) string {
			if cond {
				return a
			}
			return b
		},
	}
}

// tileShape picks a representative drawing for each category tile.
func tileShape(slug string) string {
	switch slug {
	case "sunglasses":
		return "aviator"
	case "sports-glasses":
		return "wrap"
	case "kids-glasses":
		return "round"
	case "accessories":
		return "case"
	}
	return "panto"
}

func Money(v float64) string {
	return fmt.Sprintf("€%.2f", v)
}

// Stars renders a 0–5 rating as filled/empty glyphs, rounding to the nearest star.
func Stars(rating any) string {
	var n int
	switch r := rating.(type) {
	case float64:
		n = int(math.Round(r))
	case int:
		n = r
	}
	n = max(0, min(5, n))
	return strings.Repeat("★", n) + strings.Repeat("☆", 5-n)
}

// Dict builds a map from alternating keys and values, for passing several
// values into a sub-template.
func Dict(kv ...any) map[string]any {
	m := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			m[k] = kv[i+1]
		}
	}
	return m
}

func Title(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "_", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

func Seq(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

// QueryWith returns a /shop URL with the given key/value pairs replaced in q.
// An empty value removes the key. Changing any filter resets pagination.
func QueryWith(q url.Values, kv ...string) string {
	n := url.Values{}
	for k, v := range q {
		n[k] = append([]string(nil), v...)
	}
	if len(kv) < 2 || kv[0] != "page" {
		n.Del("page")
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			n.Del(kv[i])
		} else {
			n.Set(kv[i], kv[i+1])
		}
	}
	if len(n) == 0 {
		return "/shop"
	}
	return "/shop?" + n.Encode()
}

func StatusClass(status models.OrderStatus) string {
	switch status {
	case models.OrderPaid:
		return "bg-sky-50 text-sky-700 ring-sky-200"
	case models.OrderProcessing:
		return "bg-amber-50 text-amber-700 ring-amber-200"
	case models.OrderShipped:
		return "bg-indigo-50 text-indigo-700 ring-indigo-200"
	case models.OrderDelivered:
		return "bg-emerald-50 text-emerald-700 ring-emerald-200"
	}
	return "bg-slate-100 text-slate-500 ring-slate-200"
}

// ---------------------------------------------------------------------------
// product images
// ---------------------------------------------------------------------------

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Frame draws a product as inline SVG. kind is the category kind; for
// accessories, shape holds the accessory type (case, cloth, spray, ...).
func Frame(kind, shape, material, color string) template.HTML {
	if !hexColor.MatchString(color) {
		color = "#333333"
	}
	var body string
	switch kind {
	case models.KindContacts:
		body = contactLensSVG(color)
	case models.KindAccessory:
		body = accessorySVG(shape, color)
	default:
		body = glassesSVG(kind, shape, material, color)
	}
	return template.HTML(`<svg viewBox="0 0 200 100" xmlns="http://www.w3.org/2000/svg" class="w-full h-full" role="img" aria-hidden="true">` + body + `</svg>`)
}

func glassesSVG(kind, shape, material, color string) string {
	stroke := 6.0
	switch material {
	case "metal", "titanium", "stainless steel":
		stroke = 2.5
	case "combination":
		stroke = 4
	}
	lensFill, lensOpacity := "#dbe9f0", "0.45"
	if kind == models.KindSun {
		lensFill, lensOpacity = darken(color), "0.85"
	}
	lens := fmt.Sprintf(`fill="%s" fill-opacity="%s" stroke="%s" stroke-width="%.1f" stroke-linejoin="round"`, lensFill, lensOpacity, color, stroke)

	var b strings.Builder
	if shape == "wrap" || shape == "shield" {
		fmt.Fprintf(&b, `<path d="M18 34 Q100 16 182 34 Q186 66 152 74 Q122 80 106 60 L94 60 Q78 80 48 74 Q14 66 18 34 Z" %s/>`, lens)
		fmt.Fprintf(&b, `<path d="M18 34 L4 30 M182 34 L196 30" stroke="%s" stroke-width="5" stroke-linecap="round"/>`, color)
		if shape == "wrap" {
			fmt.Fprintf(&b, `<path d="M30 30 Q100 14 170 30" fill="none" stroke="%s" stroke-width="7" stroke-linecap="round"/>`, color)
		}
		return b.String()
	}

	for _, side := range []struct{ cx, dir float64 }{{60, -1}, {140, 1}} {
		if shape == "browline" {
			fmt.Fprintf(&b, `<rect x="%.0f" y="31" width="60" height="38" rx="14" fill="%s" fill-opacity="%s" stroke="#9aa0a6" stroke-width="2"/>`, side.cx-30, lensFill, lensOpacity)
			fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="9" stroke-linecap="round"/>`,
				lensPath(side.cx, side.dir, [][2]float64{{-30, -12}, {0, -22}, {30, -12}}, "Q"), color)
			continue
		}
		b.WriteString(lensShape(shape, side.cx, side.dir, lens))
	}
	// bridge and temples
	fmt.Fprintf(&b, `<path d="M90 44 Q100 36 110 44" fill="none" stroke="%s" stroke-width="%.1f" stroke-linecap="round"/>`, color, math.Max(stroke*0.7, 2.5))
	fmt.Fprintf(&b, `<path d="M30 40 L8 36 M170 40 L192 36" stroke="%s" stroke-width="%.1f" stroke-linecap="round"/>`, color, math.Max(stroke*0.6, 2.5))
	return b.String()
}

func lensShape(shape string, cx, dir float64, attrs string) string {
	const cy = 50.0
	switch shape {
	case "round":
		return fmt.Sprintf(`<circle cx="%.0f" cy="%.0f" r="26" %s/>`, cx, cy, attrs)
	case "oval":
		return fmt.Sprintf(`<ellipse cx="%.0f" cy="%.0f" rx="30" ry="21" %s/>`, cx, cy, attrs)
	case "rectangle":
		return fmt.Sprintf(`<rect x="%.0f" y="%.0f" width="62" height="36" rx="7" %s/>`, cx-31, cy-18, attrs)
	case "square":
		return fmt.Sprintf(`<rect x="%.0f" y="%.0f" width="56" height="50" rx="9" %s/>`, cx-28, cy-25, attrs)
	case "geometric":
		return fmt.Sprintf(`<path d="%s" %s/>`, polygon(cx, cy, [][2]float64{{-30, 0}, {-16, -23}, {16, -23}, {30, 0}, {16, 23}, {-16, 23}}), attrs)
	case "cat-eye":
		return fmt.Sprintf(`<path d="M%s Q%s %s Q%s %s Q%s %s Q%s %s Z" %s/>`,
			pt(cx, cy, dir, -28, -10), pt(cx, cy, dir, 0, -18), pt(cx, cy, dir, 34, -22),
			pt(cx, cy, dir, 32, 10), pt(cx, cy, dir, 12, 20), pt(cx, cy, dir, -12, 26),
			pt(cx, cy, dir, -27, 12), pt(cx, cy, dir, -33, 0), pt(cx, cy, dir, -28, -10), attrs)
	case "aviator":
		return fmt.Sprintf(`<path d="M%s Q%s %s Q%s %s Q%s %s Q%s %s Z" %s/>`,
			pt(cx, cy, dir, 30, -16), pt(cx, cy, dir, 0, -22), pt(cx, cy, dir, -30, -16),
			pt(cx, cy, dir, -34, 10), pt(cx, cy, dir, -10, 26), pt(cx, cy, dir, 14, 30),
			pt(cx, cy, dir, 28, 6), pt(cx, cy, dir, 33, -6), pt(cx, cy, dir, 30, -16), attrs)
	default: // panto
		return fmt.Sprintf(`<path d="M%s L%s Q%s %s Q%s %s Z" %s/>`,
			pt(cx, cy, dir, -29, -16), pt(cx, cy, dir, 29, -16),
			pt(cx, cy, dir, 33, 24), pt(cx, cy, dir, 0, 25),
			pt(cx, cy, dir, -33, 24), pt(cx, cy, dir, -29, -16), attrs)
	}
}

// pt places a point relative to a lens centre; dir mirrors x so that
// asymmetric shapes (cat-eye wings, aviator drops) face outwards on both sides.
func pt(cx, cy, dir, x, y float64) string {
	return fmt.Sprintf("%.1f %.1f", cx+x*dir, cy+y)
}

func lensPath(cx, dir float64, pts [][2]float64, cmd string) string {
	const cy = 50.0
	return fmt.Sprintf("M%s %s%s %s", pt(cx, cy, dir, pts[0][0], pts[0][1]), cmd,
		pt(cx, cy, dir, pts[1][0], pts[1][1]), pt(cx, cy, dir, pts[2][0], pts[2][1]))
}

func polygon(cx, cy float64, pts [][2]float64) string {
	parts := make([]string, len(pts))
	for i, p := range pts {
		parts[i] = fmt.Sprintf("%.1f %.1f", cx+p[0], cy+p[1])
	}
	return "M" + strings.Join(parts, " L") + " Z"
}

func contactLensSVG(color string) string {
	return fmt.Sprintf(`<rect x="52" y="14" width="96" height="72" rx="10" fill="#ffffff" stroke="#cbd5e1" stroke-width="2"/>`+
		`<rect x="52" y="14" width="96" height="16" rx="8" fill="%[1]s" fill-opacity="0.85"/>`+
		`<ellipse cx="100" cy="60" rx="22" ry="18" fill="%[1]s" fill-opacity="0.25" stroke="%[1]s" stroke-width="2"/>`+
		`<ellipse cx="94" cy="54" rx="7" ry="4" fill="#ffffff" fill-opacity="0.8"/>`, color)
}

func accessorySVG(kind, color string) string {
	switch kind {
	case "cloth":
		return fmt.Sprintf(`<path d="M50 24 L150 18 L156 80 L56 84 Z" fill="%[1]s" fill-opacity="0.8"/><path d="M50 24 L150 18 L120 40 Z" fill="#ffffff" fill-opacity="0.25"/>`, color)
	case "spray":
		return fmt.Sprintf(`<rect x="82" y="34" width="36" height="56" rx="8" fill="%[1]s"/><rect x="92" y="18" width="16" height="16" rx="3" fill="#475569"/><rect x="108" y="22" width="14" height="5" fill="#475569"/><rect x="88" y="52" width="24" height="18" rx="2" fill="#ffffff" fill-opacity="0.8"/>`, color)
	case "chain", "strap":
		width, dash := 3, "6 4"
		if kind == "strap" {
			width, dash = 9, "0"
		}
		return fmt.Sprintf(`<path d="M30 30 Q100 120 170 30" fill="none" stroke="%s" stroke-width="%d" stroke-dasharray="%s" stroke-linecap="round"/><circle cx="30" cy="30" r="6" fill="#475569"/><circle cx="170" cy="30" r="6" fill="#475569"/>`,
			color, width, dash)
	case "kit":
		return fmt.Sprintf(`<rect x="40" y="26" width="120" height="56" rx="12" fill="%[1]s"/><path d="M40 40 L160 40" stroke="#ffffff" stroke-opacity="0.5" stroke-width="3"/><rect x="92" y="34" width="16" height="10" rx="2" fill="#e2e8f0"/>`, color)
	default: // case, sleeve
		return fmt.Sprintf(`<rect x="34" y="30" width="132" height="46" rx="23" fill="%[1]s"/><path d="M34 53 L166 53" stroke="#000000" stroke-opacity="0.15" stroke-width="2"/><rect x="92" y="48" width="16" height="10" rx="2" fill="#e2e8f0"/>`, color)
	}
}

// darken mixes a frame colour toward near-black for sun lens tint.
func darken(hex string) string {
	var r, g, b int
	fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	mix := func(c int) int { return int(float64(c)*0.3 + 30*0.7) }
	return fmt.Sprintf("#%02x%02x%02x", mix(r), mix(g), mix(b))
}
