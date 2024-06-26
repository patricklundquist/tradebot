// Copyright (c) 2023 BVK Chaitanya

package limiter

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/trader"
	"github.com/bvkgo/kv"
)

func (v *Limiter) Run(ctx context.Context, rt *trader.Runtime) error {
	v.runtimeLock.Lock()
	defer v.runtimeLock.Unlock()

	log.Printf("%s:%s: started limiter job", v.uid, v.point)
	if rt.Product.ProductID() != v.productID {
		return os.ErrInvalid
	}
	// We also need to handle resume logic here.
	nupdated, err := v.fetchOrderMap(ctx, rt.Product)
	if err != nil {
		log.Printf("%s:%s: could not refresh/fetch order map: %v", v.uid, v.point, err)
		return err
	}

	if p := v.PendingSize(); p.IsZero() {
		if nupdated != 0 {
			_ = kv.WithReadWriter(ctx, rt.Database, v.Save)
		}
		asyncUpdateFinishTime(v)
		log.Printf("%s:%s: limiter is complete cause pending size is zero", v.uid, v.point)
		return nil
	}

	// Check if any of the orders in the orderMap are still active on the
	// exchange.
	var live []*exchange.Order
	for _, order := range v.dupOrderMap() {
		if !order.Done {
			live = append(live, order)
		}
	}

	nlive := len(live)
	if nlive > 1 {
		log.Printf("%s:%s: found %d live orders in the order map", v.uid, v.point, nlive)
		return fmt.Errorf("found %d live orders (want 0 or 1)", nlive)
	}

	var activeOrderID exchange.OrderID
	if nlive != 0 {
		activeOrderID = live[0].OrderID
		log.Printf("%s:%s: reusing existing order %s as the active order", v.uid, v.point, activeOrderID)
	}

	dirty := 0
	flushCh := time.After(time.Minute)

	localCtx := context.Background()

	tickerCh, stopTickers := rt.Product.TickerCh()
	defer stopTickers()

	orderUpdatesCh, stopUpdates := rt.Product.OrderUpdatesCh()
	defer stopUpdates()

	lastSizeLimit := v.sizeLimit()

	for p := v.PendingSize(); !p.IsZero(); p = v.PendingSize() {
		select {
		case <-ctx.Done():
			if activeOrderID != "" {
				log.Printf("%s:%s: canceling active limit order %v (%v)", v.uid, v.point, activeOrderID, context.Cause(ctx))
				if err := v.cancel(localCtx, rt.Product, activeOrderID); err != nil {
					return err
				}
				dirty++
			}
			if err := kv.WithReadWriter(localCtx, rt.Database, v.Save); err != nil {
				log.Printf("%s:%s dirty limit order state could not be saved to the database (will retry): %v", v.uid, v.point, err)
			}
			asyncUpdateFinishTime(v)
			return context.Cause(ctx)

		case <-flushCh:
			if dirty > 0 {
				if err := kv.WithReadWriter(ctx, rt.Database, v.Save); err != nil {
					log.Printf("%s:%s dirty limit order state could not be saved to the database (will retry): %v", v.uid, v.point, err)
				} else {
					dirty = 0
				}
			}
			flushCh = time.After(time.Minute)

		case order := <-orderUpdatesCh:
			dirty++
			v.updateOrderMap(order)
			if order.Done && order.OrderID == activeOrderID {
				log.Printf("%s:%s: limit order with server order-id %s is completed with status %q (DoneReason %q)", v.uid, v.point, activeOrderID, order.Status, order.DoneReason)
				activeOrderID = ""
			}

		case ticker := <-tickerCh:
			// We should pause this job when hold option is set, effectively pausing
			// the job. We should cancel active order if any.
			if v.holdOpt.Load() {
				if activeOrderID != "" {
					log.Printf("%v: canceling existing order %s cause option hold=true is set", v.uid, activeOrderID)
					if err := v.cancel(localCtx, rt.Product, activeOrderID); err != nil {
						return err
					}
					dirty++
					activeOrderID = ""
				}
				// Do not create any new orders.
				continue
			}

			// Cancel the active order if size-limit option value has changed; order
			// will be recreated with correct size-limit.
			if x := v.sizeLimit(); activeOrderID != "" && !lastSizeLimit.Equal(x) {
				log.Printf("%v: canceling existing order %s cause size-limit has changed from %s to %s", v.uid, activeOrderID, lastSizeLimit, x)
				if err := v.cancel(localCtx, rt.Product, activeOrderID); err != nil {
					return err
				}
				dirty++
				activeOrderID = ""
				lastSizeLimit = x
			}

			// Do not create orders when we need to wait for ticker side.
			if activeOrderID == "" && !v.isTickerSideReady(ticker.Price) {
				continue
			}

			if v.IsSell() {
				if ticker.Price.LessThanOrEqual(v.point.Cancel) {
					if activeOrderID != "" {
						if err := v.cancel(localCtx, rt.Product, activeOrderID); err != nil {
							return err
						}
						dirty++
						activeOrderID = ""
					}
				}
				if ticker.Price.GreaterThan(v.point.Cancel) {
					if activeOrderID == "" {
						id, err := v.create(localCtx, rt.Product)
						if err != nil {
							return err
						}
						dirty++
						activeOrderID = id
					}
				}
				continue
			}

			if v.IsBuy() {
				if ticker.Price.GreaterThanOrEqual(v.point.Cancel) {
					if activeOrderID != "" {
						if err := v.cancel(localCtx, rt.Product, activeOrderID); err != nil {
							return err
						}
						dirty++
						activeOrderID = ""
					}
				}
				if ticker.Price.LessThan(v.point.Cancel) {
					if activeOrderID == "" {
						id, err := v.create(localCtx, rt.Product)
						if err != nil {
							return err
						}
						dirty++
						activeOrderID = id
					}
				}
				continue
			}
		}
	}

	if _, err := v.fetchOrderMap(ctx, rt.Product); err != nil {
		return err
	}
	if err := kv.WithReadWriter(ctx, rt.Database, v.Save); err != nil {
		return err
	}
	asyncUpdateFinishTime(v)
	return nil
}

// Fix is a temporary helper interface used to fix any past mistakes.
func (v *Limiter) Fix(ctx context.Context, rt *trader.Runtime) error {
	v.runtimeLock.Lock()
	defer v.runtimeLock.Unlock()

	return nil
}

func (v *Limiter) Refresh(ctx context.Context, rt *trader.Runtime) error {
	v.runtimeLock.Lock()
	defer v.runtimeLock.Unlock()

	if _, err := v.fetchOrderMap(ctx, rt.Product); err != nil {
		return fmt.Errorf("could not refresh limiter state: %w", err)
	}
	// FIXME: We may also need to check for presence of unsaved orders with future client-ids.
	return nil
}

func (v *Limiter) create(ctx context.Context, product exchange.Product) (exchange.OrderID, error) {
	offset := v.idgen.Offset()
	clientOrderID := v.idgen.NextID()

	size := v.PendingSize()
	if s := v.sizeLimit(); size.GreaterThan(s) {
		size = s
	}
	if size.LessThan(product.BaseMinSize()) {
		size = product.BaseMinSize()
	}

	var err error
	var latency time.Duration
	var orderID exchange.OrderID
	if v.IsSell() {
		s := time.Now()
		orderID, err = product.LimitSell(ctx, clientOrderID.String(), size, v.point.Price)
		latency = time.Now().Sub(s)
	} else {
		s := time.Now()
		orderID, err = product.LimitBuy(ctx, clientOrderID.String(), size, v.point.Price)
		latency = time.Now().Sub(s)
	}
	if err != nil {
		v.idgen.RevertID()
		log.Printf("%s:%s: create limit order with client-order-id %s (%d reverted) has failed (in %s): %v", v.uid, v.point, clientOrderID, offset, latency, err)
		return "", err
	}

	v.orderMap.Store(orderID, &exchange.Order{
		OrderID:       orderID,
		ClientOrderID: clientOrderID.String(),
		Side:          v.point.Side(),
	})

	log.Printf("%s:%s: created a new limit order %s with client-order-id %s (%d) in %s", v.uid, v.point, orderID, clientOrderID, offset, latency)
	return orderID, nil
}

func (v *Limiter) cancel(ctx context.Context, product exchange.Product, activeOrderID exchange.OrderID) error {
	if err := product.Cancel(ctx, activeOrderID); err != nil {
		log.Printf("%s:%s: cancel limit order %s has failed: %v", v.uid, v.point, activeOrderID, err)
		return err
	}
	// log.Printf("%s:%s: canceled the limit order %s", v.uid, v.point, activeOrderID)
	return nil
}

func (v *Limiter) fetchOrderMap(ctx context.Context, product exchange.Product) (nupdated int, status error) {
	for id, order := range v.dupOrderMap() {
		if order.Done {
			continue
		}
		norder, err := product.Get(ctx, id)
		if err != nil {
			log.Printf("%s:%s: could not fetch order with id %s: %v", v.uid, v.point, id, err)
			return nupdated, err
		}
		v.orderMap.Store(id, norder)
		nupdated++
	}
	return nupdated, nil
}
