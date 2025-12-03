# Migration Manager Module

## Introduction

The `migrationmngr` module orchestrates a one-time, coordinated transition of the chain's consensus participants. It supports:

- Staying on CometBFT with a single validator when `stay_on_comet = true` (still a Cosmos SDK chain, not a rollup)

## Initiating a Migration

The migration process is not automatic, it must be explicitly triggered by a governance proposal. This ensures that the chain's stakeholders have approved the transition.

The proposal must contain a `MsgMigrateToEvolve` message. If you set `stay_on_comet = true`, the chain will not halt after the migration completes and will continue running on CometBFT with a single validator.

### `MsgMigrateToEvolve`

This message instructs the `migrationmngr` module to begin the migration process at a specified block height.

-   `authority`: The address of the governance module. This is set automatically when submitted via a proposal.
-   `block_height`: The block number at which the migration will start.
-   `sequencer`: Proto field name, populate it with the single validator's identity.
    -   `name`: A human-readable name for the validator.
    -   `consensus_pubkey`: The consensus public key of the validator.
-   `attesters`: Not used in this scenario; leave as an empty list `[]`.

### Example Governance Proposals

Below are examples of the `messages` array within a `submit-proposal` JSON file.

#### Example 1: Migrating to a Single Validator

This proposal will migrate the chain to be validated by a single validator, starting at block `1234567`.

```json
{
  "messages": [
    {
      "@type": "/evabci.migrationmngr.v1.MsgMigrateToEvolve",
      "authority": "cosmos10d07y265gmmuvt4z0w9aw880j2r6426u005ev2",
      "block_height": "1234567",
      "sequencer": {
        "name": "validator-01",
        "consensus_pubkey": {
          "@type": "/cosmos.crypto.ed25519.PubKey",
          "key": "YOUR_SEQUENCER_PUBKEY_BASE64"
        }
      },
      "attesters": []
    }
  ],
  "metadata": "...",
  "deposit": "...",
  "title": "Proposal to Migrate to a Single Sequencer",
  "summary": "This proposal initiates the migration to a single-sequencer chain operated by sequencer-01."
}
```

<!-- Example with attesters removed because this scenario is sequencer-only. -->

## How Migrations Work Under the Hood

The migration process is managed through state machine logic that is triggered at a specific block height, defined by a governance proposal. Once a migration is approved and the block height is reached, the module's `EndBlock` and `PreBlock` logic take over.

In this scenario, the migration sets the chain to a single, designated validator which becomes the sole block producer.

### The Migration Mechanism

The core of the migration is handled by returning `abci.ValidatorUpdate` messages to the underlying consensus engine (CometBFT). These updates change the voting power of validators.

-   **Removing Old Validators**: The existing validators that are not the new single validator have their power reduced to `0`.
-   **Adding New Participants**: The new single validator is added to the consensus set with a power of `1`.

### IBC considerations

After the upgrade completes, you must submit an IBC `MsgUpdateClient` at `migration_height + 1` for EVERY counterparty client to refresh their consensus state.

With Hermes, for each counterparty and client ID, run:

```bash
hermes --json update client \
  --host-chain <HOST_CHAIN_ID> \
  --client <CLIENT_ID> \
  --height $((migration_height+1))
```

### A Coordinated Upgrade

The final step depends on the `stay_on_comet` flag provided in the proposal.

- One block after the migration end (`migration_height + 1`) the module cleans up migration state and continues running on CometBFT without halting and the chain now runs with the configured single validator.

## What Validators and Node Operators Need to Do

If you are a validator or node operator on a chain using this module, you must be prepared for the migration event.

1.  **Monitor Governance**: The migration will be initiated by a governance proposal. Stay informed about upcoming proposals. The proposal will define the target block height for the migration.

2.  **Migration completion behavior**:
    - If `stay_on_comet = true`: The chain does not halt. It continues on CometBFT with the single validator. No operator action is required beyond normal operations. if using IBC, plan to `MsgUpdateClient` at `migration_height + 1` on counterparties.


## Application Wiring (`app.go`)

To ensure the `migrationmngr` module can correctly take control during a migration, you must make modifications to your application's main `app.go` file. This is crucial to prevent the standard `staking` module from sending conflicting validator set updates.

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

To prevent the standard `staking` module from sending conflicting validator updates during migration, you must use the staking wrapper module provided in the `modules/staking` directory instead of the standard Cosmos SDK staking module.

The staking wrapper module automatically nullifies validator updates from the standard staking module by returning an empty validator update list in its `EndBlock` method. This ensures that the `migrationmngr` module remains the sole source of validator set changes during the migration process.

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

If you're using depinject, the staking wrapper module will be automatically wired through the dependency injection system and will prevent validator updates from the staking module while allowing the `migrationmngr` module to control validator set changes during migrations.

### 3. Wiring examples

Below are minimal code snippets to help wire the module and keeper.

- Using traditional `module.Manager` in `app.go`:

```go
import (
    // modules
    migrationmngrmodule "github.com/evstack/ev-abci/modules/migrationmngr"
    stakingwrapper "github.com/evstack/ev-abci/modules/staking"

    // staking types remain the same
    stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// in NewApp(...)
m := module.NewManager(
    // ... other modules ...
    stakingwrapper.NewAppModule(appCodec, stakingKeeper, accountKeeper, bankKeeper, legacySubspace),
    migrationmngrmodule.NewAppModule(appCodec, app.MigrationmngrKeeper),
)
```

- Using depinject app config (module registration is handled by init registrations):

```go
// Import the modules somewhere in your app so their init() runs:
import (
    _ "github.com/evstack/ev-abci/modules/migrationmngr"
    _ "github.com/evstack/ev-abci/modules/staking"
)

// In your app config (protobuf or go-based), ensure x/staking is the wrapper
// by importing the module above; it will register itself and provide the keeper.
// migrationmngr's depinject provider expects a StakingKeeper and will wire itself.
```
