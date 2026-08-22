import { describe, expect, it } from 'vitest'
import {
  balanceDisplayCurrencySymbol,
  currencySymbol,
  formatBalanceAmount,
  formatPaymentAmount,
  normalizeBalanceDisplayCurrency,
} from '../currency'

describe('formatPaymentAmount', () => {
  it('uses the currency default fraction digits', () => {
    expect(formatPaymentAmount(100, 'JPY', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'KRW', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'HKD', 'en-US')).toContain('.00')
  })
})

describe('currencySymbol', () => {
  it('maps common payment currencies and falls back safely', () => {
    expect(currencySymbol('USD')).toBe('$')
    expect(currencySymbol('cny')).toBe('¥')
    expect(currencySymbol('EUR')).toBe('€')
    expect(currencySymbol('')).toBe('¥')
    expect(currencySymbol('XYZ')).toBe('XYZ')
  })
})

describe('balance display currency', () => {
  it('always uses the fixed yuan presentation', () => {
    expect(normalizeBalanceDisplayCurrency()).toBe('CNY')
    expect(normalizeBalanceDisplayCurrency('cny')).toBe('CNY')
    expect(normalizeBalanceDisplayCurrency('USD')).toBe('CNY')
    expect(normalizeBalanceDisplayCurrency('EUR')).toBe('CNY')
    expect(balanceDisplayCurrencySymbol()).toBe('¥')
    expect(balanceDisplayCurrencySymbol('USD')).toBe('¥')
    expect(balanceDisplayCurrencySymbol('CNY')).toBe('¥')
    expect(formatBalanceAmount(12.5, 'CNY')).toBe('¥12.50')
  })

  it('does not convert the stored balance value', () => {
    expect(formatBalanceAmount(12.5, 'USD')).toBe('¥12.50')
    expect(formatBalanceAmount(12.5, 'CNY')).not.toBe('¥87.50')
  })
})
