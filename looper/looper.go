// Copyright (c) 2023 BVK Chaitanya

package looper

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"log"
	"path"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/limiter"
	"github.com/bvk/tradebot/point"
	"github.com/bvkgo/kv"
)

const DefaultKeyspace = "/loopers"

type Looper struct {
	productID string

	key string

	buyPoint  point.Point
	sellPoint point.Point

	buys  []*limiter.Limiter
	sells []*limiter.Limiter
}

type Status struct {
	UID string

	ProductID string

	BuyPoint  point.Point
	SellPoint point.Point

	NumBuys  int
	NumSells int
}

func New(uid string, productID string, buy, sell *point.Point) (*Looper, error) {
	v := &Looper{
		productID: productID,
		key:       uid,
		buyPoint:  *buy,
		sellPoint: *sell,
	}
	if err := v.check(); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *Looper) check() error {
	if len(v.key) == 0 || !path.IsAbs(v.key) {
		return fmt.Errorf("looper uid/key %q is invalid", v.key)
	}
	if err := v.buyPoint.Check(); err != nil {
		return fmt.Errorf("buy point %v is invalid", v.buyPoint)
	}
	if side := v.buyPoint.Side(); side != "BUY" {
		return fmt.Errorf("buy point %v has invalid side", v.buyPoint)
	}
	if err := v.sellPoint.Check(); err != nil {
		return fmt.Errorf("sell point %v is invalid", v.sellPoint)
	}
	if side := v.sellPoint.Side(); side != "SELL" {
		return fmt.Errorf("sell point %v has invalid side", v.sellPoint)
	}
	return nil
}

func (v *Looper) String() string {
	return "looper:" + v.key
}

func (v *Looper) Status() *Status {
	return &Status{
		UID:       v.key,
		ProductID: v.productID,
		BuyPoint:  v.buyPoint,
		SellPoint: v.sellPoint,
		NumBuys:   len(v.buys), // FIXME: Remove the incomplete ones?
		NumSells:  len(v.sells),
	}
}

func (v *Looper) Run(ctx context.Context, product exchange.Product, db kv.Database) error {
	for ctx.Err() == nil {
		nbuys, nsells := len(v.buys), len(v.sells)

		if nbuys == 0 {
			if err := v.addNewBuy(ctx, product, db); err != nil {
				if ctx.Err() == nil {
					log.Printf("could not add limit-buy %d (retrying): %v", nbuys, err)
					time.Sleep(time.Second)
				}
			}
			continue
		}

		if last := v.buys[nbuys-1]; !last.Pending().IsZero() {
			if err := last.Run(ctx, product, db); err != nil {
				if ctx.Err() == nil {
					log.Printf("limit-buy %d has failed (retrying): %v", nbuys, err)
					time.Sleep(time.Second)
				}
			}
			continue
		}

		if nsells < nbuys {
			if err := v.addNewSell(ctx, product, db); err != nil {
				if ctx.Err() == nil {
					log.Printf("could not add limit-sell %d (retrying); %v", nsells, err)
					time.Sleep(time.Second)
				}
			}
			continue
		}

		if last := v.sells[nsells-1]; !last.Pending().IsZero() {
			if err := last.Run(ctx, product, db); err != nil {
				if ctx.Err() == nil {
					log.Printf("limit-sell %d has failed (retrying): %v", nsells, err)
					time.Sleep(time.Second)
				}
			}
			continue
		}

		if err := v.addNewBuy(ctx, product, db); err != nil {
			if ctx.Err() == nil {
				log.Printf("could not add limit-buy %d (retrying): %v", nbuys, err)
				time.Sleep(time.Second)
			}
			continue
		}
	}

	return context.Cause(ctx)
}

func (v *Looper) addNewBuy(ctx context.Context, product exchange.Product, db kv.Database) error {
	// Wait for the ticker to go above the buy point price.
	tickerCh := product.TickerCh()
	for p := v.buyPoint.Price; p.LessThanOrEqual(v.buyPoint.Price); {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case ticker := <-tickerCh:
			p = ticker.Price
		}
	}

	uid := path.Join(v.key, fmt.Sprintf("buy-%06d", len(v.buys)))
	b, err := limiter.New(uid, product.ID(), &v.buyPoint)
	if err != nil {
		return err
	}
	v.buys = append(v.buys, b)
	if err := kv.WithReadWriter(ctx, db, v.Save); err != nil {
		v.buys = v.buys[:len(v.buys)-1]
		return err
	}
	return nil
}

func (v *Looper) addNewSell(ctx context.Context, product exchange.Product, db kv.Database) error {
	// // Wait for the ticker to go below the sell point price.
	// tickerCh := product.TickerCh()
	// for p := v.sellPoint.Price; p.GreaterThanOrEqual(v.sellPoint.Price); {
	// 	log.Printf("%v:%v:%v waiting for the ticker price to go below sell point", v.key, v.buyPoint, v.sellPoint)
	// 	select {
	// 	case <-ctx.Done():
	// 		return context.Cause(ctx)
	// 	case ticker := <-tickerCh:
	// 		p = ticker.Price
	// 	}
	// }

	uid := path.Join(v.key, fmt.Sprintf("sell-%06d", len(v.sells)))
	s, err := limiter.New(uid, product.ID(), &v.sellPoint)
	if err != nil {
		return err
	}
	v.sells = append(v.sells, s)
	if err := kv.WithReadWriter(ctx, db, v.Save); err != nil {
		v.sells = v.sells[:len(v.sells)-1]
		return err
	}
	return nil
}

func (v *Looper) Save(ctx context.Context, rw kv.ReadWriter) error {
	var limiters []string
	// TODO: We can avoid saving already completed limiters repeatedly.
	for _, b := range v.buys {
		if err := b.Save(ctx, rw); err != nil {
			return err
		}
		s := b.Status()
		limiters = append(limiters, s.UID)
	}
	for _, s := range v.sells {
		if err := s.Save(ctx, rw); err != nil {
			return err
		}
		ss := s.Status()
		limiters = append(limiters, ss.UID)
	}
	gv := &gobs.LooperState{
		ProductID: v.productID,
		Limiters:  limiters,
		BuyPoint:  v.buyPoint,
		SellPoint: v.sellPoint,
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(gv); err != nil {
		return err
	}
	return rw.Set(ctx, v.key, &buf)
}

func Load(ctx context.Context, uid string, r kv.Reader) (*Looper, error) {
	gv, err := kvutil.Get[gobs.LooperState](ctx, r, uid)
	if err != nil {
		return nil, err
	}
	var buys, sells []*limiter.Limiter
	for _, id := range gv.Limiters {
		v, err := limiter.Load(ctx, id, r)
		if err != nil {
			return nil, err
		}
		if v.Side() == "BUY" {
			buys = append(buys, v)
			continue
		}
		if v.Side() == "SELL" {
			sells = append(sells, v)
			continue
		}
		return nil, fmt.Errorf("unexpected limiter side %q", v.Side())
	}

	v := &Looper{
		key:       uid,
		productID: gv.ProductID,
		buys:      buys,
		sells:     sells,
		buyPoint:  gv.BuyPoint,
		sellPoint: gv.SellPoint,
	}
	if err := v.check(); err != nil {
		return nil, err
	}
	return v, nil
}
