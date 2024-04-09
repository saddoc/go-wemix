// cmet.go

package main

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/wemix/metclient"
	"golang.org/x/time/rate"
)

var (
	nilAddress = common.Address{}
	DefaultGas = int(1000000)
	DefaultUrl = "http://localhost:8588"
)

// load the first contract in the given file
func loadContract(fn string) (*metclient.ContractData, error) {
	f, err := os.Open(fn)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	contract, err := metclient.LoadJsonContract(f)
	if err == nil {
		return contract, nil
	}

	f.Seek(0, 0)
	contracts, err := metclient.LoadJsContract(f)
	if err != nil {
		return nil, err
	} else {
		for _, i := range contracts {
			return i, nil
		}
	}
	return nil, fmt.Errorf("No contracts")
}

func getContract(cli *ethclient.Client, from *keystore.Key, to common.Address, gas int, fn string) (*metclient.RemoteContract, error) {
	contractData, err := loadContract(fn)
	if err != nil {
		return nil, err
	}
	if to == nilAddress {
		return nil, fmt.Errorf("Invalid Contract Address")
	}
	return &metclient.RemoteContract{
		Cli:  cli,
		From: from,
		To:   &to,
		Abi:  contractData.Abi,
		Gas:  gas,
	}, nil
}

func kvCount(ctr *metclient.RemoteContract) (count int, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var _count *big.Int
	err = metclient.CallContract(ctx, ctr, "count", nil, &_count, nil)
	if err != nil {
		return
	}

	return int(_count.Int64()), nil
}

func kvPut(ctr *metclient.RemoteContract, key, value []byte, async bool) (hash common.Hash, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	args := []interface{}{key, value, false}
	hash, err = metclient.SendContract(ctx, ctr, "put", args)
	if err != nil {
		return
	} else if async {
		return
	}

	var receipt *types.Receipt
	receipt, err = metclient.GetReceipt(ctx, ctr.Cli, hash, 500, 10000)
	if err != nil {
		return hash, err
	} else if receipt.Status == 1 {
		return hash, nil
	} else {
		return hash, fmt.Errorf("Execution status %d\n", receipt.Status)
	}
}

func kvMput(ctr *metclient.RemoteContract, kvs [][]byte, async bool) (hash common.Hash, err error) {
	var bb bytes.Buffer
	for _, i := range kvs {
		bb.Write(metclient.PackNum(reflect.ValueOf(len(i))))
		bb.Write(i)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	args := []interface{}{bb.Bytes(), false}
	hash, err = metclient.SendContract(ctx, ctr, "mput", args)
	if err != nil {
		return hash, err
	} else if async {
		return hash, nil
	}

	var receipt *types.Receipt
	receipt, err = metclient.GetReceipt(ctx, ctr.Cli, hash, 500, 10000)
	if err != nil {
		return hash, err
	} else if receipt.Status == 1 {
		return hash, nil
	} else {
		return hash, fmt.Errorf("Execution status %d\n", receipt.Status)
	}
}

func kvGet(ctr *metclient.RemoteContract, key []byte) (value []byte, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = metclient.CallContract(ctx, ctr, "get", []interface{}{key}, &value, nil)
	if err != nil {
		return
	}
	return
}

func BulkFunc(numThreads int, silent bool, producer func() func(int) int) {
	t := time.Now().UnixNano()
	n := int64(0)
	jobs := make(chan func(int) int)

	var wg sync.WaitGroup
	for i := 0; i < numThreads; i++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for {
				f := <-jobs
				if f == nil {
					break
				} else {
					nn := f(gid)
					atomic.AddInt64(&n, int64(nn))
				}
			}
		}(i)
	}

	for {
		j := producer()
		if j != nil {
			jobs <- j
		} else {
			for k := 0; k < numThreads; k++ {
				jobs <- nil
			}
			break
		}
	}

	wg.Wait()

	if !silent {
		t = (time.Now().UnixNano() - t) / 1000000 // to milliseconds
		if n == 0 {
			fmt.Printf("Took %d / 0 = Infinity tps\n", n)
		} else {
			fmt.Printf("Took %d / %.3f = %.3f tps\n", n, float64(t)/1000.0,
				float64(n)/(float64(t)/1000.0))
		}
	}
}

func bulkSend(numThreads int, reqUrl, keysFile string, loop, count int, amount, froms, tos string, randomTo bool, rateLimit int, dynamicFee bool) {
	clis := make([]*ethclient.Client, numThreads)
	for i := 0; i < numThreads; i += 1 {
		cli, err := ethclient.Dial(reqUrl)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		clis[i] = cli
	}

	kr, err := OpenNamedKeys(keysFile, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "canot open keys file %s: %v\n", keysFile, err)
		os.Exit(1)
	}

	var fromInfos []*AcctInfo
	var toAddrs []common.Address

	toInt := func(s string, d int) int {
		if v, e := strconv.Atoi(s); e == nil {
			return v
		}
		return d
	}

	// from accounts
	func() {
		fmt.Printf("loading from accounts: ")
		ls := strings.Split(froms, ",")
		if len(ls) == 3 && toInt(ls[1], -1) >= 0 && toInt(ls[2], -1) > 0 {
			start := toInt(ls[1], -1)
			count := toInt(ls[2], -1)
			fromInfos = make([]*AcctInfo, count)

			ix := int64(start)
			BulkFunc(runtime.NumCPU()*2, false, func() func(int) int {
				jx := atomic.AddInt64(&ix, 1) - 1
				if jx >= int64(start+count) {
					return nil
				}
				return func(gid int) int {
					nx := fmt.Sprintf("%s%d", ls[0], jx)
					info, err := kr.get(nx, common.Address{})
					if err != nil {
						panic(fmt.Sprintf("neither address nor name: %s %v", nx, err))
					}
					fromInfos[int(jx)-start] = info
					return 1
				}
			})
		} else {
			fromInfos = make([]*AcctInfo, len(ls))
			for i, j := range ls {
				var addr common.Address
				if common.IsHexAddress(j) {
					addr = common.HexToAddress(j)
				}
				fromInfo, err := kr.get(j, addr)
				if err != nil {
					panic(fmt.Sprintf("neither address nor name: %s", j))
				}
				fromInfos[i] = fromInfo
			}
			fmt.Println("done.")
		}
	}()

	// to addresses
	func() {
		fmt.Print("loading to addresses: ")
		ls := strings.Split(tos, ",")
		if len(ls) == 3 && toInt(ls[1], -1) >= 0 && toInt(ls[2], -1) > 0 {
			start := toInt(ls[1], -1)
			count := toInt(ls[2], -1)
			toAddrs = make([]common.Address, count)

			ix := int64(start)
			BulkFunc(runtime.NumCPU()*2, false, func() func(int) int {
				jx := atomic.AddInt64(&ix, 1) - 1
				if jx >= int64(start+count) {
					return nil
				}
				return func(gid int) int {
					nx := fmt.Sprintf("%s%d", ls[0], jx)
					if toAddr, err := kr.Name2Address(nx); err == nil {
						toAddrs[int(jx)-start] = toAddr
					} else {
						panic(fmt.Sprintf("neither address nor name: %s", nx))
					}
					return 1
				}
			})
		} else {
			toAddrs = make([]common.Address, len(ls))
			for i, j := range ls {
				if common.IsHexAddress(j) {
					toAddrs[i] = common.HexToAddress(j)
				} else if toAddr, err := kr.Name2Address(j); err == nil {
					toAddrs[i] = toAddr
				} else {
					panic(fmt.Sprintf("neither address nor name: %s", j))
				}
			}
			fmt.Println("done.")
		}
	}()

	if len(fromInfos) == 0 {
		fmt.Fprintf(os.Stderr, "no from accounts")
		return
	}
	if len(toAddrs) == 0 {
		fmt.Fprintf(os.Stderr, "no to addresses")
		return
	}

	kr.Seal()

	sendValue := func(cli *ethclient.Client, from *keystore.Key, to common.Address, amount string) (common.Hash, error) {
		retryCount := 150
		retryInterval := 200
		var lastErr error

		amt, ok := new(big.Int).SetString(amount, 10)
		if !ok {
			return common.Hash{}, fmt.Errorf("invalid amount")
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		for try := 0; try < retryCount; try++ {
			tx, err := metclient.SendTransaction(ctx, cli, from, &to, amt, nil,
				DefaultGas, dynamicFee)
			if err == nil {
				if lastErr != nil {
					fmt.Fprintf(os.Stderr, "'%v' cleared\n", lastErr)
				}
				return tx, nil
			} else {
				if lastErr == nil || lastErr.Error() != err.Error() {
					les := ""
					if lastErr != nil {
						les = lastErr.Error()
					}
					fmt.Fprintf(os.Stderr, "Got '%v' (was '%v')\n", err, les)
				}
				lastErr = err
				time.Sleep(time.Duration(retryInterval) * time.Millisecond)
				continue
			}
		}
		return common.Hash{}, fmt.Errorf("timed out")
	}

	var rateLimiter *rate.Limiter
	if rateLimit > 0 {
		rateLimiter = rate.NewLimiter(rate.Limit(rateLimit), rateLimit)
	}

	for lix := 0; lix < loop; lix++ {
		ix := int64(0)
		BulkFunc(numThreads, false, func() func(int) int {
			jx := atomic.AddInt64(&ix, 1) - 1
			if jx >= int64(count) {
				return nil
			}

			fromIx := int(jx) % len(fromInfos)
			var toIx int
			if !randomTo {
				toIx = int(jx) % len(toAddrs)
			} else {
				toIx = rand.Intn(len(toAddrs))
			}

			return func(gid int) int {
				from := fromInfos[fromIx]
				to := toAddrs[toIx]

				if rateLimiter != nil {
					rateLimiter.Wait(context.Background())
				}

				// fmt.Printf("%s -> %s\n", from.Name, to.Hex())
				cli := clis[fromIx%numThreads]
				tx, err := sendValue(cli, &from.Key, to, amount)
				if err != nil {
					fmt.Fprintf(os.Stderr, "failed to send %s -> %s: %v\n",
						from.Name, to.Hex(), err)
					return 0
				}

				if jx == int64(count)-1 {
					fmt.Printf("Checking the receipt for the last tx %s\n", tx.Hex())
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					receipt, err := metclient.GetReceipt(ctx, cli, tx, 500, 10000)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Failed to get receipt for %s: %s\n", tx.Hex(), err)
					} else if receipt.Status != 1 {
						fmt.Fprintf(os.Stderr, "Failed to send: status = %d\n",
							receipt.Status)
					} else {
						fmt.Printf("Hash %s\n", tx.String())
					}
				}

				return 1
			}
		})
	}
}

var multiSenderAbiString = `[
	{
		"name": "multiSend",
		"inputs":[
			{ "internalType": "address[]", "name": "_tos",     "type": "address[]" },
			{ "internalType": "uint256[]", "name": "_amounts", "type": "uint256[]" },
			{ "internalType": "uint256",   "name": "_count",   "type": "uint256" }
		],
		"outputs": [],
		"stateMutability": "payable",
		"type": "function"
	}
]`

func bulkCall(numThreads int, reqUrl, keysFile string, contractAddress *common.Address, loop, count int, froms, tos string, iCount int, amount string, randomTo bool, rateLimit int, dynamicFee bool) {
	clis := make([]*ethclient.Client, numThreads)
	for i := 0; i < numThreads; i += 1 {
		cli, err := ethclient.Dial(reqUrl)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		clis[i] = cli
	}
	multiSenderAbi, err := abi.JSON(bytes.NewReader([]byte(multiSenderAbiString)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid abi: %v\n", err)
		os.Exit(1)
	}

	kr, err := OpenNamedKeys(keysFile, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "canot open keys file %s: %v\n", keysFile, err)
		os.Exit(1)
	}

	var fromInfos []*AcctInfo
	var toAddrs []common.Address

	toInt := func(s string, d int) int {
		if v, e := strconv.Atoi(s); e == nil {
			return v
		}
		return d
	}

	// from accounts
	func() {
		fmt.Printf("loading from accounts: ")
		ls := strings.Split(froms, ",")
		if len(ls) == 3 && toInt(ls[1], -1) >= 0 && toInt(ls[2], -1) > 0 {
			start := toInt(ls[1], -1)
			count := toInt(ls[2], -1)
			fromInfos = make([]*AcctInfo, count)

			ix := int64(start)
			BulkFunc(runtime.NumCPU()*2, false, func() func(int) int {
				jx := atomic.AddInt64(&ix, 1) - 1
				if jx >= int64(start+count) {
					return nil
				}
				return func(gid int) int {
					nx := fmt.Sprintf("%s%d", ls[0], jx)
					info, err := kr.get(nx, common.Address{})
					if err != nil {
						panic(fmt.Sprintf("neither address nor name: %s %v", nx, err))
					}
					fromInfos[int(jx)-start] = info
					return 1
				}
			})
		} else {
			fromInfos = make([]*AcctInfo, len(ls))
			for i, j := range ls {
				var addr common.Address
				if common.IsHexAddress(j) {
					addr = common.HexToAddress(j)
				}
				fromInfo, err := kr.get(j, addr)
				if err != nil {
					panic(fmt.Sprintf("neither address nor name: %s", j))
				}
				fromInfos[i] = fromInfo
			}
			fmt.Println("done.")
		}
	}()

	// to addresses
	func() {
		fmt.Print("loading to addresses: ")
		ls := strings.Split(tos, ",")
		if len(ls) == 3 && toInt(ls[1], -1) >= 0 && toInt(ls[2], -1) > 0 {
			start := toInt(ls[1], -1)
			count := toInt(ls[2], -1)
			toAddrs = make([]common.Address, count)

			ix := int64(start)
			BulkFunc(runtime.NumCPU()*2, false, func() func(int) int {
				jx := atomic.AddInt64(&ix, 1) - 1
				if jx >= int64(start+count) {
					return nil
				}
				return func(gid int) int {
					nx := fmt.Sprintf("%s%d", ls[0], jx)
					if toAddr, err := kr.Name2Address(nx); err == nil {
						toAddrs[int(jx)-start] = toAddr
					} else {
						panic(fmt.Sprintf("neither address nor name: %s", nx))
					}
					return 1
				}
			})
		} else {
			toAddrs = make([]common.Address, len(ls))
			for i, j := range ls {
				if common.IsHexAddress(j) {
					toAddrs[i] = common.HexToAddress(j)
				} else if toAddr, err := kr.Name2Address(j); err == nil {
					toAddrs[i] = toAddr
				} else {
					panic(fmt.Sprintf("neither address nor name: %s", j))
				}
			}
			fmt.Println("done.")
		}
	}()

	if len(fromInfos) == 0 {
		fmt.Fprintf(os.Stderr, "no from accounts")
		return
	}
	if len(toAddrs) == 0 {
		fmt.Fprintf(os.Stderr, "no to addresses")
		return
	}

	kr.Seal()

	send := func(cli *ethclient.Client, from *keystore.Key, tos []*common.Address, amount string) (common.Hash, error) {
		retryCount := 150
		retryInterval := 200
		var lastErr error

		amt, ok := new(big.Int).SetString(amount, 10)
		if !ok {
			return common.Hash{}, fmt.Errorf("invalid amount")
		}
		_tos := make([]common.Address, len(tos))
		amounts := make([]*big.Int, len(tos))
		for i := range amounts {
			_tos[i] = *tos[i]
			amounts[i] = amt
		}
		value := new(big.Int).Mul(amt, big.NewInt(int64(len(tos))))
		data, err := multiSenderAbi.Pack("multiSend", _tos, amounts, big.NewInt(1))
		if err != nil {
			return common.Hash{}, err
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		for try := 0; try < retryCount; try++ {
			tx, err := metclient.SendTransaction(ctx, cli, from, contractAddress, value, data,
				DefaultGas, dynamicFee)
			if err == nil {
				if lastErr != nil {
					fmt.Fprintf(os.Stderr, "'%v' cleared\n", lastErr)
				}
				return tx, nil
			} else {
				if lastErr == nil || lastErr.Error() != err.Error() {
					les := ""
					if lastErr != nil {
						les = lastErr.Error()
					}
					fmt.Fprintf(os.Stderr, "Got '%v' (was '%v')\n", err, les)
				}
				lastErr = err
				time.Sleep(time.Duration(retryInterval) * time.Millisecond)
				continue
			}
		}
		return common.Hash{}, fmt.Errorf("timed out")
	}

	var rateLimiter *rate.Limiter
	if rateLimit > 0 {
		rateLimiter = rate.NewLimiter(rate.Limit(rateLimit), rateLimit)
	}

	for lix := 0; lix < loop; lix++ {
		ix := int64(0)
		BulkFunc(numThreads, false, func() func(int) int {
			jx := atomic.AddInt64(&ix, 1) - 1
			if jx >= int64(count) {
				return nil
			}

			_tos := make([]*common.Address, iCount)
			fromIx := int(jx) % len(fromInfos)
			for i := 0; i < iCount; i++ {
				if !randomTo {
					_tos[i] = &toAddrs[(int(jx)+i)%len(toAddrs)]
				} else {
					_tos[i] = &toAddrs[rand.Intn(len(toAddrs))]
				}
			}

			return func(gid int) int {
				from := fromInfos[fromIx]

				if rateLimiter != nil {
					rateLimiter.Wait(context.Background())
				}

				// fmt.Printf("%s -> %s\n", from.Name, to.Hex())
				cli := clis[fromIx%numThreads]
				tx, err := send(cli, &from.Key, _tos, amount)
				if err != nil {
					fmt.Fprintf(os.Stderr, "failed to send %s -> %s: %v\n",
						from.Name, contractAddress.Hex(), err)
					return 0
				}

				if jx == int64(count)-1 {
					fmt.Printf("Checking the receipt for the last tx %s\n", tx.Hex())
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					receipt, err := metclient.GetReceipt(ctx, cli, tx, 500, 10000)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Failed to get receipt for %s: %s\n", tx.Hex(), err)
					} else if receipt.Status != 1 {
						fmt.Fprintf(os.Stderr, "Failed to send: status = %d\n",
							receipt.Status)
					} else {
						fmt.Printf("Hash %s\n", tx.String())
					}
				}

				return 1
			}
		})
	}
}

func usage() {
	fmt.Fprintf(os.Stderr,
		`Usage: cmet [options...] [deploy <contract.(js|.json)>+ |
    send <to> <amount> |
    kv-count | kv-put <key> <value> | kv-get <key> <value> |
    bulk-kv-put <prefix> <start> <end> [<batch>] |
    bulk-kv-get <prefix> <start> <end> |
    create-accounts <prefix> <start> <count> <file-name> |
    bulk-send <loop> <count> <value> <froms> <tos>]
    bulk-call <loop> <count> <froms> <tos> <i-count> <amount>]

options:
-a <password> <account-file>: an ethereum account file and password (CMET_ACCOUNT)
	-:	read from stdin
	@<file-name>:	password is in <file-name> file
-c <contract-address>:	if not specified, env. var. CMET_CONTRACT.
-d:         use dyanmic base fee (eip1559)
-g <gas>:	gas amount (CMET_GAS)
-p <gas-price>: gas price
-i <abi>:	ABI in .json or .js file, if not specified, env. var. CMET_ABI.
-l <rate>:	rate limiting, txs per second.
-s <url>:	gmet url. CMET_URL.
-t <count>:	number of workers
-k <file>:  file that contains keys. CMET_KEYS.
-r:         randomized to for bulk-send.
-q:	silent
`)
}

func main() {
	var (
		account                      *keystore.Key
		accountPassword, accountFile string
		silent                       bool = false
		reqUrl                       string
		numThreads                   int = 1
		ctr                          *metclient.RemoteContract
		contractAddress              common.Address
		abiFile                      string
		gas, gasPrice                int = 0, 0
		keysFile                     string
		randomTo                     bool = false
		rateLimit                    int  = 0
		dynamicFee                   bool = false
		err                          error
	)

	var nargs []string
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "-h":
			usage()
			os.Exit(1)
		case "-q":
			silent = true
		case "-a":
			if i >= len(os.Args)-2 {
				usage()
				os.Exit(1)
			}
			accountPassword = os.Args[i+1]
			accountFile = os.Args[i+2]
			i += 2
		case "-c":
			if i >= len(os.Args)-1 {
				usage()
				os.Exit(1)
			}
			if !common.IsHexAddress(os.Args[i+1]) {
				usage()
				fmt.Fprintln(os.Stderr, "Invalid contract address")
				os.Exit(1)
			}
			contractAddress = common.HexToAddress(os.Args[i+1])
			i++
		case "-d":
			dynamicFee = true
		case "-s":
			if i >= len(os.Args)-1 {
				usage()
				os.Exit(1)
			}
			reqUrl = os.Args[i+1]
			i++
		case "-i":
			if i >= len(os.Args)-1 {
				usage()
				os.Exit(1)
			}
			abiFile = os.Args[i+1]
			i++
		case "-g":
			fallthrough
		case "-l":
			fallthrough
		case "-p":
			fallthrough
		case "-t":
			if i >= len(os.Args)-1 {
				usage()
				os.Exit(1)
			}
			if v, e := strconv.Atoi(os.Args[i+1]); e != nil {
				usage()
				os.Exit(1)
			} else {
				switch os.Args[i] {
				case "-g":
					gas = v
				case "-l":
					rateLimit = v
				case "-p":
					gasPrice = v
				case "-t":
					numThreads = v
				}
			}
			i++
		case "-k":
			if i >= len(os.Args)-1 {
				usage()
				os.Exit(1)
			}
			keysFile = os.Args[i+1]
			i++
		case "-r":
			randomTo = true
		default:
			nargs = append(nargs, os.Args[i])
		}
	}

	if len(nargs) < 1 {
		usage()
		os.Exit(1)
	}

	// if not specified, use environment varibles for the following
	if len(accountPassword) == 0 && len(accountFile) == 0 {
		if v := os.Getenv("CMET_ACCOUNT"); len(v) > 0 {
			if w := strings.Fields(v); len(w) == 2 {
				accountPassword, accountFile = w[0], w[1]
			}
		}
	}
	if len(abiFile) == 0 {
		if v := os.Getenv("CMET_ABI"); len(v) > 0 {
			abiFile = v
		}
	}
	if len(reqUrl) == 0 {
		if v := os.Getenv("CMET_URL"); len(v) > 0 {
			reqUrl = v
		}
		if len(reqUrl) == 0 {
			reqUrl = DefaultUrl
		}
	}
	if contractAddress == nilAddress {
		if v := os.Getenv("CMET_CONTRACT"); len(v) > 0 && common.IsHexAddress(v) {
			contractAddress = common.HexToAddress(v)
		}
	}
	if gas == 0 {
		if v := os.Getenv("CMET_GAS"); len(v) > 0 {
			if gas, err = strconv.Atoi(v); err != nil {
				fmt.Fprintf(os.Stderr, "Invalid gas value: %s\n", v)
				return
			}
		}
		if gas == 0 {
			gas = DefaultGas
		}
	}

	switch nargs[0] {
	case "deploy":
		if len(nargs) < 2 {
			usage()
			return
		}

		account, err = metclient.LoadAccount(accountPassword, accountFile)
		if err != nil {
			usage()
			fmt.Fprintln(os.Stderr, "Failed to load account:", err)
			os.Exit(1)
		}

		var cli *ethclient.Client
		cli, err = ethclient.Dial(reqUrl)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		for j := 1; j < len(nargs); j++ {
			var hash common.Hash
			var contractData *metclient.ContractData
			var receipt *types.Receipt

			contractData, err = loadContract(nargs[j])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Cannot load a contract from %s: %s\n", nargs[j], err)
				continue
			}

			ctx, cancel := context.WithCancel(context.Background())
			hash, err = metclient.Deploy(ctx, cli, account, contractData, nil,
				gas, gasPrice)
			if err != nil {
				cancel()
				fmt.Fprintf(os.Stderr, "Deploying %s failed: %s\n", nargs[j], err)
				continue
			}

			receipt, err = metclient.GetContractReceipt(ctx, cli, hash, 500, 10000)
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Contract failed: %s\n", err)
			} else {
				if receipt.Status == 1 {
					fmt.Printf("Contract mined! ")
				} else {
					fmt.Printf("Contract failed with %d! ", receipt.Status)
				}
				fmt.Printf("address: %s transactionHash: %s\n",
					receipt.ContractAddress.String(), hash.String())
			}
		}

	case "send":
		if len(nargs) != 3 {
			usage()
			return
		}

		if !common.IsHexAddress(nargs[1]) {
			fmt.Println("Invalid address", err)
			os.Exit(1)
		}
		to := common.HexToAddress(nargs[1])

		amount, ok := new(big.Int).SetString(nargs[2], 10)
		if !ok {
			fmt.Println("invalid amount")
			os.Exit(1)
		}

		account, err = metclient.LoadAccount(accountPassword, accountFile)
		if err != nil {
			usage()
			fmt.Println("Failed to load account:", err)
			os.Exit(1)
		}

		var cli *ethclient.Client
		cli, err = ethclient.Dial(reqUrl)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		var tx common.Hash
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tx, err = metclient.SendTransaction(ctx, cli, account, &to, amount, nil,
			gas, dynamicFee)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Sending to %s failed: %s\n", nargs[1], err)
			os.Exit(1)
		}

		var receipt *types.Receipt
		receipt, err = metclient.GetReceipt(ctx, cli, tx, 500, 10000)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to send to %s: %s\n", to.String(), err)
		} else if receipt.Status != 1 {
			fmt.Fprintf(os.Stderr, "Failed to send to %s: status = %d\n",
				nargs[1], receipt.Status)
		} else {
			fmt.Printf("Hash %s\n", tx.String())
		}

	case "kv-count":
		if len(nargs) < 1 {
			usage()
			return
		}

		var cli *ethclient.Client
		cli, err = ethclient.Dial(reqUrl)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		ctr, err = getContract(cli, nil, contractAddress, gas, abiFile)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		var count int
		count, err = kvCount(ctr)
		if err == nil {
			fmt.Println(count)
		} else {
			fmt.Println(err)
			os.Exit(1)
		}

	case "kv-put":
		if len(nargs) < 3 {
			usage()
			return
		}

		account, err = metclient.LoadAccount(accountPassword, accountFile)
		if err != nil {
			usage()
			fmt.Println("Failed to load account:", err)
			os.Exit(1)
		}

		var cli *ethclient.Client
		cli, err = ethclient.Dial(reqUrl)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		ctr, err = getContract(cli, account, contractAddress, gas, abiFile)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		var tx common.Hash
		tx, err = kvPut(ctr, []byte(nargs[1]), []byte(nargs[2]), false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to put data: %s\n", err)
		} else {
			fmt.Printf("Hash %s\n", tx.String())
		}

	case "kv-get":
		if len(nargs) < 2 {
			usage()
			return
		}

		var cli *ethclient.Client
		cli, err = ethclient.Dial(reqUrl)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		ctr, err = getContract(cli, nil, contractAddress, gas, abiFile)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		var value []byte
		value, err = kvGet(ctr, []byte(nargs[1]))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get: %s\n", err)
		} else {
			fmt.Println(string(value))
		}

	case "bulk-kv-put":
		if len(nargs) < 4 {
			usage()
			return
		}

		account, err = metclient.LoadAccount(accountPassword, accountFile)
		if err != nil {
			usage()
			fmt.Println("Failed to load account:", err)
			os.Exit(1)
		}

		var cli *ethclient.Client
		cli, err = ethclient.Dial(reqUrl)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		ctr, err = getContract(cli, account, contractAddress, gas, abiFile)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		lock := sync.Mutex{}
		prefix := nargs[1]
		start, e1 := strconv.Atoi(nargs[2])
		end, e2 := strconv.Atoi(nargs[3])
		if e1 != nil || e2 != nil {
			usage()
			return
		}
		per := 1
		if len(nargs) > 4 {
			var e3 error
			if per, e3 = strconv.Atoi(nargs[4]); e3 != nil {
				usage()
				return
			}
		}

		i := start
		BulkFunc(numThreads, false, func() func(int) int {
			lock.Lock()
			si := i
			ei := si + per - 1
			if ei > end {
				ei = end
			}
			i += per
			lock.Unlock()

			if si > end {
				return nil
			}

			var data [][]byte
			if per != 1 {
				for ix := si; ix <= ei; ix++ {
					x := []byte(fmt.Sprintf("%s-%d", prefix, ix))
					data = append(data, x)
					x = []byte(fmt.Sprintf("%s-%d-data", prefix, ix))
					data = append(data, x)
				}
			}

			return func(gid int) int {
				var tx common.Hash
				var err error

				if per == 1 {
					k := fmt.Sprintf("%s-%d", prefix, si)
					v := fmt.Sprintf("%s-%d-data", prefix, si)
					tx, err = kvPut(ctr, []byte(k), []byte(v), true)
				} else {
					tx, err = kvMput(ctr, data, true)
				}
				if err != nil {
					if si == ei {
						fmt.Printf("%d: %s\n", si, err)
					} else {
						fmt.Printf("%d-%d: %s\n", si, ei, err)
					}
				}
				if !silent {
					if si == ei {
						fmt.Printf("%d: %s\n", si, tx.Hex())
					} else {
						fmt.Printf("%d-%d: %s\n", si, ei, tx.Hex())
					}
				}
				if ei >= end {
					fmt.Printf("Checking last tx %s...", tx.Hex())
					ctx, cancel := context.WithCancel(context.Background())
					j := 0
					for {
						r, e := ctr.Cli.TransactionReceipt(ctx, tx)
						if e == nil {
							if r.Status == 1 {
								fmt.Printf("done.\n")
							} else {
								fmt.Fprintf(os.Stderr, "failed with status %d.\n",
									r.Status)
							}
							break
						} else {
							if j%20 == 0 {
								fmt.Printf(".")
							}
							j++
							time.Sleep(500 * time.Millisecond)
						}
					}
					cancel()
				}
				return ei - si + 1
			}
		})

	case "create-accounts":
		if len(nargs) != 5 {
			usage()
			return
		}
		prefix := nargs[1]
		start, e1 := strconv.Atoi(nargs[2])
		count, e2 := strconv.Atoi(nargs[3])
		if e1 != nil || start < 0 || e2 != nil || count <= 0 {
			usage()
			return
		}
		fn := nargs[4]

		if err := CreatePrivKeys(fn, prefix, start, count); err != nil {
			fmt.Fprintf(os.Stderr, "failed to create private keys: %v\n", err)
		}

	case "bulk-send":
		// bulk-send <loop> <count> <value> <froms> <tos>]
		if len(nargs) != 6 {
			usage()
			return
		}
		loop, err := strconv.Atoi(nargs[1])
		if err != nil || loop <= 0 {
			usage()
			return
		}
		count, err := strconv.Atoi(nargs[2])
		if err != nil || count <= 0 {
			usage()
			return
		}

		bulkSend(numThreads, reqUrl, keysFile, loop, count, nargs[3], nargs[4],
			nargs[5], randomTo, rateLimit, dynamicFee)

	case "bulk-call":
		// bulk-call <loop> <count> <froms> <tos> <i-count> <amount>]
		if len(nargs) != 7 {
			usage()
			return
		}
		if contractAddress == nilAddress {
			usage()
			return
		}
		loop, err := strconv.Atoi(nargs[1])
		if err != nil || loop <= 0 {
			usage()
			return
		}
		count, err := strconv.Atoi(nargs[2])
		if err != nil || count <= 0 {
			usage()
			return
		}
		iCount, err := strconv.Atoi(nargs[5])
		if err != nil || iCount <= 0 {
			usage()
			return
		}
		froms, tos, amount := nargs[3], nargs[4], nargs[6]

		bulkCall(numThreads, reqUrl, keysFile, &contractAddress, loop, count, froms, tos, iCount,
			amount, randomTo, rateLimit, dynamicFee)

	default:
		usage()
	}
}

// EOF
