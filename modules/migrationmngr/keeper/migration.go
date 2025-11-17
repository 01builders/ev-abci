package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/evstack/ev-abci/modules/migrationmngr/types"
)

// IBCSmoothingFactor is the factor used to smooth the migration process when IBC is enabled. It determines how many blocks the migration will take.
var IBCSmoothingFactor uint64 = 30

// migrateNow unbonds validators immediately.
// this method is used when ibc is not enabled, so no migration smoothing is needed.
func (k Keeper) migrateNow(
	ctx context.Context,
	migrationData types.Migration,
	lastValidatorSet []stakingtypes.Validator,
) (initialValUpdates []abci.ValidatorUpdate, err error) {
	// unbond delegations, let staking module handle validator updates
	k.Logger(ctx).Info("Unbonding all validators immediately (IBC not enabled)")
	validatorsToRemove := getValidatorsToRemove(migrationData, lastValidatorSet)
	for _, val := range validatorsToRemove {
		if err := k.unbondValidatorDelegations(ctx, val); err != nil {
			return nil, err
		}
	}
	// return empty updates - staking module will update CometBFT
	return []abci.ValidatorUpdate{}, nil
}

// migrateOver unbonds validators gradually over a period of blocks.
// this is to ensure ibc light client verification keep working while changing the whole validator set.
// the migration step is tracked in store.
func (k Keeper) migrateOver(
	ctx context.Context,
	migrationData types.Migration,
	lastValidatorSet []stakingtypes.Validator,
) (initialValUpdates []abci.ValidatorUpdate, err error) {
	step, err := k.MigrationStep.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, sdkerrors.ErrInvalidRequest.Wrapf("failed to get migration step: %v", err)
	}

	if step >= IBCSmoothingFactor {
		// migration complete
		if err := k.MigrationStep.Remove(ctx); err != nil {
			return nil, sdkerrors.ErrInvalidRequest.Wrapf("failed to remove migration step: %v", err)
		}

		// unbonding was already completed gradually over previous blocks, just return empty updates
		k.Logger(ctx).Info("Migration complete, all validators unbonded gradually")
		return []abci.ValidatorUpdate{}, nil
	}

	// unbond delegations gradually, let staking module handle validator updates
	return k.migrateOverWithUnbonding(ctx, migrationData, lastValidatorSet, step)
}

// unbondValidatorDelegations unbonds all delegations to a specific validator.
// this properly returns tokens to delegators.
func (k Keeper) unbondValidatorDelegations(ctx context.Context, validator stakingtypes.Validator) error {
	valAddr, err := sdk.ValAddressFromBech32(validator.OperatorAddress)
	if err != nil {
		return sdkerrors.ErrInvalidAddress.Wrapf("invalid validator address: %v", err)
	}

	// get all delegations to this validator
	delegations, err := k.stakingKeeper.GetValidatorDelegations(ctx, valAddr)
	if err != nil {
		return sdkerrors.ErrLogic.Wrapf("failed to get validator delegations: %v", err)
	}

	// unbond each delegation
	for _, delegation := range delegations {
		delAddr, err := sdk.AccAddressFromBech32(delegation.DelegatorAddress)
		if err != nil {
			return sdkerrors.ErrInvalidAddress.Wrapf("invalid delegator address: %v", err)
		}

		// unbond all shares from this delegation
		_, _, err = k.stakingKeeper.Undelegate(ctx, delAddr, valAddr, delegation.Shares)
		if err != nil {
			return sdkerrors.ErrLogic.Wrapf("failed to undelegate: %v", err)
		}
	}

	return nil
}

// getValidatorsToRemove returns validators that should be removed during migration.
// all validators except the sequencer are removed.
func getValidatorsToRemove(migrationData types.Migration, lastValidatorSet []stakingtypes.Validator) []stakingtypes.Validator {
	var validatorsToRemove []stakingtypes.Validator
	for _, val := range lastValidatorSet {
		if !val.ConsensusPubkey.Equal(migrationData.Sequencer.ConsensusPubkey) {
			validatorsToRemove = append(validatorsToRemove, val)
		}
	}
	return validatorsToRemove
}

// migrateOverWithUnbonding unbonds validators gradually over the smoothing period.
// this is used when IBC is enabled.
func (k Keeper) migrateOverWithUnbonding(
	ctx context.Context,
	migrationData types.Migration,
	lastValidatorSet []stakingtypes.Validator,
	step uint64,
) ([]abci.ValidatorUpdate, error) {
	validatorsToRemove := getValidatorsToRemove(migrationData, lastValidatorSet)

	if len(validatorsToRemove) == 0 {
		k.Logger(ctx).Info("No validators to remove, migration complete")
		return []abci.ValidatorUpdate{}, nil
	}

	// unbond validators gradually
	removePerStep := (len(validatorsToRemove) + int(IBCSmoothingFactor) - 1) / int(IBCSmoothingFactor)
	startRemove := int(step) * removePerStep
	endRemove := min(startRemove+removePerStep, len(validatorsToRemove))

	k.Logger(ctx).Info("Unbonding validators gradually",
		"step", step,
		"start_index", startRemove,
		"end_index", endRemove,
		"total_to_remove", len(validatorsToRemove))

	for _, val := range validatorsToRemove[startRemove:endRemove] {
		if err := k.unbondValidatorDelegations(ctx, val); err != nil {
			return nil, err
		}
	}

	// increment step
	if err := k.MigrationStep.Set(ctx, step+1); err != nil {
		return nil, sdkerrors.ErrInvalidRequest.Wrapf("failed to set migration step: %v", err)
	}

	// return empty updates - let staking module handle validator set changes
	return []abci.ValidatorUpdate{}, nil
}
