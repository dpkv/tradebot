// Copyright (c) 2023 BVK Chaitanya

package trader

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/bvkgo/tradebot/coinbase"
	"github.com/bvkgo/tradebot/exchange"
	"github.com/google/uuid"
)

type Trader struct {
	closeCtx   context.Context
	closeCause context.CancelCauseFunc

	wg sync.WaitGroup

	coinbaseClient *coinbase.Client

	productMap map[string]*coinbase.Product

	handlerMap map[string]http.Handler
}

func NewTrader(secrets *Secrets) (_ *Trader, status error) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer func() {
		if status != nil {
			cancel(status)
		}
	}()

	var coinbaseClient *coinbase.Client
	if secrets.Coinbase != nil {
		opts := &coinbase.Options{}
		client, err := coinbase.New(secrets.Coinbase.Key, secrets.Coinbase.Secret, opts)
		if err != nil {
			return nil, err
		}
		coinbaseClient = client
	}

	t := &Trader{
		closeCtx:       ctx,
		closeCause:     cancel,
		coinbaseClient: coinbaseClient,
		handlerMap:     make(map[string]http.Handler),
		productMap:     make(map[string]*coinbase.Product),
	}

	t.handlerMap["/trader/buy/limit"] = httpPostJSONHandler(t.doLimitBuy)
	t.handlerMap["/trader/sell/limit"] = httpPostJSONHandler(t.doLimitSell)
	t.handlerMap["/trader/loop"] = httpPostJSONHandler(t.doLoop)
	return t, nil
}

func (t *Trader) Close() error {
	t.closeCause(fmt.Errorf("trade is closing: %w", os.ErrClosed))
	t.wg.Wait()

	if t.coinbaseClient != nil {
		for _, p := range t.productMap {
			t.coinbaseClient.CloseProduct(p)
		}
		t.coinbaseClient.Close()
	}
	return nil
}

func (t *Trader) HandlerMap() map[string]http.Handler {
	return maps.Clone(t.handlerMap)
}

func (t *Trader) getProduct(ctx context.Context, name string) (exchange.Product, error) {
	product, ok := t.productMap[name]
	if !ok {
		p, err := t.coinbaseClient.NewProduct(name)
		if err != nil {
			return nil, err
		}
		product = p
		t.productMap[name] = p
	}
	return product, nil
}

func httpPostJSONHandler[T1 any, T2 any](fun func(context.Context, *T1) (*T2, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "invalid http method type", http.StatusMethodNotAllowed)
			return
		}
		if v := r.Header.Get("content-type"); !strings.EqualFold(v, "application/json") {
			http.Error(w, "unsupported content type", http.StatusBadRequest)
			return
		}
		req := new(T1)
		if err := json.NewDecoder(r.Body).Decode(req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp, err := fun(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsbytes, err := json.Marshal(resp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(jsbytes)
	})
}

func (t *Trader) doLimitBuy(ctx context.Context, req *LimitBuyRequest) (_ *LimitBuyResponse, status error) {
	defer func() {
		if status != nil {
			slog.ErrorContext(ctx, "limit-buy has failed", "error", status)
		}
	}()

	// if err := req.Check(); err != nil {
	// 	return err
	// }

	product, err := t.getProduct(ctx, req.Product)
	if err != nil {
		return nil, err
	}

	taskID := uuid.New().String()
	// TODO: We should keep track of background jobs

	buy := NewLimitBuy(product, taskID, req.BuyPrice, req.BuyCancelPrice, req.BuySize)
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()

		if err := buy.Run(t.closeCtx); err != nil {
			slog.ErrorContext(t.closeCtx, "limit-buy operation has failed", "error", err)
		} else {
			slog.InfoContext(ctx, "limit-buy has completed successfully")
		}
	}()

	resp := &LimitBuyResponse{
		TaskID: taskID,
	}
	return resp, nil
}

func (t *Trader) doLimitSell(ctx context.Context, req *LimitSellRequest) (_ *LimitSellResponse, status error) {
	defer func() {
		if status != nil {
			slog.ErrorContext(ctx, "limit-sell has failed", "error", status)
		}
	}()

	// if err := req.Check(); err != nil {
	// 	return err
	// }

	product, err := t.getProduct(ctx, req.Product)
	if err != nil {
		return nil, err
	}

	taskID := uuid.New().String()
	// TODO: We should keep track of background jobs

	sell := NewLimitSell(product, taskID, req.SellPrice, req.SellCancelPrice, req.SellSize)
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		if err := sell.Run(t.closeCtx); err != nil {
			slog.ErrorContext(t.closeCtx, "limit-sell operation has failed", "error", err)
		} else {
			slog.InfoContext(ctx, "limit-sell has completed successfully")
		}
	}()

	resp := &LimitSellResponse{
		TaskID: taskID,
	}
	return resp, nil
}

func (t *Trader) doLoop(ctx context.Context, req *LoopRequest) (_ *LoopResponse, status error) {
	defer func() {
		if status != nil {
			slog.ErrorContext(ctx, "loop has failed", "error", status)
		}
	}()

	// if err := req.Check(); err != nil {
	// 	return err
	// }

	product, err := t.getProduct(ctx, req.Product)
	if err != nil {
		return nil, err
	}

	taskID := uuid.New().String()
	// TODO: We should keep track of background jobs

	loop, err := NewLooper(product, taskID, req.BuySize, req.BuyPrice, req.BuyCancelPrice, req.SellSize, req.SellPrice, req.SellCancelPrice)
	if err != nil {
		return nil, err
	}

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()

		if err := loop.Run(t.closeCtx); err != nil {
			slog.ErrorContext(t.closeCtx, "loop operation has failed", "error", err)
		} else {
			slog.InfoContext(ctx, "loop has completed successfully")
		}
	}()

	resp := &LoopResponse{
		TaskID: taskID,
	}
	return resp, nil
}
