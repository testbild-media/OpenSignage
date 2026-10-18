package schedule

import (
	"reflect"
	"testing"
	"time"

	"opensignage/internal/contracts"
)

func TestExpandPrices(t *testing.T) {
	products := []CatalogProduct{
		{ID: "gin-tonic", CategoryID: "longdrinks", Currency: "EUR"},
		{ID: "cuba-libre", CategoryID: "longdrinks", Currency: "EUR"},
		{ID: "vodka-o", CategoryID: "vodka", ParentID: "longdrinks", Currency: "EUR"},
		{ID: "vodka-e", CategoryID: "vodka", ParentID: "longdrinks", Currency: "EUR"},
		{ID: "swiss", CategoryID: "longdrinks", Currency: "CHF"},
		{ID: "pils", CategoryID: "beer", Currency: "EUR"},
	}
	rule := contracts.Rule{ID: "happy", Prices: []contracts.PriceOverride{
		{CategoryID: "longdrinks", Price: eur(500)},
		{CategoryID: "vodka", Price: eur(450)},
		{ProductID: "gin-tonic", Price: eur(550)},
	}}
	plain := contracts.Rule{ID: "plain", Prices: []contracts.PriceOverride{{ProductID: "pils", Price: eur(300)}}}
	got := ExpandPrices([]contracts.Rule{rule, plain}, products)
	want := []contracts.PriceOverride{
		{ProductID: "gin-tonic", Price: eur(550)}, // own product price wins
		{ProductID: "cuba-libre", Price: eur(500)},
		{ProductID: "vodka-e", Price: eur(450)}, // subcategory before category
		{ProductID: "vodka-o", Price: eur(450)},
		// "swiss" keeps its CHF price, "pils" is in another category
	}
	if !reflect.DeepEqual(got[0].Prices, want) {
		t.Fatalf("expanded %+v", got[0].Prices)
	}
	if !reflect.DeepEqual(got[1], plain) {
		t.Fatalf("rules without category prices stay: %+v", got[1])
	}
	if len(rule.Prices) != 3 || rule.Prices[0].CategoryID != "longdrinks" {
		t.Fatal("the input rules are not changed")
	}

	// The evaluation then works on product prices as before; a higher rule still wins.
	zone := contracts.Rule{ID: "z", Scope: contracts.ScopeZone, ZoneID: "bar", Enabled: true,
		Window: &contracts.Window{Days: []int{1, 2, 3, 4, 5, 6, 7}, Start: "00:00", End: "00:00"}, Prices: rule.Prices}
	def := contracts.Rule{ID: "d", Scope: contracts.ScopeDefault, ZoneID: "bar", Enabled: true,
		Prices: []contracts.PriceOverride{{ProductID: "cuba-libre", Price: eur(700)}, {ProductID: "pils", Price: eur(320)}}}
	res := Evaluate(Input{Rules: ExpandPrices([]contracts.Rule{zone, def}, products), ZoneID: "bar", Now: at(time.UTC, "2026-10-12 20:00")})
	if res.Prices["cuba-libre"] != eur(500) || res.Prices["pils"] != eur(320) || res.Prices["vodka-o"] != eur(450) {
		t.Fatalf("evaluated %+v", res.Prices)
	}
	if c := Changes(rule); !reflect.DeepEqual(c, []string{"price:category:longdrinks", "price:category:vodka", "price:gin-tonic"}) {
		t.Fatalf("changes %v", c)
	}
}
