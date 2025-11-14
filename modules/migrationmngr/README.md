# Migration Manager Module

## Introduction

The `migrationmngr` module is a specialized module for orchestrating a one-time, coordinated transition of the chain's consensus participants. It is designed to migrate a standard proof-of-stake (PoS) validator set down to a single validator while properly unbonding all other validators and returning staked tokens to delegators.

The migration is designed to be robust and safe, with mechanisms to handle different scenarios, including whether the chain has active IBC connections. The chain continues running on CometBFT after the migration completes.

## Initiating a Migration

The migration process is not automatic; it must be explicitly triggered by a governance proposal. This ensures that the chain's stakeholders have approved the transition.

The proposal must contain a `MsgMigrate` message.

### `MsgMigrate`

This message instructs the `migrationmngr` module to begin the migration process at a specified block height.

-   `authority`: The address of the governance module. This is set automatically when submitted via a proposal.
-   `block_height`: The block number at which the migration will start.
-   `sequencer`: An object defining the validator that will remain after migration.
    -   `name`: A human-readable name for the validator.
    -   `consensus_pubkey`: The consensus public key of the validator.

### Example Governance Proposal

Below is an example of the `messages` array within a `submit-proposal` JSON file.

#### Migrating to a Single Validator

This proposal will migrate the chain to be validated by a single validator, starting at block `1234567`. All other validators will have their delegations unbonded.

```json
{
  "messages": [
    {
      "@type": "/evabci.migrationmngr.v1.MsgMigrate",
      "authority": "cosmos10d07y265gmmuvt4z0w9aw880j2r6426u005ev2",
      "block_height": "1234567",
      "sequencer": {
        "name": "validator-01",
        "consensus_pubkey": {
          "@type": "/cosmos.crypto.ed25519.PubKey",
          "key": "YOUR_VALIDATOR_PUBKEY_BASE64"
        }
      }
    }
  ],
  "metadata": "...",
  "deposit": "...",
  "title": "Proposal to Migrate to a Single Validator",
  "summary": "This proposal initiates the migration to a single-validator chain operated by validator-01."
}
```

## How Migrations Work Under the Hood

The migration process is managed through state machine logic that is triggered at a specific block height, defined by a governance proposal. Once a migration is approved and the block height is reached, the module's `EndBlock` logic takes over.

The migration unbonds all validators except the designated sequencer. All delegations to the removed validators are unbonded, triggering the standard staking module unbonding period, after which delegators can reclaim their staked tokens.

### The Migration Mechanism

The module triggers unbonding for all validators that should be removed. The staking module's standard `Undelegate` function is called for each delegation, which:

-   Initiates the unbonding period (typically 21 days)
-   Removes voting power from the validators
-   Returns tokens to delegators after the unbonding period completes

The staking module itself handles validator set updates to CometBFT, so the `migrationmngr` module returns empty validator updates and lets the staking module manage the consensus engine updates.

### IBC-Aware Migrations

A critical feature of the migration manager is its awareness of the Inter-Blockchain Communication (IBC) protocol. A sudden, drastic change in the validator set can cause IBC light clients on other chains to fail their verification checks, leading to a broken connection.

To prevent this, the module first checks if IBC is enabled by verifying the existence of the IBC module's store key.

-   **If IBC is Enabled**: The migration is "smoothed" over a period of blocks (currently `30` blocks, defined by `IBCSmoothingFactor`). In each block during this period, a fraction of the validators are unbonded. This gradual change ensures that IBC light clients can safely update their trusted validator sets without interruption.
-   **If IBC is Not Enabled**: The migration can be performed "immediately" in a single block. All validators are unbonded at once at the migration start height.

### Migration Completion

After the migration period ends (at block `migration_end_height + 1`), the `PreBlock` logic cleans up the migration state from the store. The chain continues running normally on CometBFT with the remaining single validator.

## What Validators and Node Operators Need to Know

If you are a validator or node operator on a chain using this module, here's what to expect:

1.  **Monitor Governance**: The migration will be initiated by a governance proposal. Stay informed about upcoming proposals. The proposal will define the target block height for the migration.

2.  **Understand the Migration Timeline**:
    - With IBC enabled: Migration occurs from `block_height` to `block_height + 30`
    - Without IBC: Migration occurs at `block_height + 1`
    - State cleanup happens at `block_height + 31` (with IBC) or `block_height + 2` (without IBC)

3.  **Delegation Unbonding**: If your validator is being removed:
    - All delegations to your validator will be unbonded
    - Delegators will enter the standard unbonding period
    - After the unbonding period, delegators can reclaim their tokens
    - The chain continues running - there is no halt or binary upgrade required

4.  **Remaining Validator**: If you are operating the designated remaining validator:
    - Your validator will become the sole validator on the chain
    - Continue operating your node as normal
    - No special configuration changes are required

The chain continues operating on CometBFT throughout and after the migration process.

## Application Wiring (`app.go`)

To ensure the `migrationmngr` module can correctly manage the migration, you must make modifications to your application's main `app.go` file.

### 1. Add the Migration Manager Keeper to the `App` Struct

First, make the `migrationmngr`'s keeper available in your `App` struct. Find the struct definition (e.g., `type App struct { ... }`) and add the `MigrationmngrKeeper` field. You will also need to ensure it is properly instantiated in your app's constructor (e.g., `NewApp(...)`).

```go
import (
	// ... other imports
	migrationmngrkeeper "github.com/evstack/ev-abci/modules/migrationmngr/keeper"
	// ... other imports
)

type App struct {
	// ... other keepers
	StakingKeeper        *stakingkeeper.Keeper
	MigrationmngrKeeper  migrationmngrkeeper.Keeper // Add this line
	// ... other keepers
}
```

### 2. Use the Staking Wrapper Module

To ensure the migration manager can properly coordinate with the staking module, you must use the staking wrapper module provided in the `modules/staking` directory instead of the standard Cosmos SDK staking module.

The staking wrapper module ensures that validator updates are handled correctly during migrations by coordinating with the `migrationmngr` module.

**Update your application's module dependencies:**

Replace imports of the standard Cosmos SDK staking module:

```go
import (
	// OLD - remove this
	"github.com/cosmos/cosmos-sdk/x/staking"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)
```

**With imports of the ev-abci staking wrapper module:**

```go
import (
	// NEW - use the wrapper module
	"github.com/evstack/ev-abci/modules/staking"
	stakingkeeper "github.com/evstack/ev-abci/modules/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types" // types remain the same
)
```

If you're using depinject, the staking wrapper module will be automatically wired through the dependency injection system.
