package service

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

type subscriptionUpgradeGroupRepoStub struct {
	groupRepoNoop
	groups map[int64]*Group
}

func (r *subscriptionUpgradeGroupRepoStub) GetByID(_ context.Context, id int64) (*Group, error) {
	group, ok := r.groups[id]
	if !ok {
		return nil, ErrGroupNotFound
	}
	return group, nil
}

func createSubscriptionUpgradeTestUser(t *testing.T, client *dbent.Client) *dbent.User {
	t.Helper()
	return client.User.Create().
		SetEmail(fmt.Sprintf("upgrade-%s@example.com", strconv.FormatInt(time.Now().UnixNano(), 10))).
		SetPasswordHash("test-password-hash").
		SaveX(context.Background())
}

func createSubscriptionUpgradeTestOrder(t *testing.T, client *dbent.Client, user *dbent.User, configure func(*dbent.PaymentOrderCreate)) *dbent.PaymentOrder {
	t.Helper()
	order := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(350).
		SetPayAmount(350).
		SetRechargeCode("UPGRADE-TEST").
		SetOutTradeNo(fmt.Sprintf("upgrade-test-%d", time.Now().UnixNano())).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("test.local")
	if configure != nil {
		configure(order)
	}
	return order.SaveX(context.Background())
}

func TestBuildSubscriptionUpgradeQuoteUsesUnusedMonthlyQuotaAsCredit(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-12 * time.Hour)
	sourceLimit := 1600.0
	targetLimit := 3200.0

	quote, err := buildSubscriptionUpgradeQuote(
		now,
		&dbent.UserSubscription{
			ID:                 88,
			UserID:             9,
			GroupID:            50,
			Status:             SubscriptionStatusActive,
			MonthlyWindowStart: &windowStart,
			MonthlyUsageUsd:    800,
		},
		&dbent.SubscriptionPlan{ID: 35, GroupID: 50, Name: "gpt 4x", Price: 350, ValidityDays: 30, ValidityUnit: "days"},
		&dbent.SubscriptionPlan{ID: 40, GroupID: 60, Name: "gpt 8x", Price: 700, ValidityDays: 30, ValidityUnit: "days"},
		&Group{ID: 50, Name: "GPT 4x", Platform: "openai", Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription, MonthlyLimitUSD: &sourceLimit},
		&Group{ID: 60, Name: "GPT 8x", Platform: "openai", Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription, MonthlyLimitUSD: &targetLimit},
	)

	require.NoError(t, err)
	require.Equal(t, 800.0, quote.RemainingQuota)
	require.Equal(t, 0.5, quote.RemainingRatio)
	require.Equal(t, 175.0, quote.CreditAmount)
	require.Equal(t, 525.0, quote.UpgradeAmount)
	require.Equal(t, 3200.0, quote.TargetQuota)
	require.Equal(t, now.Add(30*24*time.Hour), quote.NewExpiresAt)
}

func TestBuildSubscriptionUpgradeQuoteResetsExpiredUsageWindowForCredit(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-31 * 24 * time.Hour)
	sourceLimit := 1600.0
	targetLimit := 3200.0

	quote, err := buildSubscriptionUpgradeQuote(
		now,
		&dbent.UserSubscription{ID: 88, UserID: 9, GroupID: 50, MonthlyWindowStart: &windowStart, MonthlyUsageUsd: 800},
		&dbent.SubscriptionPlan{ID: 35, GroupID: 50, Name: "gpt 4x", Price: 350, ValidityDays: 30, ValidityUnit: "days"},
		&dbent.SubscriptionPlan{ID: 40, GroupID: 60, Name: "gpt 8x", Price: 700, ValidityDays: 30, ValidityUnit: "days"},
		&Group{ID: 50, Name: "GPT 4x", Platform: "openai", Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription, MonthlyLimitUSD: &sourceLimit},
		&Group{ID: 60, Name: "GPT 8x", Platform: "openai", Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription, MonthlyLimitUSD: &targetLimit},
	)

	require.NoError(t, err)
	require.Zero(t, quote.SourceUsage)
	require.Equal(t, 350.0, quote.CreditAmount)
	require.Equal(t, 350.0, quote.UpgradeAmount)
}

func TestBuildSubscriptionUpgradeQuoteRejectsLowerQuotaTarget(t *testing.T) {
	now := time.Now()
	windowStart := now
	sourceLimit := 1600.0
	targetLimit := 1200.0

	_, err := buildSubscriptionUpgradeQuote(
		now,
		&dbent.UserSubscription{ID: 88, UserID: 9, GroupID: 50, MonthlyWindowStart: &windowStart},
		&dbent.SubscriptionPlan{ID: 35, GroupID: 50, Name: "source", Price: 350, ValidityDays: 30, ValidityUnit: "days"},
		&dbent.SubscriptionPlan{ID: 36, GroupID: 54, Name: "target", Price: 525, ValidityDays: 30, ValidityUnit: "days"},
		&Group{ID: 50, Platform: "openai", Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription, MonthlyLimitUSD: &sourceLimit},
		&Group{ID: 54, Platform: "openai", Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription, MonthlyLimitUSD: &targetLimit},
	)

	require.Error(t, err)
}

func TestLoadSubscriptionUpgradeQuoteUsesGroupMonthlyPlanForManualAssignment(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user := createSubscriptionUpgradeTestUser(t, client)
	sourceGroup := client.Group.Create().
		SetName("source-plan-group").
		SetPlatform(PlatformOpenAI).
		SetSubscriptionType(SubscriptionTypeSubscription).
		SetMonthlyLimitUsd(1600).
		SaveX(ctx)
	targetGroup := client.Group.Create().
		SetName("target-plan-group").
		SetPlatform(PlatformOpenAI).
		SetSubscriptionType(SubscriptionTypeSubscription).
		SetMonthlyLimitUsd(3200).
		SaveX(ctx)
	client.SubscriptionPlan.Create().
		SetGroupID(sourceGroup.ID).
		SetName("source-quarterly").
		SetPrice(1050).
		SetCurrency("CNY").
		SetValidityDays(90).
		SetValidityUnit("day").
		SetForSale(true).
		SaveX(ctx)
	sourceMonthlyPlan := client.SubscriptionPlan.Create().
		SetGroupID(sourceGroup.ID).
		SetName("source-monthly").
		SetPrice(350).
		SetCurrency("CNY").
		SetValidityDays(30).
		SetValidityUnit("day").
		SetForSale(true).
		SaveX(ctx)
	targetPlan := client.SubscriptionPlan.Create().
		SetGroupID(targetGroup.ID).
		SetName("target").
		SetPrice(700).
		SetCurrency("CNY").
		SetValidityDays(30).
		SetValidityUnit("day").
		SetForSale(true).
		SaveX(ctx)
	targetQuarterlyPlan := client.SubscriptionPlan.Create().
		SetGroupID(targetGroup.ID).
		SetName("target-quarterly").
		SetPrice(2100).
		SetCurrency("CNY").
		SetValidityDays(90).
		SetValidityUnit("day").
		SetForSale(true).
		SaveX(ctx)
	windowStart := time.Now().Add(-time.Hour)
	sourceSubscription := client.UserSubscription.Create().
		SetUserID(user.ID).
		SetGroupID(sourceGroup.ID).
		SetStartsAt(time.Now().Add(-24 * time.Hour)).
		SetExpiresAt(time.Now().Add(29 * 24 * time.Hour)).
		SetStatus(SubscriptionStatusActive).
		SetMonthlyWindowStart(windowStart).
		SetMonthlyUsageUsd(800).
		SaveX(ctx)
	svc := &PaymentService{
		entClient: client,
		groupRepo: &subscriptionUpgradeGroupRepoStub{groups: map[int64]*Group{
			sourceGroup.ID: {ID: sourceGroup.ID, Name: sourceGroup.Name, Platform: PlatformOpenAI, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription, MonthlyLimitUSD: sourceGroup.MonthlyLimitUsd},
			targetGroup.ID: {ID: targetGroup.ID, Name: targetGroup.Name, Platform: PlatformOpenAI, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription, MonthlyLimitUSD: targetGroup.MonthlyLimitUsd},
		}},
	}
	quote, _, err := svc.loadSubscriptionUpgradeQuote(ctx, client, user.ID, sourceSubscription.ID, targetPlan.ID, false)

	require.NoError(t, err)
	require.Equal(t, int64(sourceMonthlyPlan.ID), quote.SourcePlanID)
	require.Equal(t, 350.0, quote.SourcePrice, "gifted subscriptions use the group's monthly baseline plan")
	require.Equal(t, 175.0, quote.CreditAmount)
	require.Equal(t, 525.0, quote.UpgradeAmount)

	_, _, err = svc.loadSubscriptionUpgradeQuote(ctx, client, user.ID, sourceSubscription.ID, targetQuarterlyPlan.ID, false)
	require.Error(t, err, "upgrades must only quote the target group's monthly baseline plan")
}

func TestApplySubscriptionUpgradeResetsExpiredTargetAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user := createSubscriptionUpgradeTestUser(t, client)
	sourceGroup := client.Group.Create().SetName("source-fulfillment").SaveX(ctx)
	targetGroup := client.Group.Create().SetName("target-fulfillment").SaveX(ctx)
	windowStart := time.Now().Add(-48 * time.Hour)
	source := client.UserSubscription.Create().
		SetUserID(user.ID).
		SetGroupID(sourceGroup.ID).
		SetStartsAt(time.Now().Add(-10 * 24 * time.Hour)).
		SetExpiresAt(time.Now().Add(20 * 24 * time.Hour)).
		SetStatus(SubscriptionStatusSuspended).
		SetDailyWindowStart(windowStart).
		SetWeeklyWindowStart(windowStart).
		SetMonthlyWindowStart(windowStart).
		SetDailyUsageUsd(10).
		SetWeeklyUsageUsd(100).
		SetMonthlyUsageUsd(800).
		SaveX(ctx)
	expiredTarget := client.UserSubscription.Create().
		SetUserID(user.ID).
		SetGroupID(targetGroup.ID).
		SetStartsAt(time.Now().Add(-60 * 24 * time.Hour)).
		SetExpiresAt(time.Now().Add(-30 * 24 * time.Hour)).
		SetStatus(SubscriptionStatusExpired).
		SetDailyWindowStart(windowStart).
		SetWeeklyWindowStart(windowStart).
		SetMonthlyWindowStart(windowStart).
		SetDailyUsageUsd(1).
		SetWeeklyUsageUsd(2).
		SetMonthlyUsageUsd(3).
		SaveX(ctx)
	key := client.APIKey.Create().
		SetUserID(user.ID).
		SetGroupID(sourceGroup.ID).
		SetKey(fmt.Sprintf("sk-upgrade-%d", time.Now().UnixNano())).
		SetName("upgrade test key").
		SaveX(ctx)
	order := createSubscriptionUpgradeTestOrder(t, client, user, func(order *dbent.PaymentOrderCreate) {
		order.SetOrderType(payment.OrderTypeSubscriptionUpgrade).
			SetStatus(OrderStatusPaid).
			SetSubscriptionGroupID(targetGroup.ID).
			SetSubscriptionDays(30).
			SetUpgradeSourceSubscriptionID(source.ID).
			SetUpgradeSnapshot(map[string]any{"target_price": 700.0, "target_quota": 3200.0})
	})
	svc := &PaymentService{entClient: client}

	require.NoError(t, svc.applySubscriptionUpgradeWithRowLocks(ctx, order, false))
	require.NoError(t, svc.applySubscriptionUpgradeWithRowLocks(ctx, order, false), "replaying fulfillment must not duplicate or reset the target again")

	deletedSource, err := client.UserSubscription.Get(mixins.SkipSoftDelete(ctx), source.ID)
	require.NoError(t, err)
	require.NotNil(t, deletedSource.DeletedAt)
	target := client.UserSubscription.GetX(ctx, expiredTarget.ID)
	require.Equal(t, SubscriptionStatusActive, target.Status)
	require.Zero(t, target.DailyUsageUsd)
	require.Zero(t, target.WeeklyUsageUsd)
	require.Zero(t, target.MonthlyUsageUsd)
	require.Nil(t, target.DailyWindowStart)
	require.Nil(t, target.WeeklyWindowStart)
	require.Nil(t, target.MonthlyWindowStart)
	require.WithinDuration(t, time.Now().Add(30*24*time.Hour), target.ExpiresAt, 5*time.Second)
	require.NotNil(t, target.Notes)
	require.Contains(t, *target.Notes, fmt.Sprintf("payment_order:%d", order.ID))
	movedKey := client.APIKey.Query().Where(apikey.IDEQ(key.ID)).OnlyX(ctx)
	require.NotNil(t, movedKey.GroupID)
	require.Equal(t, targetGroup.ID, *movedKey.GroupID)
	require.Equal(t, 1, client.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)),
		paymentauditlog.ActionEQ(upgradeAuditAction),
	).CountX(ctx))
	require.Equal(t, 1, client.UserSubscription.Query().Where(
		usersubscription.UserIDEQ(user.ID),
		usersubscription.GroupIDEQ(targetGroup.ID),
	).CountX(ctx))
}

func TestReleaseUpgradeSourceRestoresSuspendedSubscription(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user := createSubscriptionUpgradeTestUser(t, client)
	group := client.Group.Create().SetName("source-release").SaveX(ctx)
	source := client.UserSubscription.Create().
		SetUserID(user.ID).
		SetGroupID(group.ID).
		SetStartsAt(time.Now().Add(-time.Hour)).
		SetExpiresAt(time.Now().Add(24 * time.Hour)).
		SetStatus(SubscriptionStatusSuspended).
		SaveX(ctx)
	order := createSubscriptionUpgradeTestOrder(t, client, user, func(order *dbent.PaymentOrderCreate) {
		order.SetOrderType(payment.OrderTypeSubscriptionUpgrade).
			SetStatus(OrderStatusPending).
			SetUpgradeSourceSubscriptionID(source.ID)
	})
	svc := &PaymentService{entClient: client}

	require.NoError(t, svc.releaseUpgradeSource(ctx, order, "user cancelled"))
	require.Equal(t, SubscriptionStatusActive, client.UserSubscription.GetX(ctx, source.ID).Status)
	require.Equal(t, 1, client.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)),
		paymentauditlog.ActionEQ("SUBSCRIPTION_UPGRADE_UNLOCKED"),
	).CountX(ctx))
}
