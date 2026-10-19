package pricing

import (
	"testing"

	"gpt-load/internal/usage"
)

func TestCacheValueNeedsTheInputPriceAndEachCachePrice(t *testing.T) {
	identity := Identity{ChannelID: "anthropic", ModelID: "claude"}
	tokens := usage.Tokens{CacheRead: 1_000_000, CacheWrite5M: 1_000_000, CacheWrite1H: 1_000_000, Output: 1}
	for name, test := range map[string]struct {
		prices Prices
		want   CacheValue
	}{
		// 没有输入价就没有比较基准，节省和溢价都不计。
		"no input price": {Prices{Output: fixedPrice(1), CacheRead: fixedPrice(1), CacheWrite: fixedPrice(5)}, CacheValue{}},
		// 缺读取价只影响节省；写入溢价照算。
		"no read price": {Prices{Input: fixedPrice(4), Output: fixedPrice(1), CacheWrite: fixedPrice(5)}, CacheValue{WritePremiumNanoUSD: (5 - 4) + (8 - 4)}},
		// 读取价高于输入价时节省为负，如实记录；1 小时写入 1×1.6 按计费规则四舍五入为 2。
		"read above input": {Prices{Input: fixedPrice(2), Output: fixedPrice(1), CacheRead: fixedPrice(3), CacheWrite: fixedPrice(1)}, CacheValue{ReadSavingsNanoUSD: -1, WritePremiumNanoUSD: (1 - 2) + (2 - 2)}},
	} {
		t.Run(name, func(t *testing.T) {
			table := mustTable(t, Rule{Identity: identity, Prices: test.prices})
			quote := table.Quote(identity, usage.Result{State: usage.StateComplete, Tokens: tokens})
			if quote.State != CostStatePriced || quote.CacheValue != test.want {
				t.Fatalf("Quote() = %#v, want priced with cache value %#v", quote, test.want)
			}
		})
	}
}

func TestCacheValueFollowsRequestPriceMultipliersWithSign(t *testing.T) {
	identity := Identity{ChannelID: "anthropic", ModelID: "claude"}
	table := mustTable(t, Rule{Identity: identity, Prices: Prices{
		Input: fixedPrice(100), Output: fixedPrice(1), CacheRead: fixedPrice(10), CacheWrite: fixedPrice(50),
	}})
	quote, _ := table.QuoteForModeWithMultipliers(identity, usage.Result{
		State:  usage.StateComplete,
		Tokens: usage.Tokens{CacheRead: 1_000_000, CacheWrite5M: 1_000_000},
	}, ModeStandard, PriceMultipliers{Group: 800_000, AccessKey: 1_500_000})
	// 读取省 90、写入低于输入价 50，按 0.8 × 1.5 = 1.2 倍。
	want := CacheValue{ReadSavingsNanoUSD: 108, WritePremiumNanoUSD: -60}
	if quote.State != CostStatePriced || quote.CacheValue != want {
		t.Fatalf("QuoteForModeWithMultipliers() = %#v, want cache value %#v", quote, want)
	}
}
