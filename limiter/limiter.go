// Copyright (c) 2023 BVK Chaitanya

package limiter

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/idgen"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/point"
	"github.com/bvkgo/kv"
	"github.com/shopspring/decimal"
)

const DefaultKeyspace = "/limiters/"

type Limiter struct {
	productID    string
	exchangeName string

	uid string

	point point.Point

	idgen *idgen.Generator

	// clientServerMap holds a mapping from client-order-id to
	// exchange-order-id. We keep this metadata to verify the correctness if
	// required.
	clientServerMap map[string]exchange.OrderID

	orderMap map[exchange.OrderID]*exchange.Order
}

type Status struct {
	UID string

	ProductID string

	Side string

	Point point.Point

	Pending decimal.Decimal
}

// New creates a new BUY or SELL limit order at the given price point. Limit
// orders at the exchange are canceled and recreated automatically as the
// ticker price crosses the cancel threshold and comes closer to the
// limit-price.
func New(uid, exchangeName, productID string, point *point.Point) (*Limiter, error) {
	v := &Limiter{
		productID:       productID,
		exchangeName:    exchangeName,
		uid:             uid,
		point:           *point,
		idgen:           idgen.New(uid, 0),
		orderMap:        make(map[exchange.OrderID]*exchange.Order),
		clientServerMap: make(map[string]exchange.OrderID),
	}
	if err := v.check(); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *Limiter) check() error {
	if len(v.uid) == 0 {
		return fmt.Errorf("limiter uid is empty")
	}
	if err := v.point.Check(); err != nil {
		return fmt.Errorf("limiter buy/sell point is invalid: %w", err)
	}
	return nil
}

func (v *Limiter) String() string {
	return "limiter:" + v.uid
}

func (v *Limiter) UID() string {
	return v.uid
}

func (v *Limiter) ProductID() string {
	return v.productID
}

func (v *Limiter) ExchangeName() string {
	return v.exchangeName
}

func (v *Limiter) Side() string {
	return v.point.Side()
}

func (v *Limiter) Status() *Status {
	return &Status{
		UID:       v.uid,
		ProductID: v.productID,
		Side:      v.point.Side(),
		Point:     v.point,
		Pending:   v.Pending(),
	}
}

func (v *Limiter) Pending() decimal.Decimal {
	var filled decimal.Decimal
	for _, order := range v.orderMap {
		filled = filled.Add(order.FilledSize)
	}
	return v.point.Size.Sub(filled)
}

func (v *Limiter) compactOrderMap() {
	for id, order := range v.orderMap {
		if order.Done && order.FilledSize.IsZero() {
			delete(v.orderMap, id)
			continue
		}
	}
}

func (v *Limiter) updateOrderMap(order *exchange.Order) error {
	if _, ok := v.orderMap[order.OrderID]; !ok {
		return nil
	}
	v.orderMap[order.OrderID] = order
	return nil
}

func (v *Limiter) Save(ctx context.Context, rw kv.ReadWriter) error {
	v.compactOrderMap()
	gv := &gobs.LimiterState{
		V2: &gobs.LimiterStateV2{
			ProductID:      v.productID,
			ExchangeName:   v.exchangeName,
			ClientIDSeed:   v.idgen.Seed(),
			ClientIDOffset: v.idgen.Offset(),
			TradePoint: gobs.Point{
				Size:   v.point.Size,
				Price:  v.point.Price,
				Cancel: v.point.Cancel,
			},
			ClientServerIDMap: make(map[string]string),
			ServerIDOrderMap:  make(map[string]*gobs.Order),
		},
	}
	for k, v := range v.clientServerMap {
		gv.V2.ClientServerIDMap[k] = string(v)
	}
	for k, v := range v.orderMap {
		order := &gobs.Order{
			ServerOrderID: string(v.OrderID),
			ClientOrderID: v.ClientOrderID,
			CreateTime:    gobs.RemoteTime{Time: v.CreateTime.Time},
			Side:          v.Side,
			Status:        v.Status,
			FilledFee:     v.Fee,
			FilledSize:    v.FilledSize,
			FilledPrice:   v.FilledPrice,
			Done:          v.Done,
			DoneReason:    v.DoneReason,
		}
		gv.V2.ServerIDOrderMap[string(k)] = order
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(gv); err != nil {
		return fmt.Errorf("could not encode limiter state: %w", err)
	}
	key := v.uid
	if !strings.HasPrefix(key, DefaultKeyspace) {
		v := strings.TrimPrefix(v.uid, "/wallers")
		key = path.Join(DefaultKeyspace, v)
	}
	if err := rw.Set(ctx, key, &buf); err != nil {
		return fmt.Errorf("could not save limiter state: %w", err)
	}
	return nil
}

func Load(ctx context.Context, uid string, r kv.Reader) (*Limiter, error) {
	key := uid
	if !strings.HasPrefix(key, DefaultKeyspace) {
		v := strings.TrimPrefix(uid, "/wallers")
		key = path.Join(DefaultKeyspace, v)
	}
	gv, err := kvutil.Get[gobs.LimiterState](ctx, r, key)
	if errors.Is(err, os.ErrNotExist) {
		gv, err = kvutil.Get[gobs.LimiterState](ctx, r, uid)
	}
	if err != nil {
		return nil, fmt.Errorf("could not load limiter state: %w", err)
	}
	gv.Upgrade()
	seed := uid
	if len(gv.V2.ClientIDSeed) > 0 {
		seed = gv.V2.ClientIDSeed
	}
	v := &Limiter{
		uid:          uid,
		productID:    gv.V2.ProductID,
		exchangeName: gv.V2.ExchangeName,
		idgen:        idgen.New(seed, gv.V2.ClientIDOffset),

		point: point.Point{
			Size:   gv.V2.TradePoint.Size,
			Price:  gv.V2.TradePoint.Price,
			Cancel: gv.V2.TradePoint.Cancel,
		},

		orderMap:        make(map[exchange.OrderID]*exchange.Order),
		clientServerMap: make(map[string]exchange.OrderID),
	}
	for kk, vv := range gv.V2.ClientServerIDMap {
		v.clientServerMap[kk] = exchange.OrderID(vv)
	}
	for kk, vv := range gv.V2.ServerIDOrderMap {
		order := &exchange.Order{
			OrderID:       exchange.OrderID(vv.ServerOrderID),
			ClientOrderID: vv.ClientOrderID,
			CreateTime:    exchange.RemoteTime{Time: vv.CreateTime.Time},
			Side:          vv.Side,
			Status:        vv.Status,
			Fee:           vv.FilledFee,
			FilledSize:    vv.FilledSize,
			FilledPrice:   vv.FilledPrice,
			Done:          vv.Done,
			DoneReason:    vv.DoneReason,
		}
		v.orderMap[exchange.OrderID(kk)] = order
	}
	return v, nil
}
