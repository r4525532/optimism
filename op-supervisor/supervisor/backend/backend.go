package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync/atomic"
	"time"

	"github.com/ethereum-optimism/optimism/op-service/client"
	"github.com/ethereum-optimism/optimism/op-service/dial"
	"github.com/ethereum-optimism/optimism/op-supervisor/config"
	"github.com/ethereum-optimism/optimism/op-supervisor/supervisor/backend/db"
	"github.com/ethereum-optimism/optimism/op-supervisor/supervisor/backend/source"
	"github.com/ethereum-optimism/optimism/op-supervisor/supervisor/frontend"
	"github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/log"
)

type SupervisorBackend struct {
	started atomic.Bool
	logger  log.Logger

	chainMonitors []*source.ChainMonitor
	logDBs        []*db.DB
}

var _ frontend.Backend = (*SupervisorBackend)(nil)

var _ io.Closer = (*SupervisorBackend)(nil)

func NewSupervisorBackend(ctx context.Context, logger log.Logger, m Metrics, cfg *config.Config) (*SupervisorBackend, error) {
	chainMonitors := make([]*source.ChainMonitor, len(cfg.L2RPCs))
	logDBs := make([]*db.DB, len(cfg.L2RPCs))
	for i, rpc := range cfg.L2RPCs {
		rpcClient, chainID, err := createRpcClient(ctx, logger, rpc)
		if err != nil {
			return nil, err
		}
		cm := newChainMetrics(chainID, m)
		path, err := prepLogDBPath(chainID, cfg.Datadir)
		if err != nil {
			return nil, fmt.Errorf("failed to create datadir for chain %v: %w", chainID, err)
		}
		logDB, err := db.NewFromFile(logger, cm, path)
		if err != nil {
			return nil, fmt.Errorf("failed to create logdb for chain %v at %v: %w", chainID, path, err)
		}
		logDBs[i] = logDB

		// Get the last checkpoint that was written then Rewind the db
		// to the block prior to that block and start from there.
		// Guarantees we will always roll back at least one block
		// so we know we're always starting from a fully written block.
		checkPointBlock, _, err := logDB.ClosestBlockInfo(math.MaxUint64)
		if err != nil {
			return nil, fmt.Errorf("failed to get block from checkpoint: %w", err)
		}
		block := checkPointBlock - 1
		err = logDB.Rewind(block)
		if err != nil {
			return nil, fmt.Errorf("failed to 'Rewind' the database: %w", err)
		}
		monitor, err := source.NewChainMonitor(ctx, logger, cm, chainID, rpc, rpcClient, block)
		if err != nil {
			return nil, fmt.Errorf("failed to create monitor for rpc %v: %w", rpc, err)
		}
		chainMonitors[i] = monitor
	}
	return &SupervisorBackend{
		logger:        logger,
		chainMonitors: chainMonitors,
		logDBs:        logDBs,
	}, nil
}

func createRpcClient(ctx context.Context, logger log.Logger, rpc string) (client.RPC, types.ChainID, error) {
	ethClient, err := dial.DialEthClientWithTimeout(ctx, 10*time.Second, logger, rpc)
	if err != nil {
		return nil, types.ChainID{}, fmt.Errorf("failed to connect to rpc %v: %w", rpc, err)
	}
	chainID, err := ethClient.ChainID(ctx)
	if err != nil {
		return nil, types.ChainID{}, fmt.Errorf("failed to load chain id for rpc %v: %w", rpc, err)
	}
	return client.NewBaseRPCClient(ethClient.Client()), types.ChainIDFromBig(chainID), nil
}

func (su *SupervisorBackend) Start(ctx context.Context) error {
	if !su.started.CompareAndSwap(false, true) {
		return errors.New("already started")
	}
	for _, monitor := range su.chainMonitors {
		if err := monitor.Start(); err != nil {
			return fmt.Errorf("failed to start chain monitor: %w", err)
		}
	}
	return nil
}

func (su *SupervisorBackend) Stop(ctx context.Context) error {
	if !su.started.CompareAndSwap(true, false) {
		return errors.New("already stopped")
	}
	var errs error
	for _, monitor := range su.chainMonitors {
		if err := monitor.Stop(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to stop chain monitor: %w", err))
		}
	}
	for _, logDB := range su.logDBs {
		if err := logDB.Close(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to close logdb: %w", err))
		}
	}
	return errs
}

func (su *SupervisorBackend) Close() error {
	// TODO(protocol-quest#288): close logdb of all chains
	return nil
}

func (su *SupervisorBackend) CheckMessage(identifier types.Identifier, payloadHash common.Hash) (types.SafetyLevel, error) {
	// TODO(protocol-quest#288): hook up to logdb lookup
	return types.CrossUnsafe, nil
}

func (su *SupervisorBackend) CheckBlock(chainID *hexutil.U256, blockHash common.Hash, blockNumber hexutil.Uint64) (types.SafetyLevel, error) {
	// TODO(protocol-quest#288): hook up to logdb lookup
	return types.CrossUnsafe, nil
}
