// Package market — синтетический эмулятор подмножества Ozon Seller API.
//
// Реального кабинета продавца нет, поэтому данные генерируются детерминированно
// (seed от client_id и номера временного окна), но в формате и с ограничениями,
// похожими на настоящий API: пагинация offset/has_next и cursor, лимит запросов
// с ответом 429, авторизация заголовками Client-Id / Api-Key.
package market

// Event — сценарное изменение поведения товара, начиная с FromDaysAgo дней назад
// относительно старта эмулятора. На них проверяется, находит ли агент аномалии.
type Event struct {
	FromDaysAgo float64
	PriceMult   float64
	DemandMult  float64
}

type Product struct {
	SKU         int64
	OfferID     string
	Name        string
	BasePrice   float64
	DailyDemand float64 // среднее число заказанных штук в сутки
	// StockDays — на сколько дней спроса (сверх истории) хватает стартового остатка.
	// 0 — берём с большим запасом (90 дней).
	StockDays float64
	Events    []Event
}

type Seller struct {
	ClientID string
	APIKey   string
	Name     string
	Products []Product
}

// DefaultSellers — два продавца с заранее заложенными аномалиями:
//   - seller 1001: цену на худи подняли на 25% 4 дня назад → спрос упал;
//   - seller 2002: плед заканчивается через ~3 дня; спрос на полотенца упал
//     без изменения цены (внешняя причина — агент должен это различить).
func DefaultSellers() []Seller {
	return []Seller{
		{
			ClientID: "1001", APIKey: "demo-key-1001", Name: "Одежда «Северный ветер»",
			Products: []Product{
				{SKU: 100101, OfferID: "TSHIRT-WHT-M", Name: "Футболка базовая белая", BasePrice: 990, DailyDemand: 42},
				{SKU: 100102, OfferID: "TSHIRT-BLK-M", Name: "Футболка базовая чёрная", BasePrice: 990, DailyDemand: 38},
				{SKU: 100103, OfferID: "HOODIE-BLK-OVR", Name: "Худи оверсайз чёрное", BasePrice: 3490, DailyDemand: 24,
					Events: []Event{{FromDaysAgo: 4, PriceMult: 1.25, DemandMult: 0.35}}},
				{SKU: 100104, OfferID: "JEANS-STR-32", Name: "Джинсы прямые синие", BasePrice: 4290, DailyDemand: 15},
				{SKU: 100105, OfferID: "SOCKS-5PK", Name: "Носки хлопковые, 5 пар", BasePrice: 590, DailyDemand: 55},
				{SKU: 100106, OfferID: "CAP-BASE-GRY", Name: "Кепка базовая серая", BasePrice: 1190, DailyDemand: 11},
				{SKU: 100107, OfferID: "SWEAT-GRN-L", Name: "Свитшот зелёный", BasePrice: 2790, DailyDemand: 13},
				{SKU: 100108, OfferID: "SHORTS-SPRT", Name: "Шорты спортивные", BasePrice: 1490, DailyDemand: 9},
			},
		},
		{
			ClientID: "2002", APIKey: "demo-key-2002", Name: "Дом и уют «Тёплый угол»",
			Products: []Product{
				{SKU: 200201, OfferID: "PLAID-FLC-150", Name: "Плед флисовый 150x200", BasePrice: 1890, DailyDemand: 30, StockDays: 3},
				{SKU: 200202, OfferID: "TOWEL-SET-3", Name: "Набор полотенец, 3 шт", BasePrice: 1590, DailyDemand: 26,
					Events: []Event{{FromDaysAgo: 3, PriceMult: 1.0, DemandMult: 0.5}}},
				{SKU: 200203, OfferID: "PILLOW-50-70", Name: "Подушка 50x70", BasePrice: 1290, DailyDemand: 21},
				{SKU: 200204, OfferID: "MUG-CER-350", Name: "Кружка керамическая 350 мл", BasePrice: 490, DailyDemand: 48},
				{SKU: 200205, OfferID: "CANDLE-VAN", Name: "Свеча ароматическая «Ваниль»", BasePrice: 690, DailyDemand: 19},
				{SKU: 200206, OfferID: "BEDSET-EURO", Name: "Комплект постельного белья евро", BasePrice: 4990, DailyDemand: 8},
				{SKU: 200207, OfferID: "RUG-BATH-GRY", Name: "Коврик для ванной серый", BasePrice: 890, DailyDemand: 14},
			},
		},
	}
}
