package adb

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConnectChecksAuthorizationAndExactSuccess(t *testing.T) {
	for _, tc := range []struct {
		output, state string
		success       bool
	}{
		{"connected to 192.168.50.2:5555", "device", true},
		{"already connected to 192.168.50.2:5555", "device", true},
		{"not connected to 192.168.50.2:5555", "device", false},
		{"failed to authenticate to 192.168.50.2:5555", "device", false},
		{"connected to 192.168.50.2:5555", "unauthorized", false},
		{"connected to 192.168.50.2:5555", "offline", false},
	} {
		t.Run(tc.output+"/"+tc.state, func(t *testing.T) {
			_, err := connectADB(context.Background(), "192.168.50.2", func(ctx context.Context, args ...string) (string, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 15*time.Second {
					t.Fatal("missing connect deadline")
				}
				if reflect.DeepEqual(args, []string{"connect", "192.168.50.2:5555"}) {
					return tc.output, nil
				}
				if !reflect.DeepEqual(args, []string{"-s", "192.168.50.2:5555", "get-state"}) {
					t.Fatalf("unexpected command: %v", args)
				}
				return tc.state, nil
			})
			if (err == nil) != tc.success {
				t.Fatalf("success=%t error=%v", tc.success, err)
			}
		})
	}
}

func TestConnectRejectsBadTargetsAndHonorsCancellation(t *testing.T) {
	for _, address := range []string{"", "-bad", "192.168.1.1:0", "192.168.1.1:65536", "bad host", "127.0.0.1:abc"} {
		_, err := connectADB(context.Background(), address, func(context.Context, ...string) (string, error) { t.Fatal("invalid address executed"); return "", nil })
		if err == nil {
			t.Fatalf("accepted %q", address)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := connectADB(ctx, "192.168.50.2:5555", func(ctx context.Context, args ...string) (string, error) { return "", ctx.Err() })
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "192.168.50.2") {
		t.Fatal(err)
	}
}

func TestConnectRetainsIPv6ManualTargets(t *testing.T) {
	_, err := connectADB(context.Background(), "::1", func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "connect" {
			if args[1] != "[::1]:5555" {
				t.Fatalf("invalid IPv6 target: %v", args)
			}
			return "connected to [::1]:5555", nil
		}
		return "device", nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
