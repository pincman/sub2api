export const DEFAULT_PAYMENT_CURRENCY = 'CNY'
/**
 * Balance values are stored in the service's accounting currency. The site
 * presents those amounts with a fixed yuan symbol and never converts them.
 */
export const DEFAULT_BALANCE_DISPLAY_CURRENCY = 'CNY'

const PAYMENT_CURRENCY_SYMBOLS: Record<string, string> = {
  USD: '$',
  CNY: '¥',
  RMB: '¥',
  EUR: '€',
  GBP: '£',
  JPY: '¥',
  HKD: 'HK$',
  TWD: 'NT$',
  KRW: '₩',
  AUD: 'A$',
  CAD: 'C$',
  SGD: 'S$',
  NZD: 'NZ$',
  MOP: 'MOP$',
  MYR: 'RM',
  THB: '฿',
  PHP: '₱',
  INR: '₹',
}

export function normalizePaymentCurrency(currency?: string | null): string {
  const normalized = String(currency || '').trim().toUpperCase()
  return /^[A-Z]{3}$/.test(normalized) ? normalized : DEFAULT_PAYMENT_CURRENCY
}

/**
 * Balance presentation is intentionally fixed to yuan across the frontend.
 * Keep this helper so existing balance views share the same behaviour without
 * depending on a server-side presentation setting.
 */
export function normalizeBalanceDisplayCurrency(_currency?: string | null): string {
  return DEFAULT_BALANCE_DISPLAY_CURRENCY
}

export function currencySymbol(currency?: string | null): string {
  const normalized = normalizePaymentCurrency(currency)
  return PAYMENT_CURRENCY_SYMBOLS[normalized] || normalized
}

/** Resolve the fixed symbol used on balance and recharge-facing screens. */
export function balanceDisplayCurrencySymbol(_currency?: string | null): string {
  return '¥'
}

/**
 * Format a balance amount with the configured display symbol.
 * This intentionally does not perform a currency conversion.
 */
export function formatBalanceAmount(
  amount: number | null | undefined,
  currency?: string | null,
  fractionDigits = 2,
): string {
  const safeAmount = Number.isFinite(Number(amount)) ? Number(amount) : 0
  const digits = Number.isInteger(fractionDigits) && fractionDigits >= 0 ? fractionDigits : 2
  return `${balanceDisplayCurrencySymbol(currency)}${safeAmount.toFixed(digits)}`
}

function paymentCurrencyFractionDigits(currency: string): number {
  try {
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency,
    }).resolvedOptions().maximumFractionDigits ?? 2
  } catch {
    return 2
  }
}

export function formatPaymentAmount(amount: number, currency?: string | null, locale?: string): string {
  const normalized = normalizePaymentCurrency(currency)
  const fractionDigits = paymentCurrencyFractionDigits(normalized)
  try {
    return new Intl.NumberFormat(locale || undefined, {
      style: 'currency',
      currency: normalized,
      currencyDisplay: 'narrowSymbol',
      minimumFractionDigits: fractionDigits,
      maximumFractionDigits: fractionDigits,
    }).format(Number.isFinite(amount) ? amount : 0)
  } catch {
    return `${normalized} ${(Number.isFinite(amount) ? amount : 0).toFixed(fractionDigits)}`
  }
}
