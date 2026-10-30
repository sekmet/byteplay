package main

import "testing"

func TestLegacyRemuxSharesHLSProcessCap(t *testing.T) {
	legacy := make(chan struct{}, 3)
	shared := make(chan struct{}, 2)
	a, ok := reserveLegacyRemuxSlot(legacy, shared)
	if !ok {
		t.Fatal("first remux denied")
	}
	b, ok := reserveLegacyRemuxSlot(legacy, shared)
	if !ok {
		t.Fatal("second remux denied")
	}
	if _, ok := reserveLegacyRemuxSlot(legacy, shared); ok {
		t.Fatal("third remux exceeded global process cap")
	}
	if len(shared) != 2 || len(legacy) != 2 {
		t.Fatal("rejected remux leaked a slot")
	}
	a()
	c, ok := reserveLegacyRemuxSlot(legacy, shared)
	if !ok {
		t.Fatal("process budget not returned")
	}
	c()
	b()
	if len(shared) != 0 || len(legacy) != 0 {
		t.Fatal("process slots remain allocated")
	}
}

func TestLegacyRemuxDenialDoesNotConsumeLegacyBudget(t *testing.T) {
	legacy := make(chan struct{}, 2)
	shared := make(chan struct{}, 1)
	shared <- struct{}{}
	if _, ok := reserveLegacyRemuxSlot(legacy, shared); ok {
		t.Fatal("remux ignored active HLS worker")
	}
	if len(legacy) != 0 || len(shared) != 1 {
		t.Fatal("failed remux kept a reservation")
	}
}
