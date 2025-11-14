package keeper

import (
	"context"

	"cosmossdk.io/core/appmodule"
	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// PreBlock cleans up migration state after migration is complete.
func (k Keeper) PreBlock(ctx context.Context) (appmodule.ResponsePreBlock, error) {
	start, end, _ := k.IsMigrating(ctx)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	currentHeight := uint64(sdkCtx.BlockHeight())
	shouldCleanup := end > 0 && currentHeight == end+1

	k.Logger(ctx).Debug("PreBlock migration check",
		"height", currentHeight,
		"start", start,
		"end", end,
		"shouldCleanup", shouldCleanup)

	// one block after the migration end, we clean up the migration state
	if shouldCleanup {
		k.Logger(ctx).Info("Migration complete, cleaning up migration state")

		// remove the migration state from the store
		if err := k.Migration.Remove(ctx); err != nil {
			k.Logger(ctx).Error("failed to remove migration state", "error", err)
			return nil, sdkerrors.ErrLogic.Wrapf("failed to remove migration state: %v", err)
		}
	}

	return &sdk.ResponsePreBlock{}, nil
}

// EndBlock is called at the end of every block and returns validator updates.
func (k Keeper) EndBlock(ctx context.Context) ([]abci.ValidatorUpdate, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	start, end, shouldBeMigrating := k.IsMigrating(ctx)
	k.Logger(ctx).Debug("EndBlock migration check", "height", sdkCtx.BlockHeight(), "start", start, "end", end, "shouldBeMigrating", shouldBeMigrating)

	if !shouldBeMigrating || start > uint64(sdkCtx.BlockHeight()) {
		// no migration in progress, return empty updates
		return []abci.ValidatorUpdate{}, nil
	}

	migration, err := k.Migration.Get(ctx)
	if err != nil {
		return nil, sdkerrors.ErrLogic.Wrapf("failed to get migration state: %v", err)
	}

	validatorSet, err := k.stakingKeeper.GetLastValidators(sdkCtx)
	if err != nil {
		return nil, err
	}

	var updates []abci.ValidatorUpdate
	if !k.isIBCEnabled(ctx) {
		// if IBC is not enabled, we can migrate immediately
		// but only return updates on the first block of migration (start height)
		if uint64(sdkCtx.BlockHeight()) == start {
			updates, err = k.migrateNow(ctx, migration, validatorSet)
			if err != nil {
				return nil, err
			}
		}
	} else {
		updates, err = k.migrateOver(sdkCtx, migration, validatorSet)
		if err != nil {
			return nil, err
		}
	}

	k.Logger(ctx).Debug("EndBlock migration updates", "height", sdkCtx.BlockHeight(), "updates", len(updates))
	return updates, nil
}
