/**
 * Payment System Type Definitions
 */

// ==================== Enums / Union Types ====================

export type OrderStatus =
  | 'PENDING'
  | 'PAID'
  | 'RECHARGING'
  | 'COMPLETED'
  | 'EXPIRED'
  | 'CANCELLED'
  | 'FAILED'
  | 'REFUND_REQUESTED'
  | 'REFUNDING'
  | 'REFUND_PENDING'
  | 'PARTIALLY_REFUNDED'
  | 'REFUNDED'
  | 'REFUND_FAILED'

export type PaymentType = 'alipay' | 'wxpay' | 'alipay_direct' | 'wxpay_direct' | 'stripe' | 'easypay' | 'airwallex'

export type OrderType = 'balance' | 'subscription' | 'subscription_upgrade'

// ==================== Configuration ====================

export interface PaymentConfig {
  payment_enabled: boolean
  min_amount: number
  max_amount: number
  daily_limit: number
  max_pending_orders: number
  order_timeout_minutes: number
  balance_disabled: boolean
  subscription_disabled: boolean
  recharge_description: string
  balance_recharge_multiplier: number
  /** Display-only currency for account balance amounts. */
  balance_display_currency: string
  subscription_usd_to_cny_rate: number
  enabled_payment_types: PaymentType[]
  help_image_url: string
  help_text: string
  stripe_publishable_key: string
}

export interface MethodLimit {
  currency?: string
  display_name?: string
  daily_limit: number
  daily_used: number
  daily_remaining: number
  single_min: number
  single_max: number
  fee_rate: number
  available: boolean
}

/** Response from /payment/limits API */
export interface MethodLimitsResponse {
  methods: Record<string, MethodLimit>
  global_min: number  // widest min across all methods; 0 = no minimum
  global_max: number  // widest max across all methods; 0 = no maximum
}

/** Response from /payment/checkout-info API — single call for the payment page */
export interface CheckoutInfoResponse {
  methods: Record<string, MethodLimit>
  global_min: number
  global_max: number
  plans: SubscriptionPlan[]
  balance_disabled: boolean
  subscription_disabled: boolean
  /** Admin-authored Markdown shown directly below the current balance. */
  recharge_description: string
  balance_recharge_multiplier: number
  /** Display-only currency for account balance amounts; does not affect payment settlement. */
  payment_balance_display_currency: string
  /** Subscription CNY conversion rate (1 USD = X CNY); 0 = disabled, plan price is charged as-is */
  subscription_usd_to_cny_rate: number
  recharge_fee_rate: number
  help_text: string
  help_image_url: string
  stripe_publishable_key: string
  /** When true, Alipay payments on mobile always show the QR code instead of redirecting */
  alipay_force_qrcode?: boolean
  /** When true, official Alipay mobile orders use precreate plus an Alipay app deep link */
  alipay_mobile_precreate_deep_link?: boolean
}

// ==================== Orders ====================

export interface PaymentOrder {
  id: number
  user_id: number
  amount: number
  pay_amount: number
  currency?: string
  fee_rate: number
  payment_type: string
  out_trade_no: string
  status: OrderStatus
  order_type: OrderType
  created_at: string
  expires_at: string
  paid_at?: string
  completed_at?: string
  refund_amount: number
  refund_reason?: string
  refund_requested_at?: string
  refund_requested_by?: number
  refund_request_reason?: string
  plan_id?: number
  upgrade_source_subscription_id?: number
  provider_instance_id?: string
}

// ==================== Plans & Channels ====================

export interface SubscriptionPlan {
  id: number
  group_id: number
  group_platform?: string
  group_name?: string
  rate_multiplier?: number
  peak_rate_enabled?: boolean
  peak_start?: string
  peak_end?: string
  peak_rate_multiplier?: number
  daily_limit_usd?: number | null
  weekly_limit_usd?: number | null
  monthly_limit_usd?: number | null
  supported_model_scopes?: string[]
  name: string
  description: string
  price: number
  original_price?: number
  /** Display-only ISO 4217 currency label (e.g. "NZD"); empty means no label */
  currency?: string
  validity_days: number
  validity_unit: string
  /** Stored as JSON string in backend; API layer should parse before use */
  features: string[]
  for_sale: boolean
  sort_order: number
}

export interface SubscriptionUpgradeQuote {
  source_subscription_id: number
  source_plan_id: number
  source_plan_name: string
  source_group_id: number
  source_group_name: string
  source_price: number
  source_quota: number
  source_usage: number
  remaining_quota: number
  remaining_ratio: number
  credit_amount: number
  target_plan_id: number
  target_plan_name: string
  target_group_id: number
  target_group_name: string
  target_platform: string
  target_price: number
  target_quota: number
  upgrade_amount: number
  currency?: string
  validity_days: number
  new_expires_at: string
}

export interface PaymentChannel {
  id: number
  group_id?: number
  name: string
  platform: string
  rate_multiplier: number
  description: string
  models: string[]
  features: string[]
  enabled: boolean
}

// ==================== Providers ====================

export interface ProviderInstance {
  id: number
  provider_key: string
  name: string
  config: Record<string, string>
  supported_types: string[]
  enabled: boolean
  payment_mode: string
  refund_enabled: boolean
  allow_user_refund: boolean
  limits: string
  sort_order: number
}

// ==================== Request / Response ====================

export interface CreateOrderRequest {
  amount: number
  payment_type: string
  order_type: string
  plan_id?: number
  source_subscription_id?: number
  return_url?: string
  payment_source?: string
  openid?: string
  wechat_resume_token?: string
  is_mobile?: boolean
}

export type CreateOrderResultType = 'order_created' | 'oauth_required' | 'jsapi_ready'

export interface WechatOAuthInfo {
  authorize_url?: string
  appid?: string
  openid?: string
  scope?: string
  state?: string
  redirect_url?: string
}

export interface WechatJSAPIPayload {
  appId?: string
  timeStamp?: string
  nonceStr?: string
  package?: string
  signType?: string
  paySign?: string
}

export interface CreateOrderResult {
  order_id: number
  amount: number
  pay_url?: string
  qr_code?: string
  client_secret?: string
  intent_id?: string
  currency?: string
  country_code?: string
  payment_env?: string
  pay_amount: number
  fee_rate: number
  expires_at: string
  result_type?: CreateOrderResultType
  payment_type?: string
  out_trade_no?: string
  payment_mode?: string
  resume_token?: string
  alipay_mobile_precreate_deep_link?: boolean
  oauth?: WechatOAuthInfo
  jsapi?: WechatJSAPIPayload
  jsapi_payload?: WechatJSAPIPayload
}

export type CurrencyAmounts = Record<string, number>

export interface DailyPaymentStats {
  date: string
  amount: CurrencyAmounts
  count: number
}

export interface PaymentMethodStats {
  type: string
  amount: CurrencyAmounts
  count: number
}

export interface TopUserPaymentStats {
  user_id: number
  email: string
  amount: number
}

export interface DashboardStats {
  today_amount: CurrencyAmounts
  total_amount: CurrencyAmounts
  today_count: number
  total_count: number
  avg_amount: CurrencyAmounts
  daily_series: DailyPaymentStats[]
  payment_methods: PaymentMethodStats[]
  top_users: Record<string, TopUserPaymentStats[]>
}
