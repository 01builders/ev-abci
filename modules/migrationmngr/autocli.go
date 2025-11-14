package migrationmngr

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/evstack/ev-abci/modules/migrationmngr/types"
)

// AutoCLIOptions implements the autocli.HasAutoCLIConfig interface.
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: types.Query_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "IsMigrating",
					Use:       "is_migrating",
					Short:     "Shows whether a migration is in progress",
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service: types.Msg_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod:   "Migrate",
					GovProposal: true,
				},
			},
		},
	}
}
