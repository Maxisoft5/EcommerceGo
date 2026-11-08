package tests

import (
	"flashcart/internal/domain"
	"testing"
)

func TestNormalizeRejectsUnsafeOrders(t *testing.T) {
	cases := [][]domain.Item{nil, {{SKU: "sku", Quantity: 0}}, {{SKU: "sku", Quantity: 101}}, {{SKU: "../sku", Quantity: 1}}, {{SKU: "sku", Quantity: 1, PriceCents: 1}}, {{SKU: "sku", Quantity: 1}, {SKU: "sku", Quantity: 2}}}
	for _, items := range cases {
		if _, e := domain.Normalize(items); e == nil {
			t.Errorf("accepted invalid items: %+v", items)
		}
	}
}
func TestCanonicalRequestHash(t *testing.T) {
	a, _ := domain.Normalize([]domain.Item{{SKU: "z", Quantity: 1}, {SKU: "a", Quantity: 2}})
	b, _ := domain.Normalize([]domain.Item{{SKU: "a", Quantity: 2}, {SKU: "z", Quantity: 1}})
	if domain.Hash(a) != domain.Hash(b) {
		t.Fatal("order of JSON items changed idempotency hash")
	}
	a[0].Quantity++
	if domain.Hash(a) == domain.Hash(b) {
		t.Fatal("changed quantity did not change hash")
	}
}
func TestShardTopology(t *testing.T) {
	counts := [2]int{}
	for _, v := range []struct {
		user  string
		shard int
	}{{"alice", 1}, {"bob", 0}} {
		if got := domain.Shard(v.user); got != v.shard {
			t.Fatalf("topology changed: user=%s shard=%d expected=%d", v.user, got, v.shard)
		}
	}
	for i := 0; i < 10000; i++ {
		counts[domain.Shard(string(rune(i)))]++
	}
	if counts[0] < 4500 || counts[1] < 4500 {
		t.Fatalf("unbalanced %v", counts)
	}
}
