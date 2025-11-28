package keeper

import (
	"context"

	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/evstack/ev-abci/modules/migrationmngr/types"
)

// IBCSmoothingFactor is the factor used to smooth the migration process when IBC is enabled. It determines how many blocks the migration will take.
var IBCSmoothingFactor uint64 = 30

// migrateNow migrates the chain to evolve immediately.
// this method is used when ibc is not enabled, so no migration smoothing is needed.
// If StayOnComet is true, delegations are unbonded and empty updates returned.
// Otherwise, ABCI ValidatorUpdates are returned directly for rollup migration.
func (k Keeper) migrateNow(
	ctx context.Context,
	migrationData types.EvolveMigration,
	lastValidatorSet []stakingtypes.Validator,
) (initialValUpdates []abci.ValidatorUpdate, err error) {
	// ensure sequencer pubkey Any is unpacked and cached for TmConsPublicKey() to work correctly
	if err := migrationData.Sequencer.UnpackInterfaces(k.cdc); err != nil {
		return nil, sdkerrors.ErrInvalidRequest.Wrapf("failed to unpack sequencer pubkey: %v", err)
	}

	if migrationData.StayOnComet {
		// StayOnComet (IBC disabled): fully undelegate all validators' tokens and
		// explicitly set the final CometBFT validator set to a single validator with power=1.
		k.Logger(ctx).Info("StayOnComet: immediate undelegation and explicit valset update")

		// unbond all validator delegations
		for _, val := range lastValidatorSet {
			if err := k.unbondValidatorDelegations(ctx, val); err != nil {
				return nil, err
			}
		}

		validatorsToRemove := getValidatorsToRemove(migrationData, lastValidatorSet)

		// Build ABCI updates: zeros for all non-sequencers. sequencer power 1
		var updates []abci.ValidatorUpdate
		for _, val := range validatorsToRemove {
			updates = append(updates, val.ABCIValidatorUpdateZero())
		}

		pk, err := migrationData.Sequencer.TmConsPublicKey()
		if err != nil {
			return nil, sdkerrors.ErrInvalidRequest.Wrapf("failed to get sequencer pubkey: %v", err)
		}
		updates = append(updates, abci.ValidatorUpdate{PubKey: pk, Power: 1})

		return updates, nil
	}

	// rollup migration: build and return ABCI updates directly
	switch len(migrationData.Attesters) {
	case 0:
		// no attesters, we are migrating to a single sequencer
		initialValUpdates, err = migrateToSequencer(&migrationData, lastValidatorSet)
		if err != nil {
			return nil, sdkerrors.ErrInvalidRequest.Wrapf("failed to migrate to sequencer: %v", err)
		}
	default:
		// we are migrating the validator set to attesters
		initialValUpdates, err = migrateToAttesters(&migrationData, lastValidatorSet)
		if err != nil {
			return nil, sdkerrors.ErrInvalidRequest.Wrapf("failed to migrate to sequencer & attesters: %v", err)
		}
	}

	// set new sequencer in the store
	// it will be used by the evolve migration command when using attesters
	seq := migrationData.Sequencer
	if err := k.Sequencer.Set(ctx, seq); err != nil {
		return nil, sdkerrors.ErrInvalidRequest.Wrapf("failed to set sequencer: %v", err)
	}

	return initialValUpdates, nil
}

// migrateToSequencer migrates the chain to a single sequencer.
// the validator set is updated to include the sequencer and remove all other validators.
func migrateToSequencer(
	migrationData *types.EvolveMigration,
	lastValidatorSet []stakingtypes.Validator,
) (initialValUpdates []abci.ValidatorUpdate, err error) {
	seq := &migrationData.Sequencer

	pk, err := seq.TmConsPublicKey()
	if err != nil {
		return nil, err
	}
	sequencerUpdate := abci.ValidatorUpdate{
		PubKey: pk,
		Power:  1,
	}

	for _, val := range lastValidatorSet {
		powerUpdate := val.ABCIValidatorUpdateZero()
		if val.ConsensusPubkey.Equal(seq.ConsensusPubkey) {
			continue
		}
		initialValUpdates = append(initialValUpdates, powerUpdate)
	}

	return append(initialValUpdates, sequencerUpdate), nil
}

// migrateToAttesters migrates the chain to attesters.
// the validator set is updated to include the attesters and remove all other validators.
func migrateToAttesters(
	migrationData *types.EvolveMigration,
	lastValidatorSet []stakingtypes.Validator,
) (initialValUpdates []abci.ValidatorUpdate, err error) {
	// First, remove all existing validators that are not attesters
	attesterPubKeys := make(map[string]bool)
	for _, attester := range migrationData.Attesters {
		key := attester.ConsensusPubkey.String()
		attesterPubKeys[key] = true
	}

	// Remove validators that are not attesters
	for _, val := range lastValidatorSet {
		if !attesterPubKeys[val.ConsensusPubkey.String()] {
			powerUpdate := val.ABCIValidatorUpdateZero()
			initialValUpdates = append(initialValUpdates, powerUpdate)
		}
	}

	// Add attesters with power 1
	for _, attester := range migrationData.Attesters {
		pk, err := attester.TmConsPublicKey()
		if err != nil {
			return nil, err
		}
		attesterUpdate := abci.ValidatorUpdate{
			PubKey: pk,
			Power:  1,
		}
		initialValUpdates = append(initialValUpdates, attesterUpdate)
	}

	return initialValUpdates, nil
}

// unbondValidatorDelegations unbonds all delegations to a specific validator.
// This is used when StayOnComet is true to properly return tokens to delegators.
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
// For sequencer-only migration: all validators except the sequencer.
// For attester migration: all validators not in the attester set.
func getValidatorsToRemove(migrationData types.EvolveMigration, lastValidatorSet []stakingtypes.Validator) []stakingtypes.Validator {
	if len(migrationData.Attesters) == 0 {
		// sequencer-only: remove all except sequencer
		var validatorsToRemove []stakingtypes.Validator
		for _, val := range lastValidatorSet {
			if !val.ConsensusPubkey.Equal(migrationData.Sequencer.ConsensusPubkey) {
				validatorsToRemove = append(validatorsToRemove, val)
			}
		}
		return validatorsToRemove
	}

	// attester migration: remove all not in attester set
	attesterPubKeys := make(map[string]bool)
	for _, attester := range migrationData.Attesters {
		attesterPubKeys[attester.ConsensusPubkey.String()] = true
	}

	var validatorsToRemove []stakingtypes.Validator
	for _, val := range lastValidatorSet {
		if !attesterPubKeys[val.ConsensusPubkey.String()] {
			validatorsToRemove = append(validatorsToRemove, val)
		}
	}
	return validatorsToRemove
}
