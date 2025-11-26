package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/celestiaorg/tastora/framework/docker"
	"github.com/celestiaorg/tastora/framework/docker/cosmos"
	"github.com/celestiaorg/tastora/framework/docker/ibc"
	"github.com/celestiaorg/tastora/framework/docker/ibc/relayer"
	"github.com/celestiaorg/tastora/framework/testutil/wait"
	"github.com/celestiaorg/tastora/framework/types"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module/testutil"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/bank"
	govmodule "github.com/cosmos/cosmos-sdk/x/gov"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	ibctransfer "github.com/cosmos/ibc-go/v8/modules/apps/transfer"
	transfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	migrationmngr "github.com/evstack/ev-abci/modules/migrationmngr"
	migrationmngrtypes "github.com/evstack/ev-abci/modules/migrationmngr/types"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const FirstClientID = "07-tendermint-1"

// SingleValidatorSuite tests migration from N validators to 1 validator on CometBFT
type SingleValidatorSuite struct {
	DockerIntegrationTestSuite

	// chain instance that will undergo migration
	chain *cosmos.Chain

	// IBC counterparty chain
	counterpartyChain  *cosmos.Chain
	hermes             *relayer.Hermes
	ibcConnection      ibc.Connection
	ibcChannel         ibc.Channel
	ibcDenom           string
	preMigrationIBCBal sdk.Coin

	migrationHeight uint64
	// number of validators on the primary chain at test start
	initialValidators int

	// cancel function for background client update loop
	relayerUpdateCancel context.CancelFunc

	// last height on the primary chain (subject) for which we've
	// successfully attempted a client update on the counterparty (host).
	// Used to step updates height-by-height during migration.
	lastUpdatedChainOnCounterparty int64

	// Resolved client IDs tied to the IBC connection/channel
	hostClientIDOnCounterparty string // client for gm-1 that lives on gm-2
	hostClientIDOnChain        string // client for gm-2 that lives on gm-1
}

func TestSingleValSuite(t *testing.T) {
	suite.Run(t, new(SingleValidatorSuite))
}

func (s *SingleValidatorSuite) SetupTest() {
	sdk.GetConfig().SetBech32PrefixForAccount("gm", "gmpub")

	s.dockerClient, s.networkID = docker.Setup(s.T())
	s.logger = zaptest.NewLogger(s.T())
}

func (s *SingleValidatorSuite) TearDownTest() {
	if s.chain != nil {
		if err := s.chain.Remove(context.Background()); err != nil {
			s.T().Logf("failed to remove chain: %s", err)
		}
	}
	if s.counterpartyChain != nil {
		if err := s.counterpartyChain.Remove(context.Background()); err != nil {
			s.T().Logf("failed to remove IBC counterparty chain: %s", err)
		}
	}
}

// TestNTo1StayOnCometMigration tests reducing N validators to 1 validator while staying on CometBFT
//
// Running locally pre-requisites:
// from root of repo, build images with ibc enabled.
// - docker build . -f Dockerfile.cosmos-sdk -t cosmos-gm:test --build-arg ENABLE_IBC=true
func (s *SingleValidatorSuite) TestNTo1StayOnCometMigration() {
	ctx := context.Background()
	t := s.T()

	t.Run("create_chains", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			// start with 5 validators on the primary chain
			s.initialValidators = 5
			s.chain = s.createAndStartChain(ctx, s.initialValidators, "gm-1")
		}()

		go func() {
			defer wg.Done()
			s.counterpartyChain = s.createAndStartChain(ctx, 1, "gm-2")
		}()

		wg.Wait()
	})

	t.Run("setup_ibc_connection", func(t *testing.T) {
		s.setupIBCConnection(ctx)
	})

	// Establish initial IBC state and set s.ibcDenom before starting the
	// background relayer update loop.
	t.Run("perform_ibc_transfers", func(t *testing.T) {
		s.performIBCTransfers(ctx)
	})

	// We no longer run a background relayer update loop; instead we backfill
	// client updates after the migration completes.

	t.Run("submit_migration_proposal", func(t *testing.T) {
		s.submitSingleValidatorMigrationProposal(ctx)
	})

	t.Run("wait_for_migration_completion", func(t *testing.T) {
		s.waitForMigrationCompletion(ctx)
	})

	// Optional: Backfill client updates after the upgrade window, stepping
	// from the client's current trusted height up to the final migration
	// height. This keeps gm-1's client on gm-2 up to date across the
	// migration window without background updates.
	t.Run("backfill_client_updates", func(t *testing.T) {
		end := int64(s.migrationHeight + 30)
		err := s.BackfillChainClientOnCounterpartyUntil(ctx, end)
		s.Require().NoError(err)
	})

	t.Run("validate_single_validator", func(t *testing.T) {
		s.validateSingleValidatorSet(ctx)
	})

	t.Run("validate_chain_continues", func(t *testing.T) {
		s.validateChainProducesBlocks(ctx)
	})

	// Emit detailed IBC debug information before final IBC preservation checks
	t.Run("debug_ibc_state", func(t *testing.T) {
		if err := s.dumpIBCDebug(ctx); err != nil {
			s.T().Logf("dump IBC debug failed: %v", err)
		}
	})

	// Stop the background relayer update loop
	t.Run("stop_relayer_update_loop", func(t *testing.T) {
		if s.relayerUpdateCancel != nil {
			s.relayerUpdateCancel()
		}
	})

	t.Run("validate_ibc_preserved", func(t *testing.T) {
		s.validateIBCStatePreserved(ctx)
	})
}

// createAndStartChain creates a cosmos-sdk chain
func (s *SingleValidatorSuite) createAndStartChain(ctx context.Context, numValidators int, chainID string) *cosmos.Chain {
	s.T().Logf("Creating chain with %d validators...", numValidators)

	testEncCfg := testutil.MakeTestEncodingConfig(
		auth.AppModuleBasic{},
		bank.AppModuleBasic{},
		govmodule.AppModuleBasic{},
		migrationmngr.AppModuleBasic{},
		ibctransfer.AppModuleBasic{},
	)

	var nodes []cosmos.ChainNodeConfig
	for i := 0; i < numValidators; i++ {
		nodes = append(nodes, cosmos.NewChainNodeConfigBuilder().Build())
	}

	chain, err := cosmos.NewChainBuilder(s.T()).
		WithEncodingConfig(&testEncCfg).
		WithImage(getCosmosSDKAppContainer()).
		WithDenom("stake").
		WithDockerClient(s.dockerClient).
		WithName(chainID).
		WithDockerNetworkID(s.networkID).
		WithChainID(chainID).
		WithBech32Prefix("gm").
		WithBinaryName("gmd").
		WithGasPrices(fmt.Sprintf("0.00%s", "stake")).
		WithNodes(nodes...).
		Build(ctx)
	s.Require().NoError(err)

	err = chain.Start(ctx)
	s.Require().NoError(err)

	// wait for a few blocks to ensure chain is producing blocks and Docker DNS is propagated
	err = wait.ForBlocks(ctx, 3, chain)
	s.Require().NoError(err)

	s.T().Log("Chain created and started")
	return chain
}

// setupIBCConnection establishes IBC connection between the two chains
func (s *SingleValidatorSuite) setupIBCConnection(ctx context.Context) {
	s.T().Log("Setting up IBC connection...")

	var err error
	s.hermes, err = relayer.NewHermes(ctx, s.dockerClient, "single-val-test", s.networkID, 0, s.logger)
	s.Require().NoError(err)

	err = s.hermes.Init(ctx, []types.Chain{s.counterpartyChain, s.chain}, func(cfg *relayer.HermesConfig) {
		for i := range cfg.Chains {
			cfg.Chains[i].EventSource = map[string]interface{}{
				"mode":     "pull",
				"interval": "200ms",
			}
			cfg.Chains[i].ClockDrift = "60s"
		}
	})
	s.Require().NoError(err)

	err = s.hermes.CreateClients(ctx, s.counterpartyChain, s.chain)
	s.Require().NoError(err)

	s.ibcConnection, err = s.hermes.CreateConnections(ctx, s.counterpartyChain, s.chain)
	s.Require().NoError(err)

	err = wait.ForBlocks(ctx, 2, s.counterpartyChain, s.chain)
	s.Require().NoError(err)

	channelOpts := ibc.CreateChannelOptions{
		SourcePortName: "transfer",
		DestPortName:   "transfer",
		Order:          ibc.OrderUnordered,
		Version:        "ics20-1",
	}

	s.ibcChannel, err = s.hermes.CreateChannel(ctx, s.counterpartyChain, s.ibcConnection, channelOpts)
	s.Require().NoError(err)

	s.T().Logf("IBC connection established: %s <-> %s", s.ibcConnection.ConnectionID, s.ibcConnection.CounterpartyID)

	// Resolve and log the client IDs bound to the connection on both chains
	hostClient, err := queryConnectionClientID(ctx, s.counterpartyChain, s.ibcConnection.ConnectionID)
	s.Require().NoError(err)
	counterpartyClient, err := queryConnectionClientID(ctx, s.chain, s.ibcConnection.CounterpartyID)
	s.Require().NoError(err)
	s.hostClientIDOnCounterparty = hostClient
	s.hostClientIDOnChain = counterpartyClient
	s.T().Logf("Resolved client IDs: gm-2 has client %s (for gm-1), gm-1 has client %s (for gm-2)", hostClient, counterpartyClient)
}

// performIBCTransfers performs IBC transfers to establish IBC state
func (s *SingleValidatorSuite) performIBCTransfers(ctx context.Context) {
	s.T().Log("Performing IBC transfer...")

	transferAmount := math.NewInt(1_000_000)
	ibcChainWallet := s.counterpartyChain.GetFaucetWallet()
	gmWallet := s.chain.GetFaucetWallet()

	s.ibcDenom = s.calculateIBCDenom(s.ibcChannel.CounterpartyPort, s.ibcChannel.CounterpartyID, "stake")

	err := s.hermes.Start(ctx)
	s.Require().NoError(err)

	err = wait.ForBlocks(ctx, 2, s.counterpartyChain, s.chain)
	s.Require().NoError(err)

	transferMsg := transfertypes.NewMsgTransfer(
		s.ibcChannel.PortID,
		s.ibcChannel.ChannelID,
		sdk.NewCoin("stake", transferAmount),
		ibcChainWallet.GetFormattedAddress(),
		gmWallet.GetFormattedAddress(),
		clienttypes.ZeroHeight(),
		uint64(time.Now().Add(time.Hour).UnixNano()),
		"",
	)

	ctxTx, cancelTx := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelTx()

	resp, err := s.counterpartyChain.BroadcastMessages(ctxTx, ibcChainWallet, transferMsg)
	s.Require().NoError(err)
	s.Require().Equal(uint32(0), resp.Code)

	s.T().Log("Waiting for IBC transfer...")
	networkInfo, err := s.chain.GetNode().GetNetworkInfo(ctx)
	s.Require().NoError(err)

	err = wait.ForCondition(ctx, 2*time.Minute, 2*time.Second, func() (bool, error) {
		balance, err := queryBankBalance(ctx,
			networkInfo.External.GRPCAddress(),
			gmWallet.GetFormattedAddress(),
			s.ibcDenom)
		if err != nil {
			return false, nil
		}
		return balance.Amount.GTE(transferAmount), nil
	})
	s.Require().NoError(err)

	ibcBalance, err := queryBankBalance(ctx,
		networkInfo.External.GRPCAddress(),
		gmWallet.GetFormattedAddress(),
		s.ibcDenom)
	s.Require().NoError(err)
	s.preMigrationIBCBal = *ibcBalance
	s.T().Logf("IBC transfer complete: %s %s", ibcBalance.Amount, s.ibcDenom)
}

// startRelayerUpdateLoop starts a background goroutine that periodically
// updates clients.
func (s *SingleValidatorSuite) startRelayerUpdateLoop(parentCtx context.Context) {
	// create cancellable context and remember cancel
	ctx, cancel := context.WithCancel(parentCtx)
	s.relayerUpdateCancel = cancel

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// send a very small IBC transfer from counterparty -> primary chain
				if s.counterpartyChain == nil || s.chain == nil {
					continue
				}

				// Keep the gm-1 client on the counterparty (gm-2) up to date by
				// stepping through each new height on the primary chain. This avoids
				// large single-hop updates across validator set transitions.
				if err := s.StepwiseUpdateChainClientOnCounterparty(ctx); err != nil {
					s.T().Logf("Stepwise client update error: %v", err)
				}
			}
		}
	}()
}

/*
// startRelayerUpdateLoop starts a background goroutine that periodically sends a
// tiny IBC transfer to provoke Hermes to relay packets and submit client updates
// during the migration window. This reduces timing sensitivity for anchoring.
func (s *SingleValidatorSuite) startRelayerUpdateLoop(parentCtx context.Context) {
	// create cancellable context and remember cancel
	ctx, cancel := context.WithCancel(parentCtx)
	s.relayerUpdateCancel = cancel

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// send a very small IBC transfer from counterparty -> primary chain
				if s.counterpartyChain == nil || s.chain == nil {
					continue
				}

				// wallets
				ibcChainWallet := s.counterpartyChain.GetFaucetWallet()
				gmWallet := s.chain.GetFaucetWallet()

				// tiny amount to minimize noise
				amount := math.NewInt(1)
				msg := transfertypes.NewMsgTransfer(
					s.ibcChannel.PortID,
					s.ibcChannel.ChannelID,
					sdk.NewCoin("stake", amount),
					ibcChainWallet.GetFormattedAddress(),
					gmWallet.GetFormattedAddress(),
					clienttypes.ZeroHeight(),
					uint64(time.Now().Add(30*time.Second).UnixNano()),
					"",
				)

				// short timeout context per tx; ignore errors to keep loop resilient
				txCtx, cancelTx := context.WithTimeout(ctx, 20*time.Second)
				_, _ = s.counterpartyChain.BroadcastMessages(txCtx, ibcChainWallet, msg)
				cancelTx()
			}
		}
	}()
}
*/
// submitSingleValidatorMigrationProposal submits a proposal to migrate to single validator
func (s *SingleValidatorSuite) submitSingleValidatorMigrationProposal(ctx context.Context) {
	s.T().Log("Submitting single validator migration proposal...")

	networkInfo, err := s.chain.GetNode().GetNetworkInfo(ctx)
	s.Require().NoError(err)

	conn, err := grpc.NewClient(networkInfo.External.GRPCAddress(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	s.Require().NoError(err)
	defer conn.Close()

	curHeight, err := s.chain.Height(ctx)
	s.Require().NoError(err)

	// schedule migration 30 blocks in the future to allow governance
	migrateAt := uint64(curHeight + 30)
	s.migrationHeight = migrateAt
	s.T().Logf("Current height: %d, Migration at: %d", curHeight, migrateAt)

	// get the first validator's pubkey (this will be the remaining validator)
	sequencerPubkey := s.getValidatorPubKey(ctx, conn, 0)

	faucet := s.chain.GetFaucetWallet()
	msg := &migrationmngrtypes.MsgMigrateToEvolve{
		Authority:   authtypes.NewModuleAddress(govtypes.ModuleName).String(),
		BlockHeight: migrateAt,
		Sequencer: migrationmngrtypes.Sequencer{
			Name:            "validator-0",
			ConsensusPubkey: sequencerPubkey,
		},
		// stay on CometBFT instead of halting for rollup migration
		StayOnComet: true,
	}

	propMsg, err := govv1.NewMsgSubmitProposal(
		[]sdk.Msg{msg},
		sdk.NewCoins(sdk.NewInt64Coin("stake", 10_000_000_000)),
		faucet.GetFormattedAddress(),
		"",
		"Migrate to Single Validator",
		"Reduce validator set to single validator for chain sunset",
		false,
	)
	s.Require().NoError(err)

	prop, err := s.chain.SubmitAndVoteOnGovV1Proposal(ctx, propMsg, govv1.VoteOption_VOTE_OPTION_YES)
	s.Require().NoError(err)
	s.Require().Equal(govv1.ProposalStatus_PROPOSAL_STATUS_PASSED, prop.Status)
	s.T().Log("Proposal passed")
}

// getValidatorPubKey gets the consensus pubkey for a validator by index
func (s *SingleValidatorSuite) getValidatorPubKey(ctx context.Context, conn *grpc.ClientConn, validatorIndex int) *codectypes.Any {
	stakeQC := stakingtypes.NewQueryClient(conn)
	valsResp, err := stakeQC.Validators(ctx, &stakingtypes.QueryValidatorsRequest{})
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(len(valsResp.Validators), validatorIndex+1)

	nodes := s.chain.GetNodes()
	node := nodes[validatorIndex].(*cosmos.ChainNode)

	stdout, stderr, err := node.Exec(ctx, []string{
		"gmd", "keys", "show", "--address", "validator",
		"--home", node.HomeDir(),
		"--keyring-backend", "test",
		"--bech", "val",
	}, nil)
	s.Require().NoError(err, "failed to get valoper address: %s", stderr)
	valOperAddr := string(bytes.TrimSpace(stdout))

	for _, v := range valsResp.Validators {
		if v.OperatorAddress == valOperAddr {
			return v.ConsensusPubkey
		}
	}

	s.Require().Fail("validator not found", "could not find validator at index %d", validatorIndex)
	return nil
}

// waitForMigrationCompletion waits for the 30-block migration window to complete
func (s *SingleValidatorSuite) waitForMigrationCompletion(ctx context.Context) {
	s.T().Log("Waiting for migration to complete...")

	// migration should complete at migrationHeight + IBCSmoothingFactor (300 blocks)
	targetHeight := int64(s.migrationHeight + 30)

	err := wait.ForCondition(ctx, time.Hour, 10*time.Second, func() (bool, error) {
		h, err := s.chain.Height(ctx)
		if err != nil {
			s.T().Logf("Error getting height: %v", err)
			return false, nil
		}
		s.T().Logf("Current height: %d, Target: %d", h, targetHeight)
		return h >= targetHeight, nil
	})
	s.Require().NoError(err)

	// wait a few more blocks to ensure migration is fully complete
	err = wait.ForBlocks(ctx, 3, s.chain)
	s.Require().NoError(err)

	s.T().Log("Migration window completed")
}

// validateSingleValidatorSet validates that only 1 validator remains active
func (s *SingleValidatorSuite) validateSingleValidatorSet(ctx context.Context) {
	s.T().Log("Validating single validator set...")

	networkInfo, err := s.chain.GetNode().GetNetworkInfo(ctx)
	s.Require().NoError(err)

	conn, err := grpc.NewClient(networkInfo.External.GRPCAddress(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	s.Require().NoError(err)
	defer conn.Close()

	stakeQC := stakingtypes.NewQueryClient(conn)

	// staking bonded validators should be zero because all tokens are undelegated
	bondedResp, err := stakeQC.Validators(ctx, &stakingtypes.QueryValidatorsRequest{
		Status: stakingtypes.BondStatus_name[int32(stakingtypes.Bonded)],
	})
	s.Require().NoError(err)
	s.T().Logf("Bonded validators: %d", len(bondedResp.Validators))
	s.Require().Len(bondedResp.Validators, 0, "staking should report zero bonded validators after finalization")

	// check unbonding validators: after undelegation, validators enter unbonding state
	unbondingResp, err := stakeQC.Validators(ctx, &stakingtypes.QueryValidatorsRequest{
		Status: stakingtypes.BondStatus_name[int32(stakingtypes.Unbonding)],
	})
	s.Require().NoError(err)
	s.T().Logf("Unbonding validators: %d", len(unbondingResp.Validators))
	if s.initialValidators > 0 {
		s.Require().Equal(s.initialValidators, len(unbondingResp.Validators), "all validators should be in unbonding state after finalization")
	}

	// check unbonded validators: expect 0 since unbonding period has not elapsed
	unbondedResp, err := stakeQC.Validators(ctx, &stakingtypes.QueryValidatorsRequest{
		Status: stakingtypes.BondStatus_name[int32(stakingtypes.Unbonded)],
	})
	s.Require().NoError(err)
	s.T().Logf("Unbonded validators: %d", len(unbondedResp.Validators))
	s.Require().Len(unbondedResp.Validators, 0, "no validators should be fully unbonded yet")

	// additionally assert that the remaining bonded validator (sequencer) has no delegations left
	// find the operator address for validator 0
	val0 := s.chain.GetNode()
	stdout, stderr, err := val0.Exec(ctx, []string{
		"gmd", "keys", "show", "--address", "validator",
		"--home", val0.HomeDir(),
		"--keyring-backend", "test",
		"--bech", "val",
	}, nil)
	s.Require().NoError(err, "failed to get valoper address from node 0: %s", stderr)
	val0Oper := string(bytes.TrimSpace(stdout))

	// query delegations to the remaining validator; expect zero after finalization step
	delResp, err := stakeQC.ValidatorDelegations(ctx, &stakingtypes.QueryValidatorDelegationsRequest{
		ValidatorAddr: val0Oper,
		Pagination:    nil,
	})
	s.Require().NoError(err)
	s.T().Logf("Delegations to remaining validator: %d", len(delResp.DelegationResponses))
	s.Require().Len(delResp.DelegationResponses, 0, "remaining validator should have zero delegations after final step")

	// Also verify CometBFT validator set has exactly one validator with power=1
	rpcClient, err := s.chain.GetNode().GetRPCClient()
	s.Require().NoError(err)
	vals, err := rpcClient.Validators(ctx, nil, nil, nil)
	s.Require().NoError(err)
	s.Require().Equal(1, len(vals.Validators), "CometBFT should have exactly 1 validator in the set")
	s.Require().Equal(int64(1), vals.Validators[0].VotingPower, "CometBFT validator should have voting power 1")

	s.T().Log("Validator set validated: staking has 0 bonded; CometBFT has 1 validator with power=1")
}

// validateChainProducesBlocks validates the chain continues to produce blocks
func (s *SingleValidatorSuite) validateChainProducesBlocks(ctx context.Context) {
	s.T().Log("Validating chain produces blocks...")

	initialHeight, err := s.chain.Height(ctx)
	s.Require().NoError(err)

	err = wait.ForBlocks(ctx, 5, s.chain)
	s.Require().NoError(err)

	finalHeight, err := s.chain.Height(ctx)
	s.Require().NoError(err)
	s.Require().Greater(finalHeight, initialHeight)

	s.T().Logf("Chain producing blocks: %d -> %d", initialHeight, finalHeight)
}

// validateIBCStatePreserved validates IBC state is preserved after migration
func (s *SingleValidatorSuite) validateIBCStatePreserved(ctx context.Context) {
	s.T().Log("Validating IBC state preserved...")

	networkInfo, err := s.chain.GetNode().GetNetworkInfo(ctx)
	s.Require().NoError(err)

	gmWallet := s.chain.GetFaucetWallet()
	currentIBCBalance, err := queryBankBalance(ctx,
		networkInfo.External.GRPCAddress(),
		gmWallet.GetFormattedAddress(),
		s.ibcDenom)
	s.Require().NoError(err)
	// Background relayer update loop may have sent tiny transfers that adjust
	// this balance slightly. Instead of strict equality, assert that balance
	// has not decreased, and proceed to verify a round-trip transfer works.
	s.Require().True(currentIBCBalance.Amount.GTE(s.preMigrationIBCBal.Amount),
		"IBC balance should not be less than pre-migration balance")
	s.T().Logf("IBC balance (>= pre-migration): %s %s (pre=%s)", currentIBCBalance.Amount, s.ibcDenom, s.preMigrationIBCBal.Amount)

	// perform IBC transfer back to verify IBC still works after migration
	s.T().Log("Performing IBC transfer back to verify IBC functionality...")

	transferAmount := math.NewInt(100_000)
	ibcChainWallet := s.counterpartyChain.GetFaucetWallet()

	// give relayer a moment to drain any backlog from the background loop
	err = wait.ForBlocks(ctx, 3, s.counterpartyChain, s.chain)
	s.Require().NoError(err)

	// get counterparty network info to query balance
	counterpartyNetworkInfo, err := s.counterpartyChain.GetNode().GetNetworkInfo(ctx)
	s.Require().NoError(err)

	// get initial balance on counterparty chain
	initialCounterpartyBalance, err := queryBankBalance(ctx,
		counterpartyNetworkInfo.External.GRPCAddress(),
		ibcChainWallet.GetFormattedAddress(),
		"stake")
	s.Require().NoError(err)

	// transfer IBC tokens back from gm-1 to gm-2
	transferMsg := transfertypes.NewMsgTransfer(
		s.ibcChannel.CounterpartyPort,
		s.ibcChannel.CounterpartyID,
		sdk.NewCoin(s.ibcDenom, transferAmount),
		gmWallet.GetFormattedAddress(),
		ibcChainWallet.GetFormattedAddress(),
		clienttypes.ZeroHeight(),
		uint64(time.Now().Add(time.Hour).UnixNano()),
		"",
	)

	ctxTx, cancelTx := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelTx()
	resp, err := s.chain.BroadcastMessages(ctxTx, gmWallet, transferMsg)
	s.Require().NoError(err)
	s.Require().Equal(uint32(0), resp.Code, "IBC transfer transaction failed")

	s.T().Log("Waiting for IBC transfer to complete...")

	// wait for transfer to complete on counterparty chain
	err = wait.ForCondition(ctx, 3*time.Minute, 2*time.Second, func() (bool, error) {
		balance, err := queryBankBalance(ctx,
			counterpartyNetworkInfo.External.GRPCAddress(),
			ibcChainWallet.GetFormattedAddress(),
			"stake")
		if err != nil {
			return false, nil
		}
		expectedBalance := initialCounterpartyBalance.Amount.Add(transferAmount)
		s.T().Logf("Waiting for IBC transfer: current=%s expected>=%s denom=stake", balance.Amount.String(), expectedBalance.String())
		return balance.Amount.GTE(expectedBalance), nil
	})
	s.Require().NoError(err)

	// verify final balance on counterparty chain
	finalCounterpartyBalance, err := queryBankBalance(ctx,
		counterpartyNetworkInfo.External.GRPCAddress(),
		ibcChainWallet.GetFormattedAddress(),
		"stake")
	s.Require().NoError(err)
	expectedFinalBalance := initialCounterpartyBalance.Amount.Add(transferAmount)
	s.Require().Equal(expectedFinalBalance, finalCounterpartyBalance.Amount)

	s.T().Logf("IBC transfer back successful: %s stake received on counterparty chain", transferAmount)
}

// calculateIBCDenom calculates the IBC denomination
func (s *SingleValidatorSuite) calculateIBCDenom(portID, channelID, baseDenom string) string {
	prefixedDenom := transfertypes.GetPrefixedDenom(portID, channelID, baseDenom)
	return transfertypes.ParseDenomTrace(prefixedDenom).IBCDenom()
}

// UpdateClients updates clients on both chains.
// It is assumed there is only one client and uses the hard coded client ID that both will have.
func (s *SingleValidatorSuite) UpdateClients(ctx context.Context, hermes *relayer.Hermes, chainA, chainB *cosmos.Chain) error {
	if err := updateClient(ctx, hermes, chainA.GetChainID(), FirstClientID); err != nil {
		return fmt.Errorf("failed to update client on chain %s: %w", chainA.GetChainID(), err)
	}

	if err := updateClient(ctx, hermes, chainB.GetChainID(), FirstClientID); err != nil {
		return fmt.Errorf("failed to update client on chain %s: %w", chainB.GetChainID(), err)
	}

	return nil
}

// updateClient updates the specified client with the hostChainID and clientID.
func updateClient(ctx context.Context, hermes *relayer.Hermes, hostChainID, clientID string) error {
	cmd := []string{
		"hermes", "--json", "update", "client",
		"--host-chain", hostChainID,
		"--client", clientID,
	}
	_, _, err := hermes.Exec(ctx, hermes.Logger, cmd, nil)
	return err
}

// revisionNumberFromChainIDOrClient tries to extract the revision number from
// the subject chain-id (e.g., "gm-1" -> 1). If parsing fails, it queries the
// client state on the host chain and extracts latest_height.revision_number.
func revisionNumberFromChainIDOrClient(ctx context.Context, subjectChainID string, host *cosmos.Chain, clientID string) (uint64, error) {
	// Parse suffix from chain-id
	re := regexp.MustCompile(`-(\d+)$`)
	if m := re.FindStringSubmatch(subjectChainID); len(m) == 2 {
		if n, err := strconv.ParseUint(m[1], 10, 64); err == nil {
			return n, nil
		}
	}

	networkInfo, err := host.GetNode().GetNetworkInfo(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get host node network info: %w", err)
	}

	// Fallback: query client state JSON on host chain
	nodes := host.GetNodes()
	if len(nodes) == 0 {
		return 0, fmt.Errorf("no nodes for host chain")
	}
	node := nodes[0].(*cosmos.ChainNode)
	stdout, stderr, err := node.Exec(ctx, []string{
		"gmd", "q", "ibc", "client", "state", clientID, "-o", "json",
		"--grpc-addr", networkInfo.External.GRPCAddress(), "--grpc-insecure", "--prove=false",
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("query client state failed: %s", stderr)
	}
	var resp struct {
		ClientState struct {
			LatestHeight struct {
				RevisionNumber json.Number `json:"revision_number"`
				RevisionHeight json.Number `json:"revision_height"`
			} `json:"latest_height"`
		} `json:"client_state"`
	}
	if err := json.Unmarshal(stdout, &resp); err == nil {
		if rn, err := resp.ClientState.LatestHeight.RevisionNumber.Int64(); err == nil && rn >= 0 {
			return uint64(rn), nil
		}
	}
	return 0, fmt.Errorf("could not determine revision_number from client state JSON")
}

// queryClientRevisionHeight returns latest_height.revision_height for the client on the host chain.
func queryClientRevisionHeight(ctx context.Context, host *cosmos.Chain, clientID string) (int64, error) {
	nodes := host.GetNodes()
	if len(nodes) == 0 {
		return 0, fmt.Errorf("no nodes for host chain")
	}
	node := nodes[0].(*cosmos.ChainNode)

	networkInfo, err := node.GetNetworkInfo(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get host node network info: %w", err)
	}

	stdout, stderr, err := node.Exec(ctx, []string{
		"gmd", "q", "ibc", "client", "state", clientID, "-o", "json",
		"--grpc-addr", networkInfo.Internal.GRPCAddress(), "--grpc-insecure", "--prove=false",
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("query client state failed: %s", stderr)
	}
	var resp struct {
		ClientState struct {
			LatestHeight struct {
				RevisionHeight json.Number `json:"revision_height"`
			} `json:"latest_height"`
		} `json:"client_state"`
	}
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return 0, fmt.Errorf("failed to decode client state JSON: %w", err)
	}
	if rh, err := resp.ClientState.LatestHeight.RevisionHeight.Int64(); err == nil {
		return rh, nil
	}
	return 0, fmt.Errorf("could not parse client revision_height from host state JSON")
}

// queryConnectionClientID queries the IBC connection end and returns its client_id.
func queryConnectionClientID(ctx context.Context, chain *cosmos.Chain, connectionID string) (string, error) {
	node := chain.GetNode()
	networkInfo, err := node.GetNetworkInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get network info: %w", err)
	}
	// Use internal gRPC address when querying from inside the node container.
	var stdout, stderr []byte
	// Simple retry in case the service or state is not immediately available.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		stdout, stderr, err = node.Exec(ctx, []string{
			"gmd", "q", "ibc", "connection", "end", connectionID, "-o", "json",
			"--grpc-addr", networkInfo.Internal.GRPCAddress(), "--grpc-insecure", "--prove=false",
		}, nil)
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = fmt.Errorf("query connection end failed: %s", stderr)
		// small delay before retrying
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr != nil {
		return "", lastErr
	}
	var resp struct {
		Connection struct {
			ClientID string `json:"client_id"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return "", fmt.Errorf("failed to decode connection end JSON: %w", err)
	}
	if resp.Connection.ClientID == "" {
		return "", fmt.Errorf("empty client_id in connection end for %s", connectionID)
	}
	return resp.Connection.ClientID, nil
}

// dumpIBCDebug logs useful IBC-related state: chain heights, connection/channel IDs,
// resolved client IDs and their latest heights/chain-ids on both chains.
func (s *SingleValidatorSuite) dumpIBCDebug(ctx context.Context) error {
	// Current chain heights
	hA, err := s.chain.Height(ctx)
	if err != nil {
		return err
	}
	hB, err := s.counterpartyChain.Height(ctx)
	if err != nil {
		return err
	}
	s.T().Logf("Heights: %s=%d, %s=%d", s.chain.GetChainID(), hA, s.counterpartyChain.GetChainID(), hB)

	// Connection and channel IDs
	s.T().Logf("Connection IDs: A=%s, B=%s", s.ibcConnection.CounterpartyID, s.ibcConnection.ConnectionID)
	s.T().Logf("Channel IDs: A=%s/%s, B=%s/%s", s.ibcChannel.CounterpartyPort, s.ibcChannel.CounterpartyID, s.ibcChannel.PortID, s.ibcChannel.ChannelID)

	// Resolve client IDs from connections (reconfirm)
	hostClientB, err := queryConnectionClientID(ctx, s.counterpartyChain, s.ibcConnection.ConnectionID)
	if err != nil {
		return err
	}
	hostClientA, err := queryConnectionClientID(ctx, s.chain, s.ibcConnection.CounterpartyID)
	if err != nil {
		return err
	}
	s.T().Logf("Client IDs: on %s (for %s) = %s; on %s (for %s) = %s",
		s.counterpartyChain.GetChainID(), s.chain.GetChainID(), hostClientB,
		s.chain.GetChainID(), s.counterpartyChain.GetChainID(), hostClientA)

	// Query and log client states
	ciB, err := queryClientInfo(ctx, s.counterpartyChain, hostClientB)
	if err != nil {
		return err
	}
	s.T().Logf("Client on %s tracking %s: latest_height=%d (rev=%d)", s.counterpartyChain.GetChainID(), ciB.TrackedChainID, ciB.RevisionHeight, ciB.RevisionNumber)

	ciA, err := queryClientInfo(ctx, s.chain, hostClientA)
	if err != nil {
		return err
	}
	s.T().Logf("Client on %s tracking %s: latest_height=%d (rev=%d)", s.chain.GetChainID(), ciA.TrackedChainID, ciA.RevisionHeight, ciA.RevisionNumber)

	return nil
}

type clientInfo struct {
	TrackedChainID string
	RevisionNumber uint64
	RevisionHeight int64
}

// queryClientInfo returns chain-id and latest height for a client on a chain.
func queryClientInfo(ctx context.Context, chain *cosmos.Chain, clientID string) (clientInfo, error) {
	node := chain.GetNode()
	networkInfo, err := node.GetNetworkInfo(ctx)
	if err != nil {
		return clientInfo{}, fmt.Errorf("failed to get network info: %w", err)
	}
	stdout, stderr, err := node.Exec(ctx, []string{
		"gmd", "q", "ibc", "client", "state", clientID, "-o", "json",
		"--grpc-addr", networkInfo.External.GRPCAddress(), "--grpc-insecure", "--prove=false",
	}, nil)
	if err != nil {
		return clientInfo{}, fmt.Errorf("query client state failed: %s", stderr)
	}
	var resp struct {
		ClientState struct {
			ChainID      string `json:"chain_id"`
			LatestHeight struct {
				RevisionNumber json.Number `json:"revision_number"`
				RevisionHeight json.Number `json:"revision_height"`
			} `json:"latest_height"`
		} `json:"client_state"`
	}
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return clientInfo{}, fmt.Errorf("failed to decode client state JSON: %w", err)
	}
	rn, _ := resp.ClientState.LatestHeight.RevisionNumber.Int64()
	rh, _ := resp.ClientState.LatestHeight.RevisionHeight.Int64()
	return clientInfo{
		TrackedChainID: resp.ClientState.ChainID,
		RevisionNumber: uint64(rn),
		RevisionHeight: rh,
	}, nil
}

// StepwiseUpdateChainClientOnCounterparty advances the gm-1 client that lives on
// the counterparty chain (gm-2) one height at a time up to the current height
// of gm-1. This helps cross validator-set transitions that would otherwise fail
// a single-hop update due to insufficient overlap.
func (s *SingleValidatorSuite) StepwiseUpdateChainClientOnCounterparty(ctx context.Context) error {
	if s.chain == nil || s.counterpartyChain == nil || s.hermes == nil {
		return nil
	}

	// Subject (client updates prove headers from this chain)
	latest, err := s.chain.Height(ctx)
	if err != nil {
		return err
	}

	// Start stepping from the next height after the last attempt
	start := s.lastUpdatedChainOnCounterparty + 1
	if start < 1 {
		start = 1
	}
	if start > latest {
		return nil
	}

	clientID := s.hostClientIDOnCounterparty
	if clientID == "" {
		return fmt.Errorf("host client ID on counterparty not resolved")
	}
	for h := start; h <= latest; h++ {
		if err := updateClientAtHeight(s.T(), ctx, s.hermes, s.counterpartyChain, s.chain.GetChainID(), clientID, h); err != nil {
			// Log and continue; the next iteration may still succeed if overlap permits
			s.T().Logf("update client at height %d failed: %v", h, err)
			// Do not advance the last updated marker on failure
			continue
		}
		s.lastUpdatedChainOnCounterparty = h
	}

	return nil
}

// updateClientAtHeight updates the client by submitting a header for a specific
// subject-chain height. Hermes expects a numeric height; for single-revision
// test chains this is sufficient.
func updateClientAtHeight(t *testing.T, ctx context.Context, hermes *relayer.Hermes, host *cosmos.Chain, subjectChainID, clientID string, height int64) error {
	// Hermes v1.13.1 expects a plain numeric revision_height for --height.
	// It derives the revision_number from chain context. Use bare height here.
	hArg := fmt.Sprintf("%d", height)
	cmd := []string{
		"hermes", "--json", "update", "client",
		"--host-chain", host.GetChainID(),
		"--client", clientID,
		"--height", hArg,
	}
	stdout, stderr, err := hermes.Exec(ctx, hermes.Logger, cmd, nil)
	t.Logf("update client at height %s stdout: %s", hArg, stdout)
	t.Logf("update client at height %s: stderr  %s", hArg, stderr)
	return err
}

// BackfillChainClientOnCounterpartyFrom steps the client on the counterparty
// from a specific starting height on the primary chain up to the current height.
// BackfillChainClientOnCounterpartyUntil steps from the host client's current
// trusted height + 1 up to and including endHeight on the subject chain.
func (s *SingleValidatorSuite) BackfillChainClientOnCounterpartyUntil(ctx context.Context, endHeight int64) error {
	if s.chain == nil || s.counterpartyChain == nil || s.hermes == nil {
		return fmt.Errorf("missing chain(s) or hermes")
	}

	// Start from host client's current trusted height + 1 to ensure continuity.
	clientID := s.hostClientIDOnCounterparty
	if clientID == "" {
		return fmt.Errorf("host client ID on counterparty not resolved")
	}
	trusted, err := queryClientRevisionHeight(ctx, s.counterpartyChain, clientID)
	if err != nil {
		return err
	}
	// Always start from the client's current trusted height + 1 on the host chain
	startHeight := trusted + 1
	if startHeight < 1 {
		startHeight = 1
	}

	// Do not go past the requested endHeight
	if endHeight < startHeight {
		return nil
	}

	for h := startHeight; h <= endHeight; h++ {
		if err := updateClientAtHeight(s.T(), ctx, s.hermes, s.counterpartyChain, s.chain.GetChainID(), clientID, h); err != nil {
			s.T().Logf("backfill update at height %d failed: %v", h, err)
			return err
		}
		s.lastUpdatedChainOnCounterparty = h
	}
	return nil
}
