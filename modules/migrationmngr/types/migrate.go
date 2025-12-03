package types

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

var _ sdk.HasValidateBasic = &MsgMigrateToEvolve{}

func (m *MsgMigrateToEvolve) ValidateBasic() error {
	// Must be a single-validator migration path for this scenario
	if !m.StayOnComet {
		return fmt.Errorf("migration module only supports stay_on_comet=true")
	}

	// Governance authority must be provided (gov module sets this when routed via x/gov)
	if len(m.Authority) == 0 {
		return fmt.Errorf("authority must not be empty")
	}

	// Block height must be positive
	if m.BlockHeight == 0 {
		return fmt.Errorf("block_height must be greater than 0")
	}

	// Attesters are not supported in this scenario
	if len(m.Attesters) != 0 {
		return fmt.Errorf("attesters must be empty for single-validator migration")
	}

	// Sequencer field holds the single validator identity; require a consensus pubkey
	if m.Sequencer.ConsensusPubkey == nil || m.Sequencer.ConsensusPubkey.TypeUrl == "" || len(m.Sequencer.ConsensusPubkey.Value) == 0 {
		return fmt.Errorf("sequencer.consensus_pubkey must be set")
	}

	return nil
}
