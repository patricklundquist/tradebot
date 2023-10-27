// Copyright (c) 2023 BVK Chaitanya

package coinbase

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	testingKey     string
	testingSecret  string
	testingOptions *Options = &Options{}
)

func checkCredentials() bool {
	if len(testingKey) != 0 && len(testingSecret) != 0 {
		return true
	}
	data, err := os.ReadFile("coinbase-creds.json")
	if err != nil {
		return false
	}
	s := new(Credentials)
	if err := json.Unmarshal(data, s); err != nil {
		return false
	}
	testingKey = s.Key
	testingSecret = s.Secret
	return len(testingKey) != 0 && len(testingSecret) != 0
}

func TestClient(t *testing.T) {
	if !checkCredentials() {
		t.Skip("no credentials")
		return
	}

	c, err := New(testingKey, testingSecret, testingOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	if !slices.Contains(c.spotProducts, "BCH-USD") {
		t.Skipf("product list has no BCH-USD product")
		return
	}

	bch, err := c.NewProduct("BCH-USD")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseProduct(bch)

	orders, err := bch.List(c.ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(orders) == 0 {
		t.Skipf("no live orders to test further")
		return
	}

	testPrice, _ := decimal.NewFromString("0.01")
	testSize, _ := decimal.NewFromString("1")
	testID := uuid.New()
	testOrder, err := bch.LimitBuy(c.ctx, testID.String(), testSize, testPrice)
	if err != nil {
		t.Fatal(err)
	}
	testOrderCh := bch.OrderUpdatesCh(testOrder)
	if testOrderCh == nil {
		t.Fatalf("order updates channel cannot be nil")
	}

	go func() {
		time.Sleep(time.Second)
		if err := bch.Cancel(c.ctx, testOrder); err != nil {
			t.Fatal(err)
		}
	}()

	for order := range testOrderCh {
		if order.Done {
			t.Logf("order is done with reason %q", order.DoneReason)
			break
		}
	}

	order, err := bch.Get(c.ctx, testOrder)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("final order is: %#v", order)
}