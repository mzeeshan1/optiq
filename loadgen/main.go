// Command loadgen simulates shoppers on the optiq shop so the database fills
// with realistic customer data and the Prometheus metrics have something to show.
//
// Each virtual user behaves like a person: lands anonymously, browses
// categories and searches, looks at products, signs up (with a plausible
// German name and address) or logs back in, fills a basket, checks out,
// tracks and sometimes cancels orders, writes reviews, keeps a wishlist and
// books eye tests. After a while they leave and a new shopper takes their place.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type config struct {
	target         string
	users          int
	rps            float64
	duration       time.Duration
	errorRate      float64
	reportInterval time.Duration
	timeout        time.Duration
	loginBurst     int
	loginCooldown  time.Duration
}

func main() {
	cfg := &config{}
	flag.StringVar(&cfg.target, "target", env("TARGET", "http://localhost:8080"), "base URL of the shop")
	flag.IntVar(&cfg.users, "users", envInt("USERS", 10), "number of concurrent shoppers")
	flag.Float64Var(&cfg.rps, "rps", envFloat("RPS", 20), "target aggregate requests per second")
	flag.DurationVar(&cfg.duration, "duration", envDuration("DURATION", 0), "how long to run (0 = until interrupted)")
	flag.Float64Var(&cfg.errorRate, "error-rate", envFloat("ERROR_RATE", 0.04), "fraction of actions that deliberately fail (declined cards, bad logins, 404s, ...)")
	flag.DurationVar(&cfg.reportInterval, "report-interval", envDuration("REPORT_INTERVAL", 15*time.Second), "how often to print a progress line")
	flag.DurationVar(&cfg.timeout, "timeout", envDuration("TIMEOUT", 10*time.Second), "per-request timeout")
	flag.IntVar(&cfg.loginBurst, "login-burst", envInt("LOGIN_BURST", 8), "POST /login attempts allowed per cooldown window (app limit is 10/min/IP)")
	flag.DurationVar(&cfg.loginCooldown, "login-cooldown", envDuration("LOGIN_COOLDOWN", 65*time.Second), "pause between login bursts, must exceed the app's 1 minute window")
	flag.Parse()

	cfg.target = strings.TrimRight(cfg.target, "/")
	if cfg.users < 1 {
		exit("users must be at least 1")
	}
	if cfg.rps <= 0 {
		exit("rps must be positive")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.duration)
		defer cancel()
	}

	sh := &shared{
		stats:    newStats(),
		tokens:   newTokenBucket(ctx, cfg.rps),
		logins:   &loginBudget{burst: cfg.loginBurst, cooldown: cfg.loginCooldown},
		accounts: &accountPool{},
	}

	fmt.Printf("loadgen → %s | shoppers=%d rps=%.0f error-rate=%.0f%% duration=%s\n",
		cfg.target, cfg.users, cfg.rps, cfg.errorRate*100, durationLabel(cfg.duration))

	var wg sync.WaitGroup
	for i := 0; i < cfg.users; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for ctx.Err() == nil {
				newShopper(idx, cfg, sh).visit(ctx)
			}
		}(i)
	}

	reportDone := make(chan struct{})
	go func() {
		defer close(reportDone)
		ticker := time.NewTicker(cfg.reportInterval)
		defer ticker.Stop()
		start := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sh.stats.reportWindow(time.Since(start), cfg.reportInterval)
			}
		}
	}()

	wg.Wait()
	<-reportDone
	sh.stats.reportFinal()
}

// shared is the state every shopper goroutine draws from.
type shared struct {
	stats    *stats
	tokens   *tokenBucket
	logins   *loginBudget
	accounts *accountPool
}

// ---------------------------------------------------------------------------
// identities
// ---------------------------------------------------------------------------

type identity struct {
	first, last, email, password string
	phone, street, postal, city  string
}

// accountPool remembers accounts created during this run so that some later
// shoppers are returning customers who log in rather than sign up.
type accountPool struct {
	mu       sync.Mutex
	accounts []identity
}

func (p *accountPool) add(id identity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.accounts) < 5000 {
		p.accounts = append(p.accounts, id)
	} else {
		p.accounts[rand.IntN(len(p.accounts))] = id
	}
}

func (p *accountPool) random() (identity, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.accounts) == 0 {
		return identity{}, false
	}
	return p.accounts[rand.IntN(len(p.accounts))], true
}

func newIdentity() identity {
	first, last := pick(firstNames), pick(lastNames)
	city := cities[rand.IntN(len(cities))]
	f, l := ascii(first), ascii(last)
	local := pick([]string{
		f + "." + l, f + l, f[:1] + "." + l, f + "_" + l, l + "." + f,
	}) + fmt.Sprintf("%d", rand.IntN(10_000))
	return identity{
		first:    first,
		last:     last,
		email:    local + "@" + pick([]string{"example.com", "example.org", "example.net"}),
		password: "shopper-" + randomToken(10),
		phone:    fmt.Sprintf("+49 1%d %d", 50+rand.IntN(30), 1_000_000+rand.IntN(8_999_999)),
		street:   fmt.Sprintf("%s %d", pick(streets), 1+rand.IntN(150)),
		postal:   fmt.Sprintf("%s%03d", city.postal, rand.IntN(1000)),
		city:     city.name,
	}
}

// ---------------------------------------------------------------------------
// shopper
// ---------------------------------------------------------------------------

type shopper struct {
	idx    int
	cfg    *config
	sh     *shared
	client *http.Client
	anon   *http.Client // no cookie jar, for unauthenticated requests

	me       identity
	loggedIn bool

	slugs      []string // products seen in listings
	productID  string   // product currently being looked at
	productURL string
	lensOpts   []string
	cartItems  []string
	orderIDs   []string
	apptIDs    []string
	storeIDs   []string
}

func newShopper(idx int, cfg *config, sh *shared) *shopper {
	return &shopper{
		idx:    idx,
		cfg:    cfg,
		sh:     sh,
		client: newClient(cfg.timeout, true),
		anon:   newClient(cfg.timeout, false),
	}
}

func newClient(timeout time.Duration, withJar bool) *http.Client {
	c := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 200,
			IdleConnTimeout:     90 * time.Second,
		},
		// Writes answer with 302s. Following them would hide the real status
		// codes and silently inflate GET traffic.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if withJar {
		jar, _ := cookiejar.New(nil)
		c.Jar = jar
	}
	return c
}

// visit is one person's whole stay on the site.
func (s *shopper) visit(ctx context.Context) {
	// Window shopping before committing to an account.
	s.home(ctx)
	for i := 0; i < 1+rand.IntN(5) && ctx.Err() == nil; i++ {
		if rand.IntN(3) == 0 {
			s.search(ctx)
		} else {
			s.browseCategory(ctx)
		}
		s.viewProduct(ctx)
	}

	// About one visitor in six leaves without ever signing in.
	if rand.IntN(6) == 0 || !s.signIn(ctx) {
		return
	}

	actions := 15 + rand.IntN(50)
	for i := 0; i < actions && ctx.Err() == nil; i++ {
		if rand.Float64() < s.cfg.errorRate {
			s.misbehave(ctx)
			continue
		}
		pickAction()(s, ctx)
	}

	if rand.IntN(2) == 0 {
		s.post(ctx, "/logout", "logout", nil)
	}
}

// signIn logs back in as a returning customer when the login budget allows,
// otherwise creates a brand new account.
func (s *shopper) signIn(ctx context.Context) bool {
	if acc, ok := s.sh.accounts.random(); ok && rand.IntN(3) == 0 && s.sh.logins.take() {
		res := s.post(ctx, "/login", "login", url.Values{"email": {acc.email}, "password": {acc.password}})
		if res.err == nil && res.status == http.StatusFound {
			s.me, s.loggedIn = acc, true
			return true
		}
	}

	for attempt := 0; attempt < 3; attempt++ {
		id := newIdentity()
		s.get(ctx, "/register", "register_page")
		res := s.post(ctx, "/register", "register", url.Values{
			"first_name": {id.first},
			"last_name":  {id.last},
			"email":      {id.email},
			"password":   {id.password},
		})
		if res.err != nil {
			return false
		}
		if res.status == http.StatusFound {
			s.me, s.loggedIn = id, true
			s.sh.accounts.add(id)
			// New customers often complete their profile straight away.
			if rand.IntN(2) == 0 {
				s.updateAccount(ctx)
			}
			return true
		}
	}
	return false
}

type action func(*shopper, context.Context)

var (
	actions     []weighted
	totalWeight int
)

type weighted struct {
	fn     action
	weight int
}

func init() {
	register := func(weight int, fn action) {
		actions = append(actions, weighted{fn, weight})
		totalWeight += weight
	}

	register(8, (*shopper).home)
	register(16, (*shopper).browseCategory)
	register(7, (*shopper).search)
	register(20, (*shopper).viewProduct)
	register(9, (*shopper).addToCart)
	register(6, (*shopper).viewCart)
	register(2, (*shopper).updateCartItem)
	register(2, (*shopper).removeCartItem)
	register(6, (*shopper).checkout)
	register(3, (*shopper).viewOrders)
	register(3, (*shopper).viewOrder)
	register(1, (*shopper).cancelOrder)
	register(3, (*shopper).toggleWishlist)
	register(2, (*shopper).viewWishlist)
	register(2, (*shopper).writeReview)
	register(2, (*shopper).viewStores)
	register(2, (*shopper).bookAppointment)
	register(1, (*shopper).viewAppointments)
	register(1, (*shopper).cancelAppointment)
	register(2, (*shopper).viewAccount)
	register(1, (*shopper).updateAccount)
}

func pickAction() action {
	n := rand.IntN(totalWeight)
	for _, a := range actions {
		if n < a.weight {
			return a.fn
		}
		n -= a.weight
	}
	return (*shopper).home
}

// ---------------------------------------------------------------------------
// browsing
// ---------------------------------------------------------------------------

func (s *shopper) home(ctx context.Context) {
	res := s.get(ctx, "/", "home")
	s.learnSlugs(res.body)
}

func (s *shopper) browseCategory(ctx context.Context) {
	q := url.Values{"category": {pick(categories)}}
	// Shoppers narrow down with a filter or two and change the sort order.
	if rand.IntN(2) == 0 {
		q.Set("gender", pick([]string{"women", "men", "unisex"}))
	}
	if rand.IntN(3) == 0 {
		q.Set("shape", pick(shapes))
	}
	if rand.IntN(4) == 0 {
		q.Set("color", pick(colors))
	}
	if rand.IntN(4) == 0 {
		band := priceBands[rand.IntN(len(priceBands))]
		if band[0] != "" {
			q.Set("min", band[0])
		}
		if band[1] != "" {
			q.Set("max", band[1])
		}
	}
	if rand.IntN(6) == 0 {
		q.Set("sale", "1")
	}
	if rand.IntN(3) == 0 {
		q.Set("sort", pick([]string{"newest", "price_asc", "price_desc", "rating"}))
	}
	if rand.IntN(4) == 0 {
		q.Set("page", fmt.Sprintf("%d", 2+rand.IntN(3)))
	}
	res := s.get(ctx, "/shop?"+q.Encode(), "browse")
	s.learnSlugs(res.body)
	if len(s.slugs) == 0 { // over-filtered: fall back to the plain category
		s.learnSlugs(s.get(ctx, "/shop?category="+q.Get("category"), "browse").body)
	}
}

func (s *shopper) search(ctx context.Context) {
	res := s.get(ctx, "/shop?q="+url.QueryEscape(pick(searchTerms)), "search")
	s.learnSlugs(res.body)
}

func (s *shopper) viewProduct(ctx context.Context) {
	if len(s.slugs) == 0 {
		s.browseCategory(ctx)
		if len(s.slugs) == 0 {
			return
		}
	}
	path := "/products/" + s.slugs[rand.IntN(len(s.slugs))]
	res := s.get(ctx, path, "product")
	if res.err != nil || res.status != http.StatusOK {
		return
	}
	s.productURL = path
	s.productID = firstMatch(productIDPattern, res.body)
	s.lensOpts = allMatches(lensOptionPattern, res.body)
	s.learnSlugs(res.body) // related products
}

// ---------------------------------------------------------------------------
// basket & checkout
// ---------------------------------------------------------------------------

func (s *shopper) addToCart(ctx context.Context) {
	if s.productID == "" {
		s.viewProduct(ctx)
		if s.productID == "" {
			return // sold out or nothing found
		}
	}
	lens := "none"
	if len(s.lensOpts) > 0 {
		// Most people buying frames want prescription lenses.
		lens = s.lensOpts[rand.IntN(len(s.lensOpts))]
	}
	qty := "1"
	if rand.IntN(8) == 0 {
		qty = "2"
	}
	res := s.postFrom(ctx, s.productURL, "/cart/items", "add_to_cart", url.Values{
		"product_id":  {s.productID},
		"lens_option": {lens},
		"quantity":    {qty},
	})
	if s.sessionLost(ctx, res) {
		return
	}
	s.productID = "" // move on to the next product
	if res.status == http.StatusFound {
		s.viewCart(ctx)
	}
}

func (s *shopper) viewCart(ctx context.Context) {
	res := s.get(ctx, "/cart", "cart")
	if s.sessionLost(ctx, res) {
		return
	}
	s.cartItems = allMatches(cartItemPattern, res.body)
}

func (s *shopper) updateCartItem(ctx context.Context) {
	id, ok := randomOf(s.cartItems)
	if !ok {
		s.addToCart(ctx)
		return
	}
	res := s.post(ctx, "/cart/items/"+id, "update_cart", url.Values{"quantity": {fmt.Sprintf("%d", 1+rand.IntN(3))}})
	s.sessionLost(ctx, res)
}

func (s *shopper) removeCartItem(ctx context.Context) {
	id, ok := randomOf(s.cartItems)
	if !ok {
		s.viewCart(ctx)
		return
	}
	res := s.post(ctx, "/cart/items/"+id+"/delete", "remove_from_cart", nil)
	if s.sessionLost(ctx, res) {
		return
	}
	s.cartItems = without(s.cartItems, id)
}

func (s *shopper) checkoutForm(payment string) url.Values {
	form := url.Values{
		"name":           {s.me.first + " " + s.me.last},
		"street":         {s.me.street},
		"postal_code":    {s.me.postal},
		"city":           {s.me.city},
		"country":        {"DE"},
		"payment_method": {payment},
		"save_address":   {"on"},
	}
	if payment == "card" {
		form.Set("card_number", pick(goodCards))
	}
	return form
}

func (s *shopper) checkout(ctx context.Context) {
	if len(s.cartItems) == 0 {
		s.addToCart(ctx)
		if len(s.cartItems) == 0 {
			return
		}
	}
	res := s.get(ctx, "/checkout", "checkout_page")
	if s.sessionLost(ctx, res) || res.status != http.StatusOK {
		return
	}

	payment := "card"
	switch r := rand.IntN(100); {
	case r < 30:
		payment = "paypal"
	case r < 45:
		payment = "invoice"
	}
	res = s.post(ctx, "/checkout", "place_order", s.checkoutForm(payment))
	if s.sessionLost(ctx, res) {
		return
	}
	switch res.status {
	case http.StatusFound:
		if id := firstMatch(orderIDPattern, []byte(`href="`+res.location+`"`)); id != "" {
			s.orderIDs = append(s.orderIDs, id)
			s.get(ctx, res.location, "order_confirmation")
		}
		s.cartItems = nil
	case http.StatusConflict:
		// Someone else bought the last one: empty the basket and move on.
		for _, id := range s.cartItems {
			s.post(ctx, "/cart/items/"+id+"/delete", "remove_from_cart", nil)
		}
		s.cartItems = nil
	}
}

// ---------------------------------------------------------------------------
// after the sale
// ---------------------------------------------------------------------------

func (s *shopper) viewOrders(ctx context.Context) {
	res := s.get(ctx, "/orders", "orders")
	if s.sessionLost(ctx, res) {
		return
	}
	s.orderIDs = allMatches(orderIDPattern, res.body)
}

func (s *shopper) viewOrder(ctx context.Context) {
	id, ok := randomOf(s.orderIDs)
	if !ok {
		s.viewOrders(ctx)
		return
	}
	res := s.get(ctx, "/orders/"+id, "order")
	s.sessionLost(ctx, res)
}

func (s *shopper) cancelOrder(ctx context.Context) {
	id, ok := randomOf(s.orderIDs)
	if !ok || rand.IntN(3) != 0 { // most people keep what they bought
		s.viewOrders(ctx)
		return
	}
	res := s.post(ctx, "/orders/"+id+"/cancel", "cancel_order", nil)
	s.sessionLost(ctx, res)
}

func (s *shopper) writeReview(ctx context.Context) {
	if s.productURL == "" {
		s.viewProduct(ctx)
		if s.productURL == "" {
			return
		}
	}
	rating := pick([]string{"5", "5", "5", "4", "4", "4", "3", "2", "1"})
	text := reviewTexts[rating[0]-'0'][rand.IntN(len(reviewTexts[rating[0]-'0']))]
	res := s.post(ctx, s.productURL+"/reviews", "review", url.Values{
		"rating": {rating}, "title": {text[0]}, "body": {text[1]},
	})
	s.sessionLost(ctx, res)
}

func (s *shopper) toggleWishlist(ctx context.Context) {
	if s.productID == "" {
		s.viewProduct(ctx)
		if s.productID == "" {
			return
		}
	}
	res := s.post(ctx, "/wishlist/"+s.productID, "wishlist_toggle", url.Values{"next": {s.productURL}})
	s.sessionLost(ctx, res)
}

func (s *shopper) viewWishlist(ctx context.Context) {
	res := s.get(ctx, "/wishlist", "wishlist")
	if s.sessionLost(ctx, res) {
		return
	}
	s.learnSlugs(res.body)
}

func (s *shopper) viewStores(ctx context.Context) {
	path := "/stores"
	if rand.IntN(2) == 0 {
		path += "?city=" + url.QueryEscape(cities[rand.IntN(len(cities))].name)
	}
	res := s.get(ctx, path, "stores")
	if ids := allMatches(storeIDPattern, res.body); len(ids) > 0 {
		s.storeIDs = ids
	}
}

func (s *shopper) appointmentForm() url.Values {
	day := time.Now().AddDate(0, 0, 1+rand.IntN(30))
	if day.Weekday() == time.Sunday {
		day = day.AddDate(0, 0, 1)
	}
	store, _ := randomOf(s.storeIDs)
	return url.Values{
		"store_id": {store},
		"service":  {pick([]string{"eye_test", "eye_test", "eye_test", "contact_lens_fitting", "frame_adjustment", "kids_eye_test"})},
		"date":     {day.Format("2006-01-02")},
		"time":     {fmt.Sprintf("%d:%s", 10+rand.IntN(8), pick([]string{"00", "30"}))},
	}
}

func (s *shopper) bookAppointment(ctx context.Context) {
	if len(s.storeIDs) == 0 {
		s.viewStores(ctx)
		if len(s.storeIDs) == 0 {
			return
		}
	}
	if s.sessionLost(ctx, s.get(ctx, "/appointments", "appointments")) {
		return
	}
	res := s.post(ctx, "/appointments", "book_appointment", s.appointmentForm())
	if s.sessionLost(ctx, res) {
		return
	}
	s.viewAppointments(ctx)
}

func (s *shopper) viewAppointments(ctx context.Context) {
	res := s.get(ctx, "/appointments", "appointments")
	if s.sessionLost(ctx, res) {
		return
	}
	s.apptIDs = allMatches(apptIDPattern, res.body)
}

func (s *shopper) cancelAppointment(ctx context.Context) {
	id, ok := randomOf(s.apptIDs)
	if !ok {
		s.viewAppointments(ctx)
		return
	}
	res := s.post(ctx, "/appointments/"+id+"/cancel", "cancel_appointment", nil)
	if s.sessionLost(ctx, res) {
		return
	}
	s.apptIDs = without(s.apptIDs, id)
}

func (s *shopper) viewAccount(ctx context.Context) {
	s.sessionLost(ctx, s.get(ctx, "/account", "account"))
}

func (s *shopper) updateAccount(ctx context.Context) {
	res := s.post(ctx, "/account", "update_account", url.Values{
		"first_name":  {s.me.first},
		"last_name":   {s.me.last},
		"phone":       {s.me.phone},
		"street":      {s.me.street},
		"postal_code": {s.me.postal},
		"city":        {s.me.city},
	})
	s.sessionLost(ctx, res)
}

// misbehave produces the failures a real shop sees every day.
func (s *shopper) misbehave(ctx context.Context) {
	switch rand.IntN(7) {
	case 0:
		if !s.sh.logins.take() {
			s.get(ctx, "/login", "login_page")
			return
		}
		s.anonPost(ctx, "/login", "bad_login", url.Values{"email": {s.me.email}, "password": {"wrong-password"}})
	case 1:
		if len(s.cartItems) == 0 {
			s.addToCart(ctx)
		}
		s.post(ctx, "/checkout", "declined_card", func() url.Values {
			f := s.checkoutForm("card")
			f.Set("card_number", "4000 0000 0000 0002")
			return f
		}())
	case 2:
		if len(s.cartItems) == 0 {
			s.addToCart(ctx)
		}
		s.post(ctx, "/checkout", "invalid_checkout", url.Values{"payment_method": {"card"}, "card_number": {"1234"}})
	case 3:
		s.get(ctx, "/products/discontinued-"+randomToken(8), "not_found")
	case 4:
		s.request(ctx, s.anon, http.MethodGet, "/orders", "unauthenticated", nil, "")
	case 5:
		// Two people chasing the same Saturday-morning slot.
		if len(s.storeIDs) == 0 {
			s.viewStores(ctx)
		}
		form := s.appointmentForm()
		s.post(ctx, "/appointments", "book_appointment", form)
		s.post(ctx, "/appointments", "double_booking", form)
	default:
		if s.productID == "" {
			s.viewProduct(ctx)
		}
		s.post(ctx, "/cart/items", "invalid_quantity", url.Values{"product_id": {s.productID}, "quantity": {"99"}})
	}
}

// sessionLost reports whether the shop bounced us to the login page, and
// signs back in when it did.
func (s *shopper) sessionLost(ctx context.Context, res result) bool {
	if res.err != nil || res.status != http.StatusFound || !strings.HasPrefix(res.location, "/login") {
		return false
	}
	s.signIn(ctx)
	return true
}

func (s *shopper) learnSlugs(body []byte) {
	if found := allMatches(productSlugPattern, body); len(found) > 0 {
		s.slugs = found
	}
}

// ---------------------------------------------------------------------------
// request plumbing
// ---------------------------------------------------------------------------

type result struct {
	status   int
	location string
	body     []byte
	err      error
}

func (s *shopper) get(ctx context.Context, path, action string) result {
	return s.request(ctx, s.client, http.MethodGet, path, action, nil, "")
}

func (s *shopper) post(ctx context.Context, path, action string, form url.Values) result {
	return s.request(ctx, s.client, http.MethodPost, path, action, form, "")
}

// postFrom submits a form as if from the page at referer, like a browser does.
func (s *shopper) postFrom(ctx context.Context, referer, path, action string, form url.Values) result {
	return s.request(ctx, s.client, http.MethodPost, path, action, form, referer)
}

func (s *shopper) anonPost(ctx context.Context, path, action string, form url.Values) result {
	return s.request(ctx, s.anon, http.MethodPost, path, action, form, "")
}

func (s *shopper) request(ctx context.Context, client *http.Client, method, path, action string, form url.Values, referer string) result {
	if !s.sh.tokens.wait(ctx) {
		return result{err: ctx.Err()}
	}

	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, s.cfg.target+path, body)
	if err != nil {
		s.sh.stats.recordError(action)
		return result{err: err}
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if referer != "" {
		req.Header.Set("Referer", s.cfg.target+referer)
	}
	req.Header.Set("User-Agent", userAgents[s.idx%len(userAgents)])

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		// Shutdown cancels in-flight requests; those are not real failures.
		if ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			s.sh.stats.recordError(action)
		}
		return result{err: err}
	}
	defer resp.Body.Close()

	payload, _ := io.ReadAll(resp.Body)
	s.sh.stats.record(action, resp.StatusCode, latency)

	loc := resp.Header.Get("Location")
	if u, err := url.Parse(loc); err == nil && u.Host != "" {
		loc = u.RequestURI() // gin may send absolute URLs; we only care about the path
	}
	return result{status: resp.StatusCode, location: loc, body: payload}
}

// ---------------------------------------------------------------------------
// pacing
// ---------------------------------------------------------------------------

type tokenBucket struct {
	tokens chan struct{}
}

func newTokenBucket(ctx context.Context, rps float64) *tokenBucket {
	b := &tokenBucket{tokens: make(chan struct{}, int(max(rps/10, 1)))}
	go func() {
		ticker := time.NewTicker(time.Duration(float64(time.Second) / rps))
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case b.tokens <- struct{}{}:
				default: // bucket full, drop the tick rather than build a backlog
				}
			}
		}
	}()
	return b
}

func (b *tokenBucket) wait(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-b.tokens:
		return true
	}
}

// loginBudget throttles POST /login across all shoppers. The shop allows 10
// login attempts per IP per minute and every shopper shares this process's IP,
// so logins are spent as a small burst and then paused until the window resets.
type loginBudget struct {
	mu       sync.Mutex
	burst    int
	cooldown time.Duration
	spent    int
	first    time.Time
}

func (b *loginBudget) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.spent >= b.burst {
		if time.Since(b.first) < b.cooldown {
			return false
		}
		b.spent = 0
	}
	if b.spent == 0 {
		b.first = time.Now()
	}
	b.spent++
	return true
}

// ---------------------------------------------------------------------------
// stats
// ---------------------------------------------------------------------------

type stats struct {
	mu       sync.Mutex
	byAction map[string]map[int]int64
	errors   map[string]int64
	window   []time.Duration
	total    int64
	lastSeen int64
}

func newStats() *stats {
	return &stats{
		byAction: map[string]map[int]int64{},
		errors:   map[string]int64{},
	}
}

func (s *stats) record(action string, status int, latency time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byAction[action] == nil {
		s.byAction[action] = map[int]int64{}
	}
	s.byAction[action][status]++
	s.window = append(s.window, latency)
	s.total++
}

func (s *stats) recordError(action string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors[action]++
}

func (s *stats) reportWindow(elapsed, interval time.Duration) {
	s.mu.Lock()
	latencies := s.window
	s.window = nil
	total := s.total
	windowCount := total - s.lastSeen
	s.lastSeen = total
	classes := map[int]int64{}
	for _, byStatus := range s.byAction {
		for status, n := range byStatus {
			classes[status/100] += n
		}
	}
	signups := s.byAction["register"][http.StatusFound]
	orders := s.byAction["place_order"][http.StatusFound]
	var errCount int64
	for _, n := range s.errors {
		errCount += n
	}
	s.mu.Unlock()

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	rate := float64(windowCount) / interval.Seconds()
	fmt.Printf("[%6s] reqs=%-7d rps=%-5.1f p50=%-7s p95=%-7s p99=%-7s 2xx=%-6d 3xx=%-6d 4xx=%-5d 5xx=%-4d signups=%-5d orders=%-5d failed=%d\n",
		elapsed.Truncate(time.Second), total, rate,
		percentile(latencies, 0.50), percentile(latencies, 0.95), percentile(latencies, 0.99),
		classes[2], classes[3], classes[4], classes[5], signups, orders, errCount)
}

func (s *stats) reportFinal() {
	s.mu.Lock()
	defer s.mu.Unlock()

	fmt.Println("\nrequests by action:")
	names := make([]string, 0, len(s.byAction))
	for name := range s.byAction {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		statuses := s.byAction[name]
		codes := make([]int, 0, len(statuses))
		var subtotal int64
		for code, n := range statuses {
			codes = append(codes, code)
			subtotal += n
		}
		sort.Ints(codes)

		parts := make([]string, 0, len(codes))
		for _, code := range codes {
			parts = append(parts, fmt.Sprintf("%d×%d", code, statuses[code]))
		}
		fmt.Printf("  %-20s %-7d %s\n", name, subtotal, strings.Join(parts, " "))
	}

	if len(s.errors) > 0 {
		fmt.Println("\ntransport failures:")
		for action, n := range s.errors {
			fmt.Printf("  %-20s %d\n", action, n)
		}
	}
	fmt.Printf("\ntotal requests: %d\n", s.total)
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i].Truncate(time.Millisecond / 10)
}

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

var (
	productSlugPattern = regexp.MustCompile(`href="/products/([a-z0-9-]+)"`)
	productIDPattern   = regexp.MustCompile(`data-product-id="([0-9a-f-]{36})"`)
	lensOptionPattern  = regexp.MustCompile(`name="lens_option" value="([a-z_]+)"`)
	cartItemPattern    = regexp.MustCompile(`/cart/items/([0-9a-f-]{36})/delete`)
	orderIDPattern     = regexp.MustCompile(`href="/orders/([0-9a-f-]{36})`)
	apptIDPattern      = regexp.MustCompile(`/appointments/([0-9a-f-]{36})/cancel`)
	storeIDPattern     = regexp.MustCompile(`data-store-id="(\d+)"`)
)

var (
	categories = []string{"eyeglasses", "eyeglasses", "eyeglasses", "sunglasses", "sunglasses",
		"sports-glasses", "kids-glasses", "contact-lenses", "accessories"}
	shapes = []string{"round", "rectangle", "square", "cat-eye", "oval", "browline", "panto", "geometric", "aviator"}
	colors = []string{"Matte Black", "Havana Brown", "Tortoise", "Crystal Clear", "Gold", "Silver",
		"Gunmetal", "Navy Blue", "Rose Gold", "Honey"}
	priceBands  = [][2]string{{"", "50"}, {"50", "100"}, {"100", "200"}, {"200", ""}}
	searchTerms = []string{
		"black", "round", "titanium", "aviator", "cat-eye", "tortoise", "gold", "nordlicht", "kaiserwerk",
		"vista nova", "daily", "monthly", "case", "cloth", "kids", "black round", "acetate", "havana",
		"blue", "sport", "rose gold", "polarised", "monocle", "pince-nez", // the last few find nothing
	}
	goodCards  = []string{"4242 4242 4242 4242", "5555 5555 5555 4444", "4012 8888 8888 1881"}
	userAgents = []string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_6) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15 optiq-loadgen",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36 optiq-loadgen",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 optiq-loadgen",
		"Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Mobile Safari/537.36 optiq-loadgen",
	}

	firstNames = []string{
		"Anna", "Lena", "Sophie", "Marie", "Laura", "Julia", "Lea", "Hannah", "Emma", "Mia", "Katharina",
		"Sarah", "Lisa", "Johanna", "Charlotte", "Nora", "Ayşe", "Elif", "Zeynep", "Fatima", "Sabine", "Petra",
		"Lukas", "Leon", "Felix", "Jonas", "Maximilian", "Paul", "Tim", "Jan", "Niklas", "Moritz", "David",
		"Tobias", "Florian", "Sebastian", "Alexander", "Mehmet", "Ali", "Can", "Piotr", "Luca", "Jürgen", "Thomas",
	}
	lastNames = []string{
		"Müller", "Schmidt", "Schneider", "Fischer", "Weber", "Meyer", "Wagner", "Becker", "Schulz", "Hoffmann",
		"Schäfer", "Koch", "Bauer", "Richter", "Klein", "Wolf", "Schröder", "Neumann", "Schwarz", "Zimmermann",
		"Braun", "Krüger", "Hofmann", "Hartmann", "Lange", "Schmitt", "Werner", "Krause", "Meier", "Lehmann",
		"Yılmaz", "Kaya", "Nowak", "Kowalski", "Rossi", "Peters", "Jansen", "Hansen", "Vogel", "Friedrich",
	}
	streets = []string{
		"Hauptstraße", "Bahnhofstraße", "Königstraße", "Lindenstraße", "Schillerstraße", "Goethestraße",
		"Kaiserstraße", "Gartenstraße", "Kirchstraße", "Bergstraße", "Mozartstraße", "Rosenweg", "Am Markt",
		"Poststraße", "Friedrichstraße", "Eichendorffweg", "Ringstraße", "Waldstraße",
	}
	cities = []struct{ name, postal string }{
		{"Hamburg", "20"}, {"Berlin", "10"}, {"München", "80"}, {"Köln", "50"}, {"Frankfurt am Main", "60"},
		{"Stuttgart", "70"}, {"Düsseldorf", "40"}, {"Leipzig", "04"}, {"Dortmund", "44"}, {"Essen", "45"},
		{"Bremen", "28"}, {"Dresden", "01"}, {"Hannover", "30"}, {"Nürnberg", "90"}, {"Bonn", "53"},
		{"Münster", "48"}, {"Kiel", "24"}, {"Lübeck", "23"}, {"Freiburg im Breisgau", "79"}, {"Mainz", "55"},
	}
	reviewTexts = map[byte][][2]string{
		5: {{"Absolutely love them", "Comfortable from day one and I get compliments all the time."},
			{"Perfect fit", "No pressure behind the ears even after a full day at the screen."},
			{"Fast and friendly", "Lenses were ready in three days, the adjustment in store took five minutes."}},
		4: {{"Very happy", "Great frame, took a couple of days to get used to the varifocals."},
			{"Good value", "Nice quality for the price. The case is a bit basic."}},
		3: {{"Okay", "Does the job. The colour is a little darker than in the photos."}},
		2: {{"Not for me", "Too heavy on my nose for all-day wear."}},
		1: {{"Poor fit", "Kept slipping down no matter how often it was adjusted."}},
	}
)

var transliterate = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "ş", "s", "ı", "i")

func ascii(s string) string { return transliterate.Replace(strings.ToLower(s)) }

func pick(xs []string) string { return xs[rand.IntN(len(xs))] }

func randomOf(xs []string) (string, bool) {
	if len(xs) == 0 {
		return "", false
	}
	return xs[rand.IntN(len(xs))], true
}

func without(xs []string, x string) []string {
	out := xs[:0:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

func firstMatch(re *regexp.Regexp, body []byte) string {
	if m := re.FindSubmatch(body); m != nil {
		return string(m[1])
	}
	return ""
}

func allMatches(re *regexp.Regexp, body []byte) []string {
	matches := re.FindAllSubmatch(body, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if v := string(m[1]); !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func randomToken(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return string(b)
}

func durationLabel(d time.Duration) string {
	if d == 0 {
		return "until interrupted"
	}
	return d.String()
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	var v int
	if _, err := fmt.Sscanf(env(key, ""), "%d", &v); err == nil {
		return v
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	var v float64
	if _, err := fmt.Sscanf(env(key, ""), "%g", &v); err == nil {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, err := time.ParseDuration(env(key, "")); err == nil {
		return v
	}
	return fallback
}

func exit(msg string) {
	fmt.Fprintln(os.Stderr, "loadgen: "+msg)
	os.Exit(2)
}
