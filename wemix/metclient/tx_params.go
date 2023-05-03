// tx_params.go

package metclient

import (
	"context"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

// account nonce cache map. Uses a global lock for now. If necessary,
// need to use sync.Map instead.
// Also we might have to maintain window ala tcp window to accommodate nonces
// used in failed transactions.
var (
	txParamsCache = &sync.Map{}

	// for base fee checking
	baseFeeCheckTime     time.Time     = time.Time{}
	baseFeeCheckDuration time.Duration = 1 * time.Second
	baseFeeCheckCounter  int64

	// hash lock for nonce check
	nonceLocks []*sync.Mutex
)

func init() {
	count := 1001
	nonceLocks = make([]*sync.Mutex, count)
	for i := 0; i < count; i++ {
		nonceLocks[i] = &sync.Mutex{}
	}
}

func GetOpportunisticTxParamsLegacy(ctx context.Context, cli *ethclient.Client, addr common.Address, refresh bool, incNonce bool) (chainId, gasPrice, nonce *big.Int, err error) {
	lck := nonceLocks[new(big.Int).SetBytes(addr.Bytes()).Uint64()%uint64(cap(nonceLocks))]
	_, n_ok := txParamsCache.Load(addr)
	cid, cid_ok := txParamsCache.Load("chain-id")
	gp, gp_ok := txParamsCache.Load("gas-price")

	if !refresh && n_ok && cid_ok && gp_ok {
		// cache's good
		chainId = new(big.Int).Set(cid.(*big.Int))
		gasPrice = new(big.Int).Set(gp.(*big.Int))
		lck.Lock()
		n, _ := txParamsCache.Load(addr)
		nonce = new(big.Int).Set(n.(*big.Int))
		if incNonce {
			txParamsCache.Store(addr, new(big.Int).Add(nonce, common.Big1))
		}
		lck.Unlock()
		return
	}

	if !refresh && cid_ok {
		chainId = new(big.Int).Set(cid.(*big.Int))
	} else {
		cid, err = cli.NetworkID(ctx)
		if err != nil {
			return
		}
		txParamsCache.Store("chain-id", cid)
		chainId = new(big.Int).Set(cid.(*big.Int))
	}
	if !refresh && gp_ok {
		gasPrice = new(big.Int).Set(gp.(*big.Int))
	} else {
		gp, err = cli.SuggestGasPrice(ctx)
		if err != nil {
			return
		}
		txParamsCache.Store("gas-price", gp)
		gasPrice = new(big.Int).Set(gp.(*big.Int))
	}
	lck.Lock()
	n, n_ok := txParamsCache.Load(addr)
	if n_ok {
		nonce = new(big.Int).Set(n.(*big.Int))
		if incNonce {
			txParamsCache.Store(addr, new(big.Int).Add(nonce, common.Big1))
		}
	} else {
		var n1, n2 uint64
		n1, err = cli.PendingNonceAt(ctx, addr)
		if err != nil {
			return
		}
		n2, err = cli.NonceAt(ctx, addr, nil)
		if err != nil {
			return
		}
		if n1 < n2 {
			n1 = n2
		}

		nonce = big.NewInt(int64(n1))
		if !incNonce {
			txParamsCache.Store(addr, nonce)
		} else {
			txParamsCache.Store(addr, new(big.Int).Add(nonce, common.Big1))
		}
	}
	lck.Unlock()

	return
}

func GetOpportunisticTxParamsDynamicFee(ctx context.Context, cli *ethclient.Client, addr common.Address, refresh bool, incNonce bool) (chainId, maxTip, baseFee, nonce *big.Int, tenthMultiple float64, err error) {
	if time.Since(baseFeeCheckTime) > baseFeeCheckDuration {
		if cnt := atomic.AddInt64(&baseFeeCheckCounter, 1); cnt == 1 {
			refresh = true
		}
		defer func() {
			atomic.AddInt64(&baseFeeCheckCounter, -1)
		}()
	}

	lck := nonceLocks[new(big.Int).SetBytes(addr.Bytes()).Uint64()%uint64(cap(nonceLocks))]
	_, n_ok := txParamsCache.Load(addr)
	cid, cid_ok := txParamsCache.Load("chain-id")
	tip, tip_ok := txParamsCache.Load("max-tip")
	basefee, basefee_ok := txParamsCache.Load("base-fee")
	mf10, mf10_ok := txParamsCache.Load("base-fee-10th-factor")
	if !basefee_ok || !mf10_ok {
		refresh = true
	}

	if !refresh && n_ok && cid_ok && tip_ok && basefee_ok && mf10_ok {
		// cache's good
		chainId = new(big.Int).Set(cid.(*big.Int))
		maxTip = new(big.Int).Set(tip.(*big.Int))
		baseFee = new(big.Int).Set(basefee.(*big.Int))
		tenthMultiple = mf10.(float64)
		lck.Lock()
		n, _ := txParamsCache.Load(addr)
		nonce = new(big.Int).Set(n.(*big.Int))
		if incNonce {
			txParamsCache.Store(addr, new(big.Int).Add(nonce, common.Big1))
		}
		lck.Unlock()
		return
	}

	if !refresh && cid_ok {
		chainId = new(big.Int).Set(cid.(*big.Int))
	} else {
		cid, err = cli.ChainID(ctx)
		if err != nil {
			return
		}
		txParamsCache.Store("chain-id", cid)
		chainId = new(big.Int).Set(cid.(*big.Int))
	}
	if !refresh && tip_ok {
		maxTip = new(big.Int).Set(tip.(*big.Int))
	} else {
		tip, err = cli.SuggestGasTipCap(ctx)
		if err != nil {
			return
		}
		txParamsCache.Store("max-tip", tip)
		maxTip = new(big.Int).Set(tip.(*big.Int))
	}
	if !refresh && basefee_ok {
		baseFee = new(big.Int).Set(basefee.(*big.Int))
		tenthMultiple = mf10.(float64)
	} else {
		var head *types.Header
		head, err = cli.HeaderByNumber(ctx, nil)
		if err != nil {
			return
		}
		txParamsCache.Store("base-fee", head.BaseFee)
		baseFee = new(big.Int).Set(head.BaseFee)

		n_10 := head.Number.Int64() - 10
		if n_10 < 0 {
			mf10 = 1.0
		} else {
			var h_10 *types.Header
			h_10, err = cli.HeaderByNumber(ctx, new(big.Int).SetInt64(n_10))
			if err != nil {
				mf10 = 1.0
			} else {
				f := new(big.Float).SetInt(head.BaseFee)
				f.Quo(f, new(big.Float).SetInt(h_10.BaseFee))
				mf10, _ = f.Float64()
				if mf10.(float64) > 100.0 {
					mf10 = 100.0
				}
			}
		}
		txParamsCache.Store("base-fee-10th-factor", mf10)
		tenthMultiple = mf10.(float64)

		baseFeeCheckTime = time.Now()
	}
	lck.Lock()
	n, n_ok := txParamsCache.Load(addr)
	if n_ok {
		nonce = new(big.Int).Set(n.(*big.Int))
		if incNonce {
			txParamsCache.Store(addr, new(big.Int).Add(nonce, common.Big1))
		}
	} else {
		var n1, n2 uint64
		n1, err = cli.PendingNonceAt(ctx, addr)
		if err != nil {
			return
		}
		n2, err = cli.NonceAt(ctx, addr, nil)
		if err != nil {
			return
		}
		if n1 < n2 {
			n1 = n2
		}

		nonce = big.NewInt(int64(n1))
		if !incNonce {
			txParamsCache.Store(addr, nonce)
		} else {
			txParamsCache.Store(addr, new(big.Int).Add(nonce, common.Big1))
		}
	}
	lck.Unlock()

	return
}

func GetOpportunisticTxParams(ctx context.Context, cli *ethclient.Client, addr common.Address, refresh, incNonce, dynamicFee bool) (chainId, maxTip, baseFee, gasPrice, nonce *big.Int, tenthMultiple float64, err error) {
	if dynamicFee {
		chainId, maxTip, baseFee, nonce, tenthMultiple, err = GetOpportunisticTxParamsDynamicFee(ctx, cli, addr, refresh, incNonce)
	} else {
		chainId, gasPrice, nonce, err = GetOpportunisticTxParamsLegacy(ctx, cli, addr, refresh, incNonce)
	}
	return
}

// EOF
