package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// SeedCustomerPassword is the password every seeded customer account shares,
// so they can be used to log in by hand while exploring the shop.
const SeedCustomerPassword = "password123"

// Seed fills an empty catalog with generated products, brands, stores, demo
// customers and their reviews. It is a no-op once any product exists, and the
// generator uses a fixed random seed so every fresh database looks the same.
func Seed(db *sql.DB, products, customers int) error {
	if products <= 0 {
		return nil
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, seedLockKey); err != nil {
		return err
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM products`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	start := time.Now()
	s := &seeder{ctx: ctx, tx: tx, rng: rand.New(rand.NewPCG(42, 2026)), now: time.Now().UTC()}

	steps := []func() error{
		s.categories,
		s.brands,
		func() error { return s.products(products) },
		s.stores,
		func() error { return s.customers(customers) },
		s.reviews,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	log.Printf("seeded %d products, %d brands, %d stores, %d customers, %d reviews in %s",
		len(s.productIDs), len(s.brandIDs), s.storeCount, len(s.customerIDs), s.reviewCount,
		time.Since(start).Truncate(time.Millisecond))
	return nil
}

type seeder struct {
	ctx context.Context
	tx  *sql.Tx
	rng *rand.Rand
	now time.Time

	categoryIDs map[string]int
	brandIDs    map[string]int
	productIDs  []seededProduct
	customerIDs []string
	storeCount  int
	reviewCount int
}

type seededProduct struct {
	id         string
	createdAt  time.Time
	popularity float64
	quality    float64
}

// ---------------------------------------------------------------------------
// reference data
// ---------------------------------------------------------------------------

var seedCategories = []struct {
	slug, name, description, kind string
	share                         int // percent of the generated catalog
}{
	{"eyeglasses", "Glasses", "Prescription-ready frames for every face shape, with lenses fitted in our own workshop.", "frame", 40},
	{"sunglasses", "Sunglasses", "UV400 protection as standard, available with prescription sun lenses.", "sun", 25},
	{"sports-glasses", "Sports glasses", "Wraparound frames built for running, cycling and the slopes.", "sun", 8},
	{"kids-glasses", "Kids' glasses", "Flexible, durable frames sized for children from 3 to 12.", "frame", 9},
	{"contact-lenses", "Contact lenses", "Daily and monthly lenses, delivered to your door.", "contacts", 10},
	{"accessories", "Accessories", "Cases, cloths and care products to keep your glasses like new.", "accessory", 8},
}

type seedBrand struct {
	slug, name, country string
	tier                int // 0 budget, 1 mid-range, 2 premium
	categories          []string
}

var seedBrands = []seedBrand{
	{"nordlicht", "Nordlicht", "Germany", 1, []string{"eyeglasses", "sunglasses"}},
	{"havel-co", "Havel & Co", "Germany", 1, []string{"eyeglasses"}},
	{"elbe-optics", "Elbe Optics", "Germany", 0, []string{"eyeglasses", "sunglasses"}},
	{"kaiserwerk", "Kaiserwerk", "Germany", 2, []string{"eyeglasses", "sunglasses"}},
	{"atelier-lumiere", "Atelier Lumière", "France", 2, []string{"eyeglasses", "sunglasses"}},
	{"vista-nova", "Vista Nova", "Italy", 2, []string{"eyeglasses", "sunglasses"}},
	{"brixen", "Brixen", "Austria", 1, []string{"eyeglasses", "sunglasses", "sports-glasses"}},
	{"fjord-fell", "Fjord & Fell", "Norway", 1, []string{"eyeglasses", "sunglasses"}},
	{"alster-line", "Alster Line", "Germany", 0, []string{"eyeglasses", "sunglasses"}},
	{"solano", "Solano", "Spain", 1, []string{"sunglasses"}},
	{"polar-peak", "Polar Peak", "Switzerland", 1, []string{"sports-glasses", "sunglasses"}},
	{"velocis", "Velocis", "Italy", 2, []string{"sports-glasses"}},
	{"kiddo-specs", "Kiddo Specs", "Denmark", 0, []string{"kids-glasses"}},
	{"little-owl", "Little Owl", "Germany", 1, []string{"kids-glasses"}},
	{"aqualens", "Aqualens", "Germany", 1, []string{"contact-lenses"}},
	{"clearday", "ClearDay", "Netherlands", 1, []string{"contact-lenses"}},
	{"hydra-vision", "Hydra Vision", "Ireland", 2, []string{"contact-lenses"}},
	{"optiq-care", "Optiq Care", "Germany", 0, []string{"contact-lenses", "accessories"}},
	{"etui-atelier", "Etui Atelier", "Germany", 1, []string{"accessories"}},
}

var modelNames = []string{
	"Freya", "Linus", "Mila", "Theo", "Elin", "Jonas", "Ida", "Hugo", "Nova", "Emil",
	"Smilla", "Ole", "Greta", "Finn", "Juna", "Matti", "Liv", "Bo", "Kaia", "Anton",
	"Lotta", "Henrik", "Saga", "Paul", "Alva", "Malte", "Frida", "Levin", "Enna", "Carl",
	"Tilda", "Arne", "Wilma", "Jasper", "Ronja", "Mats", "Clara", "Bruno", "Helena", "Oskar",
}

var sportModelNames = []string{"Apex", "Summit", "Sprint", "Glide", "Vector", "Ridge", "Tempo", "Drift", "Pulse", "Crest"}

type seedColor struct{ name, hex string }

var frameColors = []seedColor{
	{"Matte Black", "#1f1f1f"}, {"Glossy Black", "#0d0d0d"}, {"Havana Brown", "#6b3e26"},
	{"Tortoise", "#7a4a2a"}, {"Crystal Clear", "#c9d6de"}, {"Gold", "#c9a14a"},
	{"Silver", "#a9aeb5"}, {"Gunmetal", "#4a4f55"}, {"Navy Blue", "#1f3a5f"},
	{"Burgundy", "#6d1f2e"}, {"Rose Gold", "#c98f7d"}, {"Olive Green", "#5b6b3a"},
	{"Honey", "#c58b3c"}, {"Grey Fade", "#8a8f96"}, {"Blush Pink", "#d98fa0"},
	{"Cherry Red", "#b3261e"}, {"Teal", "#2a7f7f"}, {"Champagne", "#d8c39a"},
}

var sportColors = []seedColor{
	{"Matte Black", "#1f1f1f"}, {"Signal Orange", "#e8622c"}, {"Electric Blue", "#1f6fd1"},
	{"Neon Lime", "#9ccc2b"}, {"White", "#e8e8e8"}, {"Carbon Grey", "#3d4247"},
}

var kidsColors = []seedColor{
	{"Sky Blue", "#5aa9e6"}, {"Bubblegum Pink", "#f28bb3"}, {"Mint", "#5cc8a0"},
	{"Sunshine Yellow", "#f2c230"}, {"Purple", "#7d4cc2"}, {"Red", "#d63a2f"},
	{"Navy Blue", "#1f3a5f"}, {"Tortoise", "#7a4a2a"},
}

var (
	frameShapes  = []string{"round", "rectangle", "square", "cat-eye", "oval", "browline", "panto", "geometric", "aviator"}
	sunShapes    = []string{"aviator", "round", "square", "cat-eye", "rectangle", "oval", "geometric", "panto", "shield"}
	sportShapes  = []string{"wrap", "shield"}
	kidsShapes   = []string{"round", "rectangle", "oval", "square", "panto"}
	frameMats    = []string{"acetate", "acetate", "acetate", "metal", "metal", "titanium", "TR90", "stainless steel", "combination", "wood"}
	sunMats      = []string{"acetate", "acetate", "metal", "metal", "TR90", "combination"}
	sportMats    = []string{"TR90", "polycarbonate", "Grilamid"}
	kidsMats     = []string{"TR90", "TR90", "acetate", "flexible silicone"}
	adultGenders = []string{"women", "women", "men", "men", "unisex"}
	sunLenses    = []string{"grey", "brown", "green G-15", "mirrored blue", "gradient grey", "polarised grey", "polarised brown"}
)

type contactLine struct {
	name, material string
	packs          []int
	minPrice       []float64 // per pack size
	coloured       bool
}

var contactLines = []contactLine{
	{"Daily Comfort", "hydrogel", []int{30, 90}, []float64{19.95, 49.95}, false},
	{"Daily Hydro", "silicone hydrogel", []int{30, 90}, []float64{24.95, 64.95}, false},
	{"Monthly Air", "silicone hydrogel", []int{3, 6}, []float64{17.95, 32.95}, false},
	{"Monthly Pro", "silicone hydrogel", []int{3, 6}, []float64{22.95, 39.95}, false},
	{"Toric Daily", "silicone hydrogel", []int{30, 90}, []float64{29.95, 79.95}, false},
	{"Multifocal Monthly", "silicone hydrogel", []int{3, 6}, []float64{34.95, 59.95}, false},
	{"Colour Daily", "hydrogel", []int{10, 30}, []float64{14.95, 29.95}, true},
}

var lensColours = []seedColor{
	{"Clear", "#cfe8f3"}, {"Ocean Blue", "#2f7fc1"}, {"Emerald", "#2f9e6a"}, {"Hazel", "#9a6b3a"}, {"Misty Grey", "#8c96a0"},
}

var accessoryItems = []struct {
	name, kind  string
	lo, hi      float64
	description string
}{
	{"Hard-shell case", "case", 9.95, 29.95, "A crush-proof case with a soft velvet lining and a magnetic closure."},
	{"Foldable case", "case", 12.95, 24.95, "Folds flat when empty and fits in any jacket pocket."},
	{"Leather sleeve", "sleeve", 19.95, 39.95, "Vegetable-tanned leather sleeve that softens beautifully with age."},
	{"Microfibre cloth", "cloth", 3.95, 7.95, "Ultra-fine microfibre that lifts fingerprints without scratching coatings."},
	{"Lens cleaning spray 60 ml", "spray", 5.95, 9.95, "Alcohol-free formula, safe for anti-reflective and blue-light coatings."},
	{"Anti-fog wipes (30 pack)", "spray", 7.95, 12.95, "Individually wrapped wipes that keep lenses clear behind a mask."},
	{"Glasses chain", "chain", 14.95, 34.95, "A lightweight chain with silicone loops that grip any temple."},
	{"Sports strap", "strap", 6.95, 14.95, "Adjustable neoprene strap that keeps frames secure during sport."},
	{"Complete care kit", "kit", 16.95, 29.95, "Spray, cloth, mini screwdriver and spare nose pads in a zip pouch."},
}

var seedCities = []struct {
	city, postal, area string
	districts          []string
}{
	{"Hamburg", "20", "040", []string{"Altstadt", "Eppendorf", "Altona"}},
	{"Berlin", "10", "030", []string{"Mitte", "Charlottenburg", "Prenzlauer Berg"}},
	{"München", "80", "089", []string{"Marienplatz", "Schwabing"}},
	{"Köln", "50", "0221", []string{"Innenstadt", "Ehrenfeld"}},
	{"Frankfurt am Main", "60", "069", []string{"Zeil", "Sachsenhausen"}},
	{"Stuttgart", "70", "0711", nil}, {"Düsseldorf", "40", "0211", nil}, {"Leipzig", "04", "0341", nil},
	{"Dortmund", "44", "0231", nil}, {"Essen", "45", "0201", nil}, {"Bremen", "28", "0421", nil},
	{"Dresden", "01", "0351", nil}, {"Hannover", "30", "0511", nil}, {"Nürnberg", "90", "0911", nil},
	{"Bochum", "44", "0234", nil}, {"Bielefeld", "33", "0521", nil}, {"Bonn", "53", "0228", nil},
	{"Münster", "48", "0251", nil}, {"Mannheim", "68", "0621", nil}, {"Karlsruhe", "76", "0721", nil},
	{"Augsburg", "86", "0821", nil}, {"Wiesbaden", "65", "0611", nil}, {"Kiel", "24", "0431", nil},
	{"Lübeck", "23", "0451", nil}, {"Rostock", "18", "0381", nil}, {"Freiburg im Breisgau", "79", "0761", nil},
	{"Potsdam", "14", "0331", nil}, {"Mainz", "55", "06131", nil}, {"Erfurt", "99", "0361", nil},
	{"Regensburg", "93", "0941", nil},
}

var streets = []string{
	"Hauptstraße", "Bahnhofstraße", "Königstraße", "Marktplatz", "Lindenstraße", "Schillerstraße",
	"Goethestraße", "Kaiserstraße", "Schloßstraße", "Breite Straße", "Gartenstraße", "Kirchstraße",
	"Bergstraße", "Mozartstraße", "Rosenweg", "Am Rathaus", "Poststraße", "Friedrichstraße",
}

var firstNames = []string{
	"Anna", "Lena", "Sophie", "Marie", "Laura", "Julia", "Lea", "Hannah", "Emma", "Mia",
	"Katharina", "Sarah", "Lisa", "Johanna", "Charlotte", "Nora", "Ayşe", "Elif", "Zeynep", "Fatima",
	"Lukas", "Leon", "Felix", "Jonas", "Maximilian", "Paul", "Tim", "Jan", "Niklas", "Moritz",
	"David", "Tobias", "Florian", "Sebastian", "Alexander", "Mehmet", "Ali", "Can", "Piotr", "Luca",
}

var lastNames = []string{
	"Müller", "Schmidt", "Schneider", "Fischer", "Weber", "Meyer", "Wagner", "Becker", "Schulz", "Hoffmann",
	"Schäfer", "Koch", "Bauer", "Richter", "Klein", "Wolf", "Schröder", "Neumann", "Schwarz", "Zimmermann",
	"Braun", "Krüger", "Hofmann", "Hartmann", "Lange", "Schmitt", "Werner", "Krause", "Meier", "Lehmann",
	"Yılmaz", "Kaya", "Nowak", "Kowalski", "Rossi", "Peters", "Jansen", "Hansen", "Vogel", "Friedrich",
}

// ---------------------------------------------------------------------------
// steps
// ---------------------------------------------------------------------------

func (s *seeder) categories() error {
	s.categoryIDs = map[string]int{}
	for i, c := range seedCategories {
		var id int
		err := s.tx.QueryRowContext(s.ctx, `
			INSERT INTO categories (slug, name, description, kind, position) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, c.slug, c.name, c.description, c.kind, i).Scan(&id)
		if err != nil {
			return fmt.Errorf("seed category %s: %w", c.slug, err)
		}
		s.categoryIDs[c.slug] = id
	}
	return nil
}

func (s *seeder) brands() error {
	s.brandIDs = map[string]int{}
	for _, b := range seedBrands {
		var id int
		err := s.tx.QueryRowContext(s.ctx, `
			INSERT INTO brands (slug, name, country, premium) VALUES ($1, $2, $3, $4)
			ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, b.slug, b.name, b.country, b.tier == 2).Scan(&id)
		if err != nil {
			return fmt.Errorf("seed brand %s: %w", b.slug, err)
		}
		s.brandIDs[b.slug] = id
	}
	return nil
}

type productRow struct {
	sku, slug, name, description         string
	category, brand                      string
	gender, shape, material, color, hex  string
	lensWidth, bridgeWidth, templeLength int
	price                                float64
	compareAt                            sql.NullFloat64
	stock                                int
	createdAt                            time.Time
}

func (s *seeder) products(total int) error {
	stmt, err := s.tx.PrepareContext(s.ctx, `
		INSERT INTO products (sku, slug, name, description, category_id, brand_id, gender, frame_shape,
			frame_material, color, color_hex, lens_width, bridge_width, temple_length, price,
			compare_at_price, stock, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$18)
		RETURNING id`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	n := 0
	for ci, cat := range seedCategories {
		count := total * cat.share / 100
		if ci == len(seedCategories)-1 {
			count = total - n
		}
		brands := brandsFor(cat.slug)
		for i := 0; i < count; i++ {
			n++
			brand := brands[s.rng.IntN(len(brands))]
			var row productRow
			switch cat.slug {
			case "contact-lenses":
				row = s.contactLens(brand)
			case "accessories":
				row = s.accessory(brand)
			default:
				row = s.frame(cat.slug, cat.kind, brand)
			}
			row.category = cat.slug
			row.brand = brand.slug
			row.sku = fmt.Sprintf("OQ-%s-%05d", strings.ToUpper(cat.slug[:3]), n)
			row.slug = fmt.Sprintf("%s-%d", slugify(row.name+" "+row.color), n)
			row.stock = s.stock()
			row.createdAt = s.now.Add(-time.Duration(s.rng.Float64() * 365 * 24 * float64(time.Hour)))
			if s.rng.Float64() < 0.15 {
				row.compareAt = sql.NullFloat64{Float64: roundPrice(row.price * (1.15 + s.rng.Float64()*0.35)), Valid: true}
			}

			var id string
			err := stmt.QueryRowContext(s.ctx, row.sku, row.slug, row.name, row.description,
				s.categoryIDs[row.category], s.brandIDs[row.brand], row.gender, row.shape, row.material,
				row.color, row.hex, row.lensWidth, row.bridgeWidth, row.templeLength, row.price,
				row.compareAt, row.stock, row.createdAt).Scan(&id)
			if err != nil {
				return fmt.Errorf("seed product %s: %w", row.sku, err)
			}
			// Popularity is heavy-tailed so a few products collect most reviews,
			// which is what a real catalog looks like.
			s.productIDs = append(s.productIDs, seededProduct{
				id:         id,
				createdAt:  row.createdAt,
				popularity: math.Pow(s.rng.Float64(), 2.5),
				quality:    s.rng.NormFloat64() * 0.6,
			})
		}
	}
	return nil
}

func (s *seeder) frame(category, kind string, brand seedBrand) productRow {
	var (
		shapes, mats []string
		colors       = frameColors
		model        = pick(s.rng, modelNames)
		gender       = pick(s.rng, adultGenders)
		lw, bw, tl   = 46 + s.rng.IntN(13), 14 + s.rng.IntN(9), 135 + 5*s.rng.IntN(4)
		lo, hi       float64
	)
	switch category {
	case "sports-glasses":
		shapes, mats, colors = sportShapes, sportMats, sportColors
		model = pick(s.rng, sportModelNames)
		gender = "unisex"
		lw, bw, tl = 60+s.rng.IntN(10), 14+s.rng.IntN(4), 125+5*s.rng.IntN(3)
	case "kids-glasses":
		shapes, mats, colors = kidsShapes, kidsMats, kidsColors
		gender = "kids"
		lw, bw, tl = 40+s.rng.IntN(9), 14+s.rng.IntN(4), 120+5*s.rng.IntN(4)
	case "sunglasses":
		shapes, mats = sunShapes, sunMats
	default:
		shapes, mats = frameShapes, frameMats
	}

	switch brand.tier {
	case 0:
		lo, hi = 29, 79
	case 1:
		lo, hi = 79, 189
	default:
		lo, hi = 189, 429
	}
	if category == "kids-glasses" {
		lo, hi = lo*0.6, hi*0.6
	}

	shape := pick(s.rng, shapes)
	mat := pick(s.rng, mats)
	color := pickAny(s.rng, colors)
	name := fmt.Sprintf("%s %s %d", brand.name, model, 100+s.rng.IntN(900))

	var desc strings.Builder
	fmt.Fprintf(&desc, "The %s from %s pairs a %s silhouette with %s %s in %s. ",
		model, brand.name, shape, adjective(s.rng, mat), mat, strings.ToLower(color.name))
	switch kind {
	case "sun":
		fmt.Fprintf(&desc, "Fitted with %s lenses offering full UV400 protection. ", pick(s.rng, sunLenses))
	default:
		desc.WriteString("Ready for single-vision, varifocal or blue-light lenses, fitted in our own workshop. ")
	}
	if category == "kids-glasses" {
		desc.WriteString("Spring hinges and a flexible frame stand up to the playground. ")
	}
	if category == "sports-glasses" {
		desc.WriteString("Grippy nose pads and temple tips keep it in place at speed. ")
	}
	fmt.Fprintf(&desc, "Designed in %s. Includes a protective case and cleaning cloth.", brand.country)

	return productRow{
		name: name, description: desc.String(),
		gender: gender, shape: shape, material: mat, color: color.name, hex: color.hex,
		lensWidth: lw, bridgeWidth: bw, templeLength: tl,
		price: roundPrice(lo + s.rng.Float64()*(hi-lo)),
	}
}

func (s *seeder) contactLens(brand seedBrand) productRow {
	line := contactLines[s.rng.IntN(len(contactLines))]
	pi := s.rng.IntN(len(line.packs))
	color := lensColours[0]
	if line.coloured {
		color = lensColours[1+s.rng.IntN(len(lensColours)-1)]
	}
	price := line.minPrice[pi] * (1 + 0.1*float64(brand.tier)) * (1 + s.rng.Float64()*0.2)

	return productRow{
		name: fmt.Sprintf("%s %s · %d lenses", brand.name, line.name, line.packs[pi]),
		description: fmt.Sprintf("%s lenses in %s with high oxygen permeability and a moisture-locking surface. "+
			"Pack of %d. Available in powers from -12.00 to +8.00; enter your prescription at checkout.",
			line.name, line.material, line.packs[pi]),
		gender: "unisex", material: line.material, color: color.name, hex: color.hex,
		price: roundPrice(price),
	}
}

func (s *seeder) accessory(brand seedBrand) productRow {
	item := accessoryItems[s.rng.IntN(len(accessoryItems))]
	color := pickAny(s.rng, frameColors)
	return productRow{
		name:        fmt.Sprintf("%s %s", brand.name, item.name),
		description: item.description,
		gender:      "unisex", shape: item.kind, color: color.name, hex: color.hex,
		price: roundPrice(item.lo + s.rng.Float64()*(item.hi-item.lo)),
	}
}

func (s *seeder) stock() int {
	switch r := s.rng.Float64(); {
	case r < 0.04:
		return 0
	case r < 0.12:
		return 1 + s.rng.IntN(5)
	default:
		return 10 + s.rng.IntN(150)
	}
}

func (s *seeder) stores() error {
	const hours = "Mon–Fri 09:30–19:00, Sat 10:00–18:00"
	for _, c := range seedCities {
		names := []string{"Optiq " + c.city}
		if len(c.districts) > 0 {
			names = names[:0]
			for _, d := range c.districts {
				names = append(names, "Optiq "+c.city+" "+d)
			}
		}
		for _, name := range names {
			street := fmt.Sprintf("%s %d", pick(s.rng, streets), 1+s.rng.IntN(120))
			postal := fmt.Sprintf("%s%03d", c.postal, s.rng.IntN(1000))
			phone := fmt.Sprintf("+49 %s %d", strings.TrimPrefix(c.area, "0"), 1_000_000+s.rng.IntN(8_999_999))
			res, err := s.tx.ExecContext(s.ctx, `
				INSERT INTO stores (slug, name, street, postal_code, city, phone, opening_hours)
				VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (slug) DO NOTHING`,
				slugify(name), name, street, postal, c.city, phone, hours)
			if err != nil {
				return fmt.Errorf("seed store %s: %w", name, err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				s.storeCount++
			}
		}
	}
	return nil
}

func (s *seeder) customers(n int) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(SeedCustomerPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		first, last := pick(s.rng, firstNames), pick(s.rng, lastNames)
		city := seedCities[s.rng.IntN(len(seedCities))]
		email := fmt.Sprintf("%s.%s%d@example.com", asciiLower(first), asciiLower(last), i+1)
		created := s.now.Add(-time.Duration(s.rng.Float64() * 2 * 365 * 24 * float64(time.Hour)))

		var id string
		err := s.tx.QueryRowContext(s.ctx, `
			INSERT INTO users (email, password_hash, first_name, last_name, phone, street, postal_code, city, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
			ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
			RETURNING id`,
			email, string(hash), first, last,
			fmt.Sprintf("+49 1%d %d", 50+s.rng.IntN(30), 1_000_000+s.rng.IntN(8_999_999)),
			fmt.Sprintf("%s %d", pick(s.rng, streets), 1+s.rng.IntN(120)),
			fmt.Sprintf("%s%03d", city.postal, s.rng.IntN(1000)), city.city, created,
		).Scan(&id)
		if err != nil {
			return fmt.Errorf("seed customer %s: %w", email, err)
		}
		s.customerIDs = append(s.customerIDs, id)
	}
	return nil
}

var reviewTexts = map[int][]struct{ title, body string }{
	5: {
		{"Absolutely love them", "Comfortable from the first day and I get compliments all the time."},
		{"Perfect fit", "Sits perfectly on my nose, no pressure behind the ears even after a long day."},
		{"Great quality", "Feels much more expensive than it was. The finish is flawless."},
		{"Would buy again", "Second pair from this brand and they never disappoint."},
	},
	4: {
		{"Very happy", "Looks great and feels sturdy. Took a couple of days to get used to."},
		{"Good value", "Nice frame for the price, the case that comes with it is a bit basic."},
		{"Stylish", "Exactly like the photos. Slightly wider than I expected but still fine."},
	},
	3: {
		{"Okay", "Does the job. The colour is a bit darker than it looks online."},
		{"Mixed feelings", "Comfortable, but the hinges feel a little loose after a month."},
	},
	2: {
		{"Not for me", "Too heavy on the nose for all-day wear. Returned after a week."},
		{"Disappointing", "The coating started to scratch quite quickly."},
	},
	1: {
		{"Poor fit", "Kept slipping down no matter how often it was adjusted in store."},
	},
}

func (s *seeder) reviews() error {
	if len(s.customerIDs) == 0 {
		return nil
	}
	stmt, err := s.tx.PrepareContext(s.ctx, `
		INSERT INTO reviews (product_id, user_id, rating, title, body, created_at)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, p := range s.productIDs {
		count := int(p.popularity * 25)
		used := map[int]bool{}
		for j := 0; j < count; j++ {
			u := s.rng.IntN(len(s.customerIDs))
			if used[u] {
				continue
			}
			used[u] = true

			rating := int(math.Round(4.2 + p.quality + s.rng.NormFloat64()*0.9))
			rating = max(1, min(5, rating))
			text := pickAny(s.rng, reviewTexts[rating])
			age := s.now.Sub(p.createdAt)
			created := p.createdAt.Add(time.Duration(s.rng.Float64() * float64(age)))

			if _, err := stmt.ExecContext(s.ctx, p.id, s.customerIDs[u], rating, text.title, text.body, created); err != nil {
				return fmt.Errorf("seed review: %w", err)
			}
			s.reviewCount++
		}
	}

	_, err = s.tx.ExecContext(s.ctx, `
		UPDATE products p SET rating_avg = r.avg, review_count = r.cnt
		FROM (SELECT product_id, ROUND(AVG(rating)::numeric, 2) AS avg, COUNT(*) AS cnt
		      FROM reviews GROUP BY product_id) r
		WHERE r.product_id = p.id`)
	return err
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func brandsFor(category string) []seedBrand {
	var out []seedBrand
	for _, b := range seedBrands {
		for _, c := range b.categories {
			if c == category {
				out = append(out, b)
			}
		}
	}
	return out
}

func adjective(rng *rand.Rand, material string) string {
	switch material {
	case "titanium":
		return "featherlight"
	case "wood":
		return "hand-finished"
	case "acetate":
		return pick(rng, []string{"hand-polished", "Italian", "rich"})
	case "metal", "stainless steel":
		return pick(rng, []string{"slim", "fine", "brushed"})
	}
	return pick(rng, []string{"durable", "lightweight", "flexible"})
}

// roundPrice snaps to the retail-looking endings .00 and .95.
func roundPrice(p float64) float64 {
	base := math.Max(math.Round(p), 4)
	if int(base)%2 == 0 {
		return base - 0.05
	}
	return base
}

func pick(rng *rand.Rand, xs []string) string { return xs[rng.IntN(len(xs))] }

func pickAny[T any](rng *rand.Rand, xs []T) T { return xs[rng.IntN(len(xs))] }

var transliterate = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "Ä", "ae", "Ö", "oe", "Ü", "ue",
	"è", "e", "é", "e", "ş", "s", "ı", "i", "&", "and",
)

func asciiLower(s string) string {
	return strings.ToLower(transliterate.Replace(s))
}

func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range asciiLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
