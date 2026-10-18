package contracts

import (
	"strings"
	"testing"
)

func TestParseHHMM(t *testing.T) {
	good := map[string]int{"00:00": 0, "09:05": 545, "23:59": 1439, "12:30": 750}
	for s, want := range good {
		if got, err := ParseHHMM(s); err != nil || got != want {
			t.Errorf("%q: %d %v (want %d)", s, got, err, want)
		}
	}
	// Exactly two ASCII digits per side: signs, spaces and other digit forms are rejected.
	for _, s := range []string{"+1:00", "-0:00", "01:+5", "00:-0", " 1:00", "1 :00", "1:00", "01:0", "24:00", "23:60",
		"0x:00", "١٢:٠٠", "01:00:00", "0100", "", ":", "aa:bb"} {
		if _, err := ParseHHMM(s); err == nil {
			t.Errorf("%q must be rejected", s)
		}
	}
}

func TestRuleValidatePricesAndName(t *testing.T) {
	base := func() Rule {
		return Rule{Name: "Happy hour", Scope: ScopeGlobal, Window: &Window{Days: []int{1}, Start: "17:00", End: "19:00"},
			Prices: []PriceOverride{{ProductID: "p1", Price: Money{Amount: 250, Currency: "EUR"}}}}
	}
	r := base()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		mutate      func(r *Rule)
		field, code string
	}{
		"negative":             {func(r *Rule) { r.Prices[0].Price.Amount = -1 }, "prices[0].price.amount", "out_of_range"},
		"too large":            {func(r *Rule) { r.Prices[0].Price.Amount = MaxMoneyAmount + 1 }, "prices[0].price.amount", "out_of_range"},
		"lower currency":       {func(r *Rule) { r.Prices[0].Price.Currency = "eur" }, "prices[0].price.currency", "invalid"},
		"digit currency":       {func(r *Rule) { r.Prices[0].Price.Currency = "E1R" }, "prices[0].price.currency", "invalid"},
		"name too long":        {func(r *Rule) { r.Name = strings.Repeat("ä", MaxRuleNameRunes+1) }, "name", "too_long"},
		"sign in window":       {func(r *Rule) { r.Window.Start = "+1:00" }, "window.start", "invalid"},
		"product and category": {func(r *Rule) { r.Prices[0].CategoryID = "c1" }, "prices", "invalid"},
		"neither":              {func(r *Rule) { r.Prices[0].ProductID = "" }, "prices", "invalid"},
		"category twice": {func(r *Rule) {
			r.Prices = []PriceOverride{{CategoryID: "c1", Price: r.Prices[0].Price}, {CategoryID: "c1", Price: r.Prices[0].Price}}
		}, "prices", "invalid"},
	}
	for name, tc := range cases {
		r := base()
		tc.mutate(&r)
		field, code := fieldOf(t, r.Validate())
		if field != tc.field || code != tc.code {
			t.Errorf("%s: %s/%s (want %s/%s)", name, field, code, tc.field, tc.code)
		}
	}
	r = base()
	r.Prices = append(r.Prices, PriceOverride{CategoryID: "p1", Price: r.Prices[0].Price}) // ids of different kinds
	if err := r.Validate(); err != nil {
		t.Errorf("product and category price: %v", err)
	}
	r = base()
	r.Name = strings.Repeat("ä", MaxRuleNameRunes) // runes, not bytes
	if err := r.Validate(); err != nil {
		t.Errorf("200 runes are allowed: %v", err)
	}
}
