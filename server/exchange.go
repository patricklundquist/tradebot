// Copyright (c) 2023 BVK Chaitanya

package server

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/bvk/tradebot/api"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
)

func (s *Server) doExchangeGetOrder(ctx context.Context, req *api.ExchangeGetOrderRequest) (*api.ExchangeGetOrderResponse, error) {
	ex, ok := s.exchangeMap[strings.ToLower(req.Name)]
	if !ok {
		return nil, fmt.Errorf("no exchange with name %q: %w", req.Name, os.ErrNotExist)
	}
	order, err := ex.GetOrder(ctx, exchange.OrderID(req.OrderID))
	if err != nil {
		return &api.ExchangeGetOrderResponse{Error: err.Error()}, nil
	}
	resp := &api.ExchangeGetOrderResponse{
		Order: &gobs.Order{
			ServerOrderID: string(order.OrderID),
			ClientOrderID: order.ClientOrderID,
			Side:          order.Side,
			Status:        order.Status,
			CreateTime:    gobs.RemoteTime{Time: order.CreateTime.Time},
			FinishTime:    gobs.RemoteTime{Time: order.FinishTime.Time},
			FilledFee:     order.Fee,
			FilledSize:    order.FilledSize,
			FilledPrice:   order.FilledPrice,
			Done:          order.Done,
			DoneReason:    order.DoneReason,
		},
	}
	return resp, nil
}

func (s *Server) doGetProduct(ctx context.Context, req *api.ExchangeGetProductRequest) (*api.ExchangeGetProductResponse, error) {
	ex, ok := s.exchangeMap[strings.ToLower(req.ExchangeName)]
	if !ok {
		return nil, fmt.Errorf("no exchange with name %q: %w", req.ExchangeName, os.ErrNotExist)
	}
	product, err := ex.GetProduct(ctx, req.ProductID)
	if err != nil {
		return &api.ExchangeGetProductResponse{Error: err.Error()}, nil
	}
	return &api.ExchangeGetProductResponse{Product: product}, nil
}
