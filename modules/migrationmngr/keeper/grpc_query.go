package keeper

import (
	"context"

	"github.com/evstack/ev-abci/modules/migrationmngr/types"
)

var _ types.QueryServer = queryServer{}

type queryServer struct {
	Keeper
}

// NewQueryServer creates a new instance of the query server.
func NewQueryServer(keeper Keeper) types.QueryServer {
	return queryServer{Keeper: keeper}
}

// IsMigrating checks if a migration is in progress.
func (q queryServer) IsMigrating(ctx context.Context, _ *types.QueryIsMigratingRequest) (*types.QueryIsMigratingResponse, error) {
	start, end, isMigrating := q.Keeper.IsMigrating(ctx)

	return &types.QueryIsMigratingResponse{
		IsMigrating:      isMigrating,
		StartBlockHeight: start,
		EndBlockHeight:   end,
	}, nil
}
