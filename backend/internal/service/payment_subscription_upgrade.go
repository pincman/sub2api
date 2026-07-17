package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const upgradeAuditAction = "SUBSCRIPTION_UPGRADE_APPLIED"

// SubscriptionUpgradeQuote is a server-authoritative preview. The residual
// credit is based on the unused share of the source monthly quota, not time:
//
//	credit = source price * (source quota - source usage) / source quota
//	amount = target price - credit
//
// A successful upgrade starts a fresh target term with zero usage.
type SubscriptionUpgradeQuote struct {
	SourceSubscriptionID int64     `json:"source_subscription_id"`
	SourcePlanID         int64     `json:"source_plan_id"`
	SourcePlanName       string    `json:"source_plan_name"`
	SourceGroupID        int64     `json:"source_group_id"`
	SourceGroupName      string    `json:"source_group_name"`
	SourcePrice          float64   `json:"source_price"`
	SourceQuota          float64   `json:"source_quota"`
	SourceUsage          float64   `json:"source_usage"`
	RemainingQuota       float64   `json:"remaining_quota"`
	RemainingRatio       float64   `json:"remaining_ratio"`
	CreditAmount         float64   `json:"credit_amount"`
	TargetPlanID         int64     `json:"target_plan_id"`
	TargetPlanName       string    `json:"target_plan_name"`
	TargetGroupID        int64     `json:"target_group_id"`
	TargetGroupName      string    `json:"target_group_name"`
	TargetPlatform       string    `json:"target_platform"`
	TargetPrice          float64   `json:"target_price"`
	TargetQuota          float64   `json:"target_quota"`
	UpgradeAmount        float64   `json:"upgrade_amount"`
	Currency             string    `json:"currency,omitempty"`
	ValidityDays         int       `json:"validity_days"`
	NewExpiresAt         time.Time `json:"new_expires_at"`
}

func roundUpgradeMoney(value float64) float64 {
	if value <= 0 {
		return 0
	}
	result, _ := decimal.NewFromFloat(value).Round(2).Float64()
	return result
}

func effectiveMonthlyUsage(sub *dbent.UserSubscription, now time.Time) float64 {
	if sub == nil || sub.MonthlyWindowStart == nil || !now.Before(sub.MonthlyWindowStart.Add(30*24*time.Hour)) {
		return 0
	}
	return math.Max(0, sub.MonthlyUsageUsd)
}

type subscriptionUpgradeSourcePurchase struct {
	plan          *dbent.SubscriptionPlan
	quotaOverride *float64
}

func upgradeSnapshotFloat(snapshot map[string]any, key string) (float64, bool) {
	value, ok := snapshot[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, math.IsNaN(typed) == false && math.IsInf(typed, 0) == false
	case float32:
		result := float64(typed)
		return result, math.IsNaN(result) == false && math.IsInf(result, 0) == false
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		result, err := typed.Float64()
		return result, err == nil
	default:
		return 0, false
	}
}

func upgradeSnapshotString(snapshot map[string]any, key string) (string, bool) {
	value, ok := snapshot[key].(string)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

// latestSubscriptionPurchaseForGroup resolves the immutable commercial value
// of the subscription the user actually bought. A plan may be edited after a
// sale, so using the plan's current price would silently change upgrade credit.
// Chained upgrades use the previous upgrade snapshot's full target value, not
// the difference that was paid for that upgrade.
func (s *PaymentService) latestSubscriptionPurchaseForGroup(ctx context.Context, client *dbent.Client, userID, groupID int64) (*subscriptionUpgradeSourcePurchase, error) {
	order, err := client.PaymentOrder.Query().
		Where(
			paymentorder.UserIDEQ(userID),
			paymentorder.SubscriptionGroupIDEQ(groupID),
			paymentorder.PlanIDNotNil(),
			paymentorder.StatusEQ(OrderStatusCompleted),
			paymentorder.OrderTypeIn(payment.OrderTypeSubscription, payment.OrderTypeSubscriptionUpgrade),
		).
		Order(dbent.Desc(paymentorder.FieldCompletedAt), dbent.Desc(paymentorder.FieldID)).
		First(ctx)
	if err != nil || order.PlanID == nil {
		return nil, infraerrors.BadRequest("UPGRADE_SOURCE_PLAN_NOT_FOUND", "the active subscription was not created by a purchasable plan")
	}
	plan, err := client.SubscriptionPlan.Get(ctx, *order.PlanID)
	if err != nil {
		return nil, infraerrors.BadRequest("UPGRADE_SOURCE_PLAN_NOT_FOUND", "the source subscription plan no longer exists")
	}
	planCopy := *plan
	result := &subscriptionUpgradeSourcePurchase{plan: &planCopy}
	if order.OrderType == payment.OrderTypeSubscriptionUpgrade {
		if price, ok := upgradeSnapshotFloat(order.UpgradeSnapshot, "target_price"); ok && price > 0 {
			result.plan.Price = price
		}
		if quota, ok := upgradeSnapshotFloat(order.UpgradeSnapshot, "target_quota"); ok && quota > 0 {
			result.quotaOverride = &quota
		}
		if currency, ok := upgradeSnapshotString(order.UpgradeSnapshot, "currency"); ok {
			result.plan.Currency = currency
		}
	} else if order.Amount > 0 {
		result.plan.Price = order.Amount
	}
	return result, nil
}

func sourceGroupWithPurchasedQuota(group *Group, quotaOverride *float64) *Group {
	if group == nil || quotaOverride == nil || *quotaOverride <= 0 {
		return group
	}
	copy := *group
	quota := *quotaOverride
	copy.MonthlyLimitUSD = &quota
	return &copy
}

func targetSubscriptionBlocksUpgrade(sub *dbent.UserSubscription, now time.Time) bool {
	if sub == nil || !sub.ExpiresAt.After(now) {
		return false
	}
	return sub.Status == SubscriptionStatusActive || sub.Status == SubscriptionStatusSuspended
}

func buildSubscriptionUpgradeQuote(now time.Time, sub *dbent.UserSubscription, sourcePlan, targetPlan *dbent.SubscriptionPlan, sourceGroup, targetGroup *Group) (*SubscriptionUpgradeQuote, error) {
	if sub == nil || sourcePlan == nil || targetPlan == nil || sourceGroup == nil || targetGroup == nil {
		return nil, infraerrors.BadRequest("INVALID_UPGRADE", "subscription upgrade context is incomplete")
	}
	if sourceGroup.MonthlyLimitUSD == nil || *sourceGroup.MonthlyLimitUSD <= 0 {
		return nil, infraerrors.BadRequest("UPGRADE_SOURCE_QUOTA_UNSUPPORTED", "source plan must have a positive monthly quota")
	}
	if targetGroup.MonthlyLimitUSD == nil || *targetGroup.MonthlyLimitUSD <= *sourceGroup.MonthlyLimitUSD {
		return nil, infraerrors.BadRequest("UPGRADE_TARGET_QUOTA_INVALID", "target plan monthly quota must be greater than the source plan")
	}
	if sourceGroup.Platform != targetGroup.Platform || !targetGroup.IsSubscriptionType() || targetGroup.Status != payment.EntityStatusActive {
		return nil, infraerrors.BadRequest("UPGRADE_TARGET_INCOMPATIBLE", "target plan must be an active subscription on the same platform")
	}
	if targetPlan.GroupID == sourcePlan.GroupID || targetPlan.Price <= sourcePlan.Price {
		return nil, infraerrors.BadRequest("UPGRADE_TARGET_NOT_HIGHER", "target plan must have a higher price and a different group")
	}
	if strings.TrimSpace(sourcePlan.Currency) != strings.TrimSpace(targetPlan.Currency) {
		return nil, infraerrors.BadRequest("UPGRADE_CURRENCY_MISMATCH", "source and target plan currencies must match")
	}

	sourceQuota := *sourceGroup.MonthlyLimitUSD
	sourceUsage := math.Min(sourceQuota, effectiveMonthlyUsage(sub, now))
	remainingQuota := math.Max(0, sourceQuota-sourceUsage)
	remainingRatio := remainingQuota / sourceQuota
	credit := roundUpgradeMoney(sourcePlan.Price * remainingRatio)
	amount := roundUpgradeMoney(targetPlan.Price - credit)
	if amount <= 0 {
		return nil, infraerrors.BadRequest("UPGRADE_AMOUNT_INVALID", "calculated upgrade amount must be positive")
	}
	validityDays := psComputeValidityDays(targetPlan.ValidityDays, targetPlan.ValidityUnit)
	if validityDays <= 0 {
		return nil, infraerrors.BadRequest("UPGRADE_VALIDITY_INVALID", "target plan validity is invalid")
	}

	return &SubscriptionUpgradeQuote{
		SourceSubscriptionID: sub.ID,
		SourcePlanID:         int64(sourcePlan.ID),
		SourcePlanName:       sourcePlan.Name,
		SourceGroupID:        sub.GroupID,
		SourceGroupName:      sourceGroup.Name,
		SourcePrice:          sourcePlan.Price,
		SourceQuota:          sourceQuota,
		SourceUsage:          sourceUsage,
		RemainingQuota:       remainingQuota,
		RemainingRatio:       remainingRatio,
		CreditAmount:         credit,
		TargetPlanID:         int64(targetPlan.ID),
		TargetPlanName:       targetPlan.Name,
		TargetGroupID:        targetPlan.GroupID,
		TargetGroupName:      targetGroup.Name,
		TargetPlatform:       targetGroup.Platform,
		TargetPrice:          targetPlan.Price,
		TargetQuota:          *targetGroup.MonthlyLimitUSD,
		UpgradeAmount:        amount,
		Currency:             targetPlan.Currency,
		ValidityDays:         validityDays,
		NewExpiresAt:         now.AddDate(0, 0, validityDays),
	}, nil
}

func (s *PaymentService) loadSubscriptionUpgradeQuote(ctx context.Context, client *dbent.Client, userID, sourceSubscriptionID, targetPlanID int64, lock bool) (*SubscriptionUpgradeQuote, *dbent.SubscriptionPlan, error) {
	query := client.UserSubscription.Query().Where(
		usersubscription.IDEQ(sourceSubscriptionID),
		usersubscription.UserIDEQ(userID),
		usersubscription.StatusEQ(SubscriptionStatusActive),
		usersubscription.ExpiresAtGT(time.Now()),
	)
	if lock {
		query = query.ForUpdate()
	}
	sub, err := query.Only(ctx)
	if err != nil {
		return nil, nil, infraerrors.BadRequest("UPGRADE_SOURCE_UNAVAILABLE", "source subscription is not active or is already locked for upgrade")
	}
	sourcePurchase, err := s.latestSubscriptionPurchaseForGroup(ctx, client, userID, sub.GroupID)
	if err != nil {
		return nil, nil, err
	}
	sourcePlan := sourcePurchase.plan
	targetPlan, err := client.SubscriptionPlan.Get(ctx, targetPlanID)
	if err != nil || !targetPlan.ForSale {
		return nil, nil, infraerrors.NotFound("PLAN_NOT_AVAILABLE", "target plan not found or not for sale")
	}
	sourceGroup, err := s.groupRepo.GetByID(ctx, sub.GroupID)
	if err != nil {
		return nil, nil, infraerrors.BadRequest("UPGRADE_SOURCE_GROUP_NOT_FOUND", "source subscription group no longer exists")
	}
	sourceGroup = sourceGroupWithPurchasedQuota(sourceGroup, sourcePurchase.quotaOverride)
	targetGroup, err := s.groupRepo.GetByID(ctx, targetPlan.GroupID)
	if err != nil {
		return nil, nil, infraerrors.BadRequest("UPGRADE_TARGET_GROUP_NOT_FOUND", "target subscription group no longer exists")
	}
	existingTarget, err := client.UserSubscription.Query().Where(
		usersubscription.UserIDEQ(userID),
		usersubscription.GroupIDEQ(targetPlan.GroupID),
	).Only(ctx)
	if dbent.IsNotFound(err) {
		existingTarget = nil
		err = nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("check target subscription: %w", err)
	}
	if targetSubscriptionBlocksUpgrade(existingTarget, time.Now()) {
		return nil, nil, infraerrors.Conflict("UPGRADE_TARGET_ALREADY_OWNED", "target subscription already exists")
	}
	quote, err := buildSubscriptionUpgradeQuote(time.Now(), sub, sourcePlan, targetPlan, sourceGroup, targetGroup)
	return quote, targetPlan, err
}

func (s *PaymentService) GetSubscriptionUpgradeOptions(ctx context.Context, userID, sourceSubscriptionID int64) ([]*SubscriptionUpgradeQuote, error) {
	sub, err := s.entClient.UserSubscription.Query().Where(
		usersubscription.IDEQ(sourceSubscriptionID),
		usersubscription.UserIDEQ(userID),
		usersubscription.StatusEQ(SubscriptionStatusActive),
		usersubscription.ExpiresAtGT(time.Now()),
	).Only(ctx)
	if err != nil {
		return nil, infraerrors.NotFound("UPGRADE_SOURCE_UNAVAILABLE", "active source subscription not found")
	}
	sourcePurchase, err := s.latestSubscriptionPurchaseForGroup(ctx, s.entClient, userID, sub.GroupID)
	if err != nil {
		return nil, err
	}
	sourcePlan := sourcePurchase.plan
	sourceGroup, err := s.groupRepo.GetByID(ctx, sub.GroupID)
	if err != nil {
		return nil, err
	}
	sourceGroup = sourceGroupWithPurchasedQuota(sourceGroup, sourcePurchase.quotaOverride)
	plans, err := s.configService.ListPlansForSale(ctx)
	if err != nil {
		return nil, err
	}
	quotes := make([]*SubscriptionUpgradeQuote, 0)
	for _, targetPlan := range plans {
		if targetPlan.GroupID == sub.GroupID || targetPlan.Price <= sourcePlan.Price {
			continue
		}
		existingTarget, checkErr := s.entClient.UserSubscription.Query().Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.GroupIDEQ(targetPlan.GroupID),
		).Only(ctx)
		if dbent.IsNotFound(checkErr) {
			existingTarget = nil
			checkErr = nil
		}
		if checkErr != nil || targetSubscriptionBlocksUpgrade(existingTarget, time.Now()) {
			continue
		}
		targetGroup, groupErr := s.groupRepo.GetByID(ctx, targetPlan.GroupID)
		if groupErr != nil {
			continue
		}
		quote, quoteErr := buildSubscriptionUpgradeQuote(time.Now(), sub, sourcePlan, targetPlan, sourceGroup, targetGroup)
		if quoteErr == nil {
			quotes = append(quotes, quote)
		}
	}
	return quotes, nil
}

func upgradeSnapshotMap(quote *SubscriptionUpgradeQuote) map[string]any {
	if quote == nil {
		return nil
	}
	raw, _ := json.Marshal(quote)
	result := map[string]any{}
	_ = json.Unmarshal(raw, &result)
	return result
}

func (s *PaymentService) lockUpgradeSourceInTx(ctx context.Context, tx *dbent.Tx, req CreateOrderRequest, targetPlan *dbent.SubscriptionPlan, expected *SubscriptionUpgradeQuote) (*SubscriptionUpgradeQuote, error) {
	if tx == nil || targetPlan == nil || expected == nil {
		return nil, infraerrors.BadRequest("INVALID_UPGRADE", "upgrade order is missing required context")
	}
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()
	quote, _, err := s.loadSubscriptionUpgradeQuote(txCtx, client, req.UserID, req.SourceSubscriptionID, int64(targetPlan.ID), true)
	if err != nil {
		return nil, err
	}
	if math.Abs(quote.UpgradeAmount-expected.UpgradeAmount) > 0.001 || math.Abs(quote.SourceUsage-expected.SourceUsage) > 0.000001 {
		return nil, infraerrors.Conflict("UPGRADE_QUOTE_CHANGED", "subscription usage changed; refresh the upgrade quote and try again")
	}
	pending, err := client.PaymentOrder.Query().Where(
		paymentorder.UpgradeSourceSubscriptionIDEQ(req.SourceSubscriptionID),
		paymentorder.StatusEQ(OrderStatusPending),
	).Exist(txCtx)
	if err != nil {
		return nil, fmt.Errorf("check pending upgrade order: %w", err)
	}
	if pending {
		return nil, infraerrors.Conflict("UPGRADE_ALREADY_PENDING", "an upgrade payment is already pending for this subscription")
	}
	if _, err := client.UserSubscription.UpdateOneID(req.SourceSubscriptionID).
		SetStatus(SubscriptionStatusSuspended).
		Save(txCtx); err != nil {
		return nil, fmt.Errorf("lock source subscription: %w", err)
	}
	return quote, nil
}

func (s *PaymentService) releaseUpgradeSource(ctx context.Context, order *dbent.PaymentOrder, reason string) error {
	if order == nil || order.OrderType != payment.OrderTypeSubscriptionUpgrade || order.UpgradeSourceSubscriptionID == nil {
		return nil
	}
	sub, err := s.entClient.UserSubscription.Query().Where(
		usersubscription.IDEQ(*order.UpgradeSourceSubscriptionID),
		usersubscription.StatusEQ(SubscriptionStatusSuspended),
	).Only(ctx)
	if dbent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	status := SubscriptionStatusActive
	if !sub.ExpiresAt.After(time.Now()) {
		status = SubscriptionStatusExpired
	}
	if _, err := s.entClient.UserSubscription.UpdateOneID(sub.ID).SetStatus(status).Save(ctx); err != nil {
		return err
	}
	if s.subscriptionSvc != nil {
		if err := s.subscriptionSvc.invalidateSubscriptionCaches(sub.UserID, sub.GroupID); err != nil {
			return err
		}
	}
	s.writeAuditLog(ctx, order.ID, "SUBSCRIPTION_UPGRADE_UNLOCKED", "system", map[string]any{"reason": reason})
	return nil
}

func hasUpgradeAppliedAudit(ctx context.Context, client *dbent.Client, orderID int64) (bool, error) {
	return client.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(fmt.Sprintf("%d", orderID)),
		paymentauditlog.ActionEQ(upgradeAuditAction),
	).Exist(ctx)
}

func (s *PaymentService) ExecuteSubscriptionUpgradeFulfillment(ctx context.Context, oid int64) error {
	order, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if order.Status == OrderStatusCompleted {
		return nil
	}
	if order.OrderType != payment.OrderTypeSubscriptionUpgrade || order.UpgradeSourceSubscriptionID == nil || order.SubscriptionGroupID == nil || order.SubscriptionDays == nil {
		return infraerrors.BadRequest("INVALID_STATUS", "missing subscription upgrade information")
	}
	if order.Status != OrderStatusPaid && order.Status != OrderStatusFailed && order.Status != OrderStatusRecharging {
		return infraerrors.BadRequest("INVALID_STATUS", "order cannot fulfill in status "+order.Status)
	}
	lease, err := s.acquirePaymentFulfillmentLease(ctx, order)
	if err != nil || lease == nil {
		return err
	}
	if err := s.doSubscriptionUpgrade(ctx, order, lease); err != nil {
		s.markFailed(ctx, oid, lease, err)
		return err
	}
	return nil
}

func (s *PaymentService) doSubscriptionUpgrade(ctx context.Context, order *dbent.PaymentOrder, lease *paymentFulfillmentLease) error {
	applied, err := hasUpgradeAppliedAudit(ctx, s.entClient, order.ID)
	if err != nil {
		return err
	}
	if !applied {
		if err := s.applySubscriptionUpgrade(ctx, order); err != nil {
			return err
		}
	}
	if err := s.applyAffiliateRebateForOrder(ctx, order); err != nil {
		return err
	}
	return s.markCompleted(ctx, order, lease, "SUBSCRIPTION_UPGRADE_SUCCESS")
}

func (s *PaymentService) applySubscriptionUpgrade(ctx context.Context, order *dbent.PaymentOrder) error {
	return s.applySubscriptionUpgradeWithRowLocks(ctx, order, true)
}

// applySubscriptionUpgradeWithRowLocks keeps production fulfillment protected
// by PostgreSQL row locks while allowing the SQLite unit harness (which does
// not support SELECT FOR UPDATE) to exercise the complete transaction.
func (s *PaymentService) applySubscriptionUpgradeWithRowLocks(ctx context.Context, order *dbent.PaymentOrder, lockRows bool) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin subscription upgrade tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	applied, err := hasUpgradeAppliedAudit(txCtx, client, order.ID)
	if err != nil {
		return err
	}
	if applied {
		return tx.Commit()
	}

	sourceID := *order.UpgradeSourceSubscriptionID
	sourceQuery := client.UserSubscription.Query().Where(
		usersubscription.IDEQ(sourceID),
		usersubscription.UserIDEQ(order.UserID),
	)
	if lockRows {
		sourceQuery = sourceQuery.ForUpdate()
	}
	source, err := sourceQuery.Only(txCtx)
	if err != nil {
		return fmt.Errorf("load locked source subscription: %w", err)
	}
	if source.Status != SubscriptionStatusSuspended && source.Status != SubscriptionStatusActive && source.Status != SubscriptionStatusExpired {
		return infraerrors.Conflict("UPGRADE_SOURCE_NOT_LOCKED", "source subscription is no longer locked for this paid upgrade")
	}
	targetGroupID := *order.SubscriptionGroupID
	targetQuery := client.UserSubscription.Query().Where(
		usersubscription.UserIDEQ(order.UserID),
		usersubscription.GroupIDEQ(targetGroupID),
	)
	if lockRows {
		targetQuery = targetQuery.ForUpdate()
	}
	target, err := targetQuery.Only(txCtx)
	if dbent.IsNotFound(err) {
		target = nil
		err = nil
	}
	if err != nil {
		return err
	}
	if targetSubscriptionBlocksUpgrade(target, time.Now()) {
		return infraerrors.Conflict("UPGRADE_TARGET_ALREADY_OWNED", "target subscription already exists")
	}

	now := time.Now()
	expiresAt := now.AddDate(0, 0, *order.SubscriptionDays)
	notes := fmt.Sprintf("subscription upgrade payment_order:%d source_subscription:%d", order.ID, source.ID)
	if target == nil {
		if _, err := client.UserSubscription.Create().
			SetUserID(order.UserID).
			SetGroupID(targetGroupID).
			SetStartsAt(now).
			SetExpiresAt(expiresAt).
			SetStatus(SubscriptionStatusActive).
			SetDailyUsageUsd(0).
			SetWeeklyUsageUsd(0).
			SetMonthlyUsageUsd(0).
			SetAssignedAt(now).
			SetNotes(notes).
			Save(txCtx); err != nil {
			return fmt.Errorf("create target subscription: %w", err)
		}
	} else {
		if _, err := client.UserSubscription.UpdateOneID(target.ID).
			SetStartsAt(now).
			SetExpiresAt(expiresAt).
			SetStatus(SubscriptionStatusActive).
			ClearDailyWindowStart().
			ClearWeeklyWindowStart().
			ClearMonthlyWindowStart().
			SetDailyUsageUsd(0).
			SetWeeklyUsageUsd(0).
			SetMonthlyUsageUsd(0).
			SetAssignedAt(now).
			SetNotes(notes).
			Save(txCtx); err != nil {
			return fmt.Errorf("reset expired target subscription: %w", err)
		}
	}
	if err := client.UserSubscription.DeleteOneID(source.ID).Exec(txCtx); err != nil {
		return fmt.Errorf("revoke source subscription: %w", err)
	}
	movedKeys, err := client.APIKey.Update().Where(
		apikey.UserIDEQ(order.UserID),
		apikey.GroupIDEQ(source.GroupID),
	).SetGroupID(targetGroupID).Save(txCtx)
	if err != nil {
		return fmt.Errorf("move API keys to target group: %w", err)
	}
	detail, _ := json.Marshal(map[string]any{
		"source_subscription_id": source.ID,
		"source_group_id":        source.GroupID,
		"target_group_id":        targetGroupID,
		"target_expires_at":      expiresAt,
		"moved_api_keys":         movedKeys,
		"upgrade_snapshot":       order.UpgradeSnapshot,
	})
	if _, err := client.PaymentAuditLog.Create().
		SetOrderID(fmt.Sprintf("%d", order.ID)).
		SetAction(upgradeAuditAction).
		SetDetail(string(detail)).
		SetOperator("system").
		Save(txCtx); err != nil {
		return fmt.Errorf("record subscription upgrade audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit subscription upgrade: %w", err)
	}
	if s.subscriptionSvc != nil {
		if err := errors.Join(
			s.subscriptionSvc.invalidateSubscriptionCaches(order.UserID, source.GroupID),
			s.subscriptionSvc.invalidateSubscriptionCaches(order.UserID, targetGroupID),
		); err != nil {
			return fmt.Errorf("invalidate subscription caches: %w", err)
		}
	}
	return nil
}
