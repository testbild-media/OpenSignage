package schedule

import (
	"sort"

	"opensignage/internal/contracts"
)

// CatalogProduct is what category prices need to know about a product: its category, the
// parent of that category ("" for a top level category) and the currency of its price.
type CatalogProduct struct {
	ID         string
	CategoryID string
	ParentID   string
	Currency   string
}

// ExpandPrices returns the rules with their category prices turned into product prices, as
// the screens evaluate them: a product gets the price of its own category, else the price of
// the parent category, unless the rule sets a price for the product itself. Products in
// another currency than the category price keep their price. Rules without category prices
// are returned as they are.
func ExpandPrices(rules []contracts.Rule, products []CatalogProduct) []contracts.Rule {
	out := make([]contracts.Rule, len(rules))
	for i, r := range rules {
		out[i] = r
		cats := map[string]contracts.Money{}
		for _, p := range r.Prices {
			if p.CategoryID != "" {
				cats[p.CategoryID] = p.Price
			}
		}
		if len(cats) == 0 {
			continue
		}
		own := map[string]bool{}
		prices := make([]contracts.PriceOverride, 0, len(r.Prices))
		for _, p := range r.Prices {
			if p.CategoryID == "" {
				prices = append(prices, p)
				own[p.ProductID] = true
			}
		}
		var expanded []contracts.PriceOverride
		for _, pr := range products {
			if own[pr.ID] {
				continue
			}
			m, ok := cats[pr.CategoryID]
			if !ok && pr.ParentID != "" {
				m, ok = cats[pr.ParentID]
			}
			if ok && m.Currency == pr.Currency {
				expanded = append(expanded, contracts.PriceOverride{ProductID: pr.ID, Price: m})
			}
		}
		sort.Slice(expanded, func(a, b int) bool { return expanded[a].ProductID < expanded[b].ProductID })
		out[i].Prices = append(prices, expanded...)
	}
	return out
}
