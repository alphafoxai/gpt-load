package pricing

import "gpt-load/internal/usage"

// CacheValue compares cached prompt tokens with paying the input price for them.
// ReadSavings is what cache reads saved; WritePremium is what cache writes cost
// above the input price (5-minute writes at the write price, 1-hour writes at
// the 8/5 write multiplier). Both are signed: a price table can set a cache
// price above the input price. A component whose input or cache price is not
// set contributes nothing; writes of unknown TTL are never priced.
type CacheValue struct {
	ReadSavingsNanoUSD  int64
	WritePremiumNanoUSD int64
}

// cacheValueOf uses the same schedule prices as the quote it accompanies.
func cacheValueOf(prices Prices, tokens usage.Tokens) (CacheValue, bool) {
	if !prices.Input.Set {
		return CacheValue{}, true
	}
	savings, ok := priceDifference(tokens.CacheRead, prices.Input, directPriceMultiplier, prices.CacheRead, directPriceMultiplier)
	if !ok {
		return CacheValue{}, false
	}
	premium5m, ok := priceDifference(tokens.CacheWrite5M, prices.CacheWrite, directPriceMultiplier, prices.Input, directPriceMultiplier)
	if !ok {
		return CacheValue{}, false
	}
	premium1h, ok := priceDifference(tokens.CacheWrite1H, prices.CacheWrite, cacheWriteOneHourMultiplier, prices.Input, directPriceMultiplier)
	if !ok {
		return CacheValue{}, false
	}
	premium, ok := checkedAddSigned(premium5m, premium1h)
	if !ok {
		return CacheValue{}, false
	}
	return CacheValue{ReadSavingsNanoUSD: savings, WritePremiumNanoUSD: premium}, true
}

// priceDifference returns tokens priced at (left × leftMultiplier) minus the
// same tokens at (right × rightMultiplier); 0 when either price is not set.
func priceDifference(tokens int64, left Price, leftMultiplier Multiplier, right Price, rightMultiplier Multiplier) (int64, bool) {
	if tokens == 0 || !left.Set || !right.Set {
		return 0, true
	}
	leftCost, ok := QuoteComponent(tokens, left.NanoUSDPerMillion, leftMultiplier)
	if !ok {
		return 0, false
	}
	rightCost, ok := QuoteComponent(tokens, right.NanoUSDPerMillion, rightMultiplier)
	if !ok {
		return 0, false
	}
	return int64(leftCost) - int64(rightCost), true
}

// applyMultipliers scales both amounts by the group and access-key price
// multipliers, rounding each the way the request total is rounded.
func (value CacheValue) applyMultipliers(multipliers PriceMultipliers) (CacheValue, bool) {
	savings, ok := applySignedPriceMultipliers(value.ReadSavingsNanoUSD, multipliers)
	if !ok {
		return CacheValue{}, false
	}
	premium, ok := applySignedPriceMultipliers(value.WritePremiumNanoUSD, multipliers)
	if !ok {
		return CacheValue{}, false
	}
	return CacheValue{ReadSavingsNanoUSD: savings, WritePremiumNanoUSD: premium}, true
}

func applySignedPriceMultipliers(amount int64, multipliers PriceMultipliers) (int64, bool) {
	if amount < 0 {
		scaled, ok := applyPriceMultipliers(NanoUSD(-amount), multipliers)
		return -int64(scaled), ok
	}
	scaled, ok := applyPriceMultipliers(NanoUSD(amount), multipliers)
	return int64(scaled), ok
}

func checkedAddSigned(left, right int64) (int64, bool) {
	sum := left + right
	if (right > 0 && sum < left) || (right < 0 && sum > left) {
		return 0, false
	}
	return sum, true
}
