package market

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// BucketSize — шаг генерации заказов. Каждое окно генерируется ровно один раз и
// строго по порядку, поэтому остаток на складе никогда не уходит в минус.
const BucketSize = 10 * time.Minute

const historyDays = 30

type Order struct {
	PostingNumber string
	At            time.Time
	SKU           int64
	Quantity      int
	Price         float64
	Cancelled     bool
}

type sellerState struct {
	seller   Seller
	byNumber map[int64]Product
	orders   []Order // отсортированы по At
	sold     map[int64]int
	initial  map[int64]int
	next     int // номер следующего окна для генерации
}

// Generator детерминирован: одинаковые start и каталог дают одинаковые заказы.
type Generator struct {
	mu      sync.Mutex
	start   time.Time // начало истории (выровнено по BucketSize)
	origin  time.Time // "сейчас" в момент запуска — от него отсчитываются Event.FromDaysAgo
	now     func() time.Time
	sellers map[string]*sellerState
}

func NewGenerator(sellers []Seller, origin time.Time, now func() time.Time) *Generator {
	origin = origin.UTC().Truncate(BucketSize)
	g := &Generator{
		start:   origin.Add(-historyDays * 24 * time.Hour),
		origin:  origin,
		now:     now,
		sellers: map[string]*sellerState{},
	}
	for _, s := range sellers {
		st := &sellerState{seller: s, byNumber: map[int64]Product{}, sold: map[int64]int{}, initial: map[int64]int{}}
		for _, p := range s.Products {
			st.byNumber[p.SKU] = p
			days := p.StockDays
			if days == 0 {
				days = 90
			}
			// Стартовый остаток = ожидаемые продажи за историю + запас на StockDays дней.
			st.initial[p.SKU] = int(math.Ceil(p.DailyDemand*historyDays*1.1 + p.DailyDemand*days))
		}
		g.sellers[s.ClientID] = st
	}
	return g
}

func (g *Generator) Seller(clientID string) (Seller, bool) {
	st, ok := g.sellers[clientID]
	if !ok {
		return Seller{}, false
	}
	return st.seller, true
}

// hourFactor — суточный профиль спроса (ночью мало заказов, вечером пик).
func hourFactor(h int) float64 {
	switch {
	case h >= 1 && h < 7:
		return 0.25
	case h >= 18 && h < 23:
		return 1.5
	default:
		return 1.1
	}
}

func weekdayFactor(d time.Weekday) float64 {
	if d == time.Saturday || d == time.Sunday {
		return 1.2
	}
	return 0.92
}

func (g *Generator) modifiers(p Product, t time.Time) (priceMult, demandMult float64) {
	priceMult, demandMult = 1, 1
	for _, e := range p.Events {
		if t.After(g.origin.Add(-time.Duration(e.FromDaysAgo * 24 * float64(time.Hour)))) {
			priceMult *= e.PriceMult
			demandMult *= e.DemandMult
		}
	}
	return
}

func seed(parts ...string) int64 {
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return int64(h.Sum64() & math.MaxInt64)
}

func poisson(r *rand.Rand, lambda float64) int {
	l, k, p := math.Exp(-lambda), 0, 1.0
	for {
		p *= r.Float64()
		if p <= l {
			return k
		}
		k++
	}
}

// advance догенерирует все окна, которые уже полностью закончились к текущему времени.
func (g *Generator) advance(st *sellerState) {
	now := g.now().UTC()
	for {
		bStart := g.start.Add(time.Duration(st.next) * BucketSize)
		bEnd := bStart.Add(BucketSize)
		if bEnd.After(now) {
			return
		}
		r := rand.New(rand.NewSource(seed(st.seller.ClientID, fmt.Sprint(st.next))))
		var bucket []Order
		for _, p := range st.seller.Products {
			pm, dm := g.modifiers(p, bStart)
			lambda := p.DailyDemand / (24 * 6) * hourFactor(bStart.Hour()) * weekdayFactor(bStart.Weekday()) * dm
			n := poisson(r, lambda)
			for i := 0; i < n; i++ {
				qty := 1
				if r.Float64() < 0.15 {
					qty = 2
				}
				if st.sold[p.SKU]+qty > st.initial[p.SKU] {
					continue // товар закончился — заказать нельзя
				}
				o := Order{
					At:        bStart.Add(time.Duration(r.Int63n(int64(BucketSize)))),
					SKU:       p.SKU,
					Quantity:  qty,
					Price:     math.Round(p.BasePrice*pm*(0.97+r.Float64()*0.06)*100) / 100,
					Cancelled: r.Float64() < 0.04,
				}
				if !o.Cancelled {
					st.sold[p.SKU] += qty
				}
				bucket = append(bucket, o)
			}
		}
		sort.Slice(bucket, func(i, j int) bool { return bucket[i].At.Before(bucket[j].At) })
		for i := range bucket {
			bucket[i].PostingNumber = fmt.Sprintf("%s-%05d-%03d", st.seller.ClientID, st.next, i+1)
		}
		st.orders = append(st.orders, bucket...)
		st.next++
	}
}

// Status — статус отправления зависит от его возраста, поэтому со временем
// статусы меняются, и сборщику приходится перечитывать окно последних 48 часов.
func Status(o Order, now time.Time) string {
	age := now.Sub(o.At)
	switch {
	case o.Cancelled && age > time.Hour:
		return "cancelled"
	case age < 2*time.Hour:
		return "awaiting_packaging"
	case age < 26*time.Hour:
		return "delivering"
	default:
		return "delivered"
	}
}

// Orders возвращает заказы продавца в интервале [since, to).
func (g *Generator) Orders(clientID string, since, to time.Time) []Order {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.sellers[clientID]
	if !ok {
		return nil
	}
	g.advance(st)
	lo := sort.Search(len(st.orders), func(i int) bool { return !st.orders[i].At.Before(since) })
	hi := sort.Search(len(st.orders), func(i int) bool { return !st.orders[i].At.Before(to) })
	out := make([]Order, hi-lo)
	copy(out, st.orders[lo:hi])
	return out
}

type Stock struct {
	Product Product
	Present int
}

func (g *Generator) Stocks(clientID string) []Stock {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.sellers[clientID]
	if !ok {
		return nil
	}
	g.advance(st)
	out := make([]Stock, 0, len(st.seller.Products))
	for _, p := range st.seller.Products {
		out = append(out, Stock{Product: p, Present: st.initial[p.SKU] - st.sold[p.SKU]})
	}
	return out
}

// PriceNow — текущая цена товара с учётом сценарных событий.
func (g *Generator) PriceNow(p Product) float64 {
	pm, _ := g.modifiers(p, g.now().UTC())
	return math.Round(p.BasePrice*pm*100) / 100
}
