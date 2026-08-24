package config

import "testing"

func TestCatalogPricing(t *testing.T) {
	cat := Catalog{Models: []ModelInfoLite{
		{ID: "priced", InPrice: 1e-6, OutPrice: 5e-6, CacheReadPrice: 1e-7},
		{ID: "unpriced"},
	}}
	in, out, cr, ok := cat.Pricing("priced")
	if !ok || in != 1e-6 || out != 5e-6 || cr != 1e-7 {
		t.Fatalf("priced model: %v %v %v ok=%v", in, out, cr, ok)
	}
	if _, _, _, ok := cat.Pricing("unpriced"); ok {
		t.Fatal("model with no prices should report ok=false")
	}
	if _, _, _, ok := cat.Pricing("missing"); ok {
		t.Fatal("unknown model should report ok=false")
	}
}

func TestCatalogPricingRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cats := map[string]Catalog{
		"p": {Models: []ModelInfoLite{{ID: "m", InPrice: 1e-6, OutPrice: 5e-6, CacheReadPrice: 1e-7}}},
	}
	if err := SaveCatalogs(cats); err != nil {
		t.Fatal(err)
	}
	got := LoadCatalogs()
	in, out, cr, ok := got["p"].Pricing("m")
	if !ok || in != 1e-6 || out != 5e-6 || cr != 1e-7 {
		t.Fatalf("round-trip: %v %v %v ok=%v", in, out, cr, ok)
	}
}
