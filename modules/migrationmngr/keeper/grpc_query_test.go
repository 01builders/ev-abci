package keeper_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/address"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	migrationmngr "github.com/evstack/ev-abci/modules/migrationmngr"
	"github.com/evstack/ev-abci/modules/migrationmngr/keeper"
	"github.com/evstack/ev-abci/modules/migrationmngr/types"
)

// mockStakingKeeper is a minimal mock for stakingKeeper used in tests.
type mockStakingKeeper struct {
	vals        []stakingtypes.Validator
	err         error
	delegations map[string][]stakingtypes.Delegation // validator address -> delegations
	undelegated []undelegateRecord                   // track unbonding operations
}

type undelegateRecord struct {
	delegator sdk.AccAddress
	validator sdk.ValAddress
	shares    math.LegacyDec
}

func (m *mockStakingKeeper) GetValidatorDelegations(ctx context.Context, valAddr sdk.ValAddress) ([]stakingtypes.Delegation, error) {
	if m.delegations == nil {
		return []stakingtypes.Delegation{}, nil
	}
	delegs, ok := m.delegations[valAddr.String()]
	if !ok {
		return []stakingtypes.Delegation{}, nil
	}
	return delegs, nil
}

func (m *mockStakingKeeper) Undelegate(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress, sharesAmount math.LegacyDec) (time.Time, math.Int, error) {
	// record the unbonding operation
	if m.undelegated == nil {
		m.undelegated = []undelegateRecord{}
	}
	m.undelegated = append(m.undelegated, undelegateRecord{
		delegator: delAddr,
		validator: valAddr,
		shares:    sharesAmount,
	})

	// return unbonding completion time (21 days from now) and amount
	unbondingTime := time.Now().Add(21 * 24 * time.Hour)
	amount := math.NewInt(sharesAmount.TruncateInt64())
	return unbondingTime, amount, nil
}

func (m *mockStakingKeeper) GetValidatorByConsAddr(ctx context.Context, consAddr sdk.ConsAddress) (stakingtypes.Validator, error) {
	for _, val := range m.vals {
		if valBz, err := val.GetConsAddr(); err == nil && bytes.Equal(valBz, consAddr) {
			return val, nil
		}
	}
	return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
}

func (m *mockStakingKeeper) GetLastValidators(context.Context) ([]stakingtypes.Validator, error) {
	return m.vals, m.err
}

func (m *mockStakingKeeper) IterateBondedValidatorsByPower(ctx context.Context, cb func(index int64, validator stakingtypes.ValidatorI) (stop bool)) error {
	for i, val := range m.vals {
		if cb(int64(i), val) {
			break
		}
	}
	return nil
}

type fixture struct {
	ctx        sdk.Context
	kvStoreKey *storetypes.KVStoreKey

	stakingKeeper *mockStakingKeeper
	keeper        keeper.Keeper
	queryServer   types.QueryServer
	msgServer     types.MsgServer
}

func initFixture(tb testing.TB) *fixture {
	tb.Helper()

	stakingKeeper := &mockStakingKeeper{}
	key := storetypes.NewKVStoreKey(types.ModuleName)
	storeService := runtime.NewKVStoreService(key)
	encCfg := moduletestutil.MakeTestEncodingConfig(migrationmngr.AppModuleBasic{})
	addressCodec := addresscodec.NewBech32Codec("cosmos")
	ctx := testutil.DefaultContext(key, storetypes.NewTransientStoreKey("transient"))

	k := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		addressCodec,
		stakingKeeper,
		nil,
		sdk.AccAddress(address.Module(types.ModuleName)).String(),
	)

	return &fixture{
		ctx:           ctx,
		kvStoreKey:    key,
		stakingKeeper: stakingKeeper,
		keeper:        k,
		queryServer:   keeper.NewQueryServer(k),
		msgServer:     keeper.NewMsgServerImpl(k),
	}
}

func TestIsMigrating(t *testing.T) {
	s := initFixture(t)

	// set up migration
	require.NoError(t, s.keeper.Migration.Set(s.ctx, types.Migration{
		BlockHeight: 1,
		Sequencer:   types.Sequencer{Name: "foo"},
	}))

	s.ctx = s.ctx.WithBlockHeight(1)
	resp, err := s.queryServer.IsMigrating(s.ctx, &types.QueryIsMigratingRequest{})
	require.NoError(t, err)
	require.True(t, resp.IsMigrating)
	require.Equal(t, uint64(1), resp.StartBlockHeight)
	require.Equal(t, uint64(2), resp.EndBlockHeight)
}

func TestIsMigrating_IBCEnabled(t *testing.T) {
	stakingKeeper := &mockStakingKeeper{}
	key := storetypes.NewKVStoreKey(types.ModuleName)
	storeService := runtime.NewKVStoreService(key)
	encCfg := moduletestutil.MakeTestEncodingConfig(migrationmngr.AppModuleBasic{})
	addressCodec := addresscodec.NewBech32Codec("cosmos")
	ibcKey := storetypes.NewKVStoreKey("ibc")
	ctx := testutil.DefaultContextWithKeys(map[string]*storetypes.KVStoreKey{
		types.ModuleName: key,
		"ibc":            ibcKey,
	}, nil, nil)

	k := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		addressCodec,
		stakingKeeper,
		func() *storetypes.KVStoreKey { return key },
		sdk.AccAddress(address.Module(types.ModuleName)).String(),
	)

	// set up migration
	require.NoError(t, k.Migration.Set(ctx, types.Migration{
		BlockHeight: 1,
		Sequencer:   types.Sequencer{Name: "foo"},
	}))

	ctx = ctx.WithBlockHeight(1)
	resp, err := keeper.NewQueryServer(k).IsMigrating(ctx, &types.QueryIsMigratingRequest{})
	require.NoError(t, err)
	require.True(t, resp.IsMigrating)
	require.Equal(t, uint64(1), resp.StartBlockHeight)
	require.Equal(t, 1+keeper.IBCSmoothingFactor, resp.EndBlockHeight)
}
