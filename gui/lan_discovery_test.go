package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestLANOperationGuardAndCancellationIdentity(t *testing.T) {
	a := &App{}
	ctx, finish, err := a.startLANOperation("first")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.startLANOperation("second"); err == nil {
		t.Fatal("overlapping operation allowed")
	}
	if a.CancelLANOperation("old") || a.CancelLANOperation("") || ctx.Err() != nil {
		t.Fatal("stale cancel affected current operation")
	}
	if !a.CancelLANOperation("first") || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("cancellation failed")
	}
	finish()
	_, nextFinish, err := a.startLANOperation("second")
	if err != nil {
		t.Fatal(err)
	}
	nextFinish()
}

func TestLANCancelBeforeStartCannotLeaveBackgroundScan(t *testing.T) {
	a := &App{}
	if !a.CancelLANOperation("queued") {
		t.Fatal("pending cancellation rejected")
	}
	if _, _, err := a.startLANOperation("queued"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request started: %v", err)
	}
	_, finish, err := a.startLANOperation("next")
	if err != nil {
		t.Fatal(err)
	}
	finish()
}

func TestLANBatchOnlyAcceptsDiscoveredTargets(t *testing.T) {
	candidates := map[string]bool{"192.168.50.2:5555": true}
	for _, addresses := range [][]string{nil, {"8.8.8.8:5555"}, {"192.168.50.2:37000"}, make([]string, 65)} {
		if _, err := lanConnectTargets(addresses, candidates); err == nil {
			t.Fatal("invalid selection accepted")
		}
	}
	got, err := lanConnectTargets([]string{"192.168.50.2:5555", "192.168.50.2:5555"}, candidates)
	if err != nil || !reflect.DeepEqual(got, []string{"192.168.50.2:5555"}) {
		t.Fatalf("targets=%v err=%v", got, err)
	}
}

func TestLANBatchBoundsConcurrencyAndContinuesAfterFailure(t *testing.T) {
	var active, maximum atomic.Int32
	targets := []string{"one", "bad", "three", "four", "five", "six"}
	result := connectLANBatch(context.Background(), targets, func(ctx context.Context, address string) (string, error) {
		n := active.Add(1)
		for {
			old := maximum.Load()
			if old >= n || maximum.CompareAndSwap(old, n) {
				break
			}
		}
		defer active.Add(-1)
		time.Sleep(5 * time.Millisecond)
		if address == "bad" {
			return "", fmt.Errorf("not authorized")
		}
		return "connected", nil
	}, nil)
	if len(result.Items) != 6 || maximum.Load() > 4 || maximum.Load() < 2 || active.Load() != 0 {
		t.Fatalf("batch=%+v parallel=%d", result, maximum.Load())
	}
	if result.Message != "连接完成：5/6 台成功" {
		t.Fatal(result.Message)
	}
}

func TestLANBatchCancellationStopsPendingConnections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	result := connectLANBatch(ctx, []string{"a", "b", "c", "d", "e", "f", "g", "h"}, func(ctx context.Context, address string) (string, error) {
		attempts.Add(1)
		cancel()
		return "", ctx.Err()
	}, nil)
	if !result.Canceled || attempts.Load() > 4 {
		t.Fatalf("unbounded cancellation: %+v calls=%d", result, attempts.Load())
	}
}
