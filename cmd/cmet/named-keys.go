// named-keys.go
//

package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type AcctInfo struct {
	Name string       `json:"name"`
	Key  keystore.Key `json:"key"`
}

type NamedKeys struct {
	sealed    bool
	lck       sync.Mutex
	name2acct map[string]*AcctInfo
	addr2acct map[string]*AcctInfo
}

func OpenNamedKeys(keysFile string, silent bool) (*NamedKeys, error) {
	var err error

	kr := &NamedKeys{
		sealed:    false,
		name2acct: map[string]*AcctInfo{},
		addr2acct: map[string]*AcctInfo{},
	}
	if err = kr.LoadPrivKeys(keysFile, silent); err != nil {
		return nil, err
	}
	return kr, nil
}

// utility function
func (kr *NamedKeys) List() {
	for _, info := range kr.name2acct {
		fmt.Println(info.Name, info.Key.Address.Hex())
	}
}

func (kr *NamedKeys) Seal() {
	kr.sealed = true
}

func (kr *NamedKeys) Name2Address(name string) (common.Address, error) {
	info, err := kr.get(name, common.Address{})
	if err != nil {
		return common.Address{}, err
	}
	return info.Key.Address, nil
}

func (kr *NamedKeys) Address2Name(addr common.Address) (string, error) {
	info, err := kr.get("", addr)
	if err != nil {
		return "", err
	}
	return info.Name, nil
}

func (kr *NamedKeys) get(name string, addr common.Address) (*AcctInfo, error) {
	locked := false
	defer func() {
		if locked {
			kr.lck.Unlock()
		}
	}()
	lock := func() {
		if !locked {
			locked = true
			kr.lck.Lock()
		}
	}
	unlock := func() {
		if locked {
			locked = false
			kr.lck.Unlock()
		}
	}

	// check cache
	lock()
	if acct, ok := kr.name2acct[name]; ok {
		return acct, nil
	} else if acct, ok = kr.addr2acct[string(addr[:])]; ok {
		return acct, nil
	}
	unlock()

	return nil, fmt.Errorf("not Found")
}

// returns pointer, hence not safe when not readonly
func (kr *NamedKeys) Get(name string, addr common.Address) (*AcctInfo, error) {
	if !kr.sealed {
		panic("NamedKeys is not sealed.")
	}

	if acct, ok := kr.name2acct[name]; ok {
		return acct, nil
	} else if acct, ok = kr.addr2acct[string(addr[:])]; ok {
		return acct, nil
	} else {
		return nil, fmt.Errorf("not found")
	}
}

func CreatePrivKeys(fileName, prefix string, start, count int) error {
	f, err := os.OpenFile(fileName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer func() { f.Close() }()

	var lck sync.Mutex

	ix := int64(start)
	BulkFunc(runtime.NumCPU()*2, false,
		func() func(int) int {
			jx := atomic.AddInt64(&ix, 1) - 1
			if jx >= int64(start+count) {
				return nil
			}
			return func(gid int) int {
				key, err := keystore.NewKey(rand.Reader)
				if err != nil {
					fmt.Fprintf(os.Stderr, "key generation failed: %v\n", err)
					return 0
				}

				lck.Lock()
				fmt.Fprintf(f, "%s%d,%s,%s\n", prefix, jx,
					hex.EncodeToString(key.Address[:]),
					hex.EncodeToString(crypto.FromECDSA(key.PrivateKey)))
				lck.Unlock()
				return 1
			}
		})
	return nil
}

func (kr *NamedKeys) LoadPrivKeys(fileName string, silent bool) error {
	if len(fileName) == 0 {
		return nil
	}
	if _, err := os.Stat(fileName); os.IsNotExist(err) {
		return nil
	}

	f, err := os.Open(fileName)
	if err != nil {
		return err
	}
	defer func() { f.Close() }()

	if !silent {
		fmt.Printf("loading keys file %s: ", fileName)
	}

	br := bufio.NewReader(f)
	BulkFunc(runtime.NumCPU()*2, silent,
		func() func(int) int {
			line, _, err := br.ReadLine()
			if err == io.EOF {
				return nil
			} else if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				return nil
			}
			items := strings.Split(string(line), ",")
			if len(items) != 3 {
				fmt.Fprintf(os.Stderr, "Invalid line: %s\n", line)
				return nil
			}

			return func(int) int {
				haddr, err := hex.DecodeString(items[1])
				if err != nil {
					fmt.Fprintf(os.Stderr, "failed to decode address %s: %v\n",
						items[1], err)
					return 0
				}
				addr := common.BytesToAddress(haddr)
				key, err := crypto.HexToECDSA(items[2])
				if err != nil {
					fmt.Fprintf(os.Stderr, "failed to decode private key %s: %v\n",
						items[1], err)
					return 0
				}

				addr2 := crypto.PubkeyToAddress(key.PublicKey)
				if !bytes.Equal(addr[:], addr2[:]) {
					fmt.Fprintf(os.Stderr, "NoT GOOD\n")
					return 0
				}

				acctInfo := &AcctInfo{
					Name: items[0],
					Key: keystore.Key{
						Address:    addr,
						PrivateKey: key,
					},
				}
				kr.lck.Lock()
				kr.name2acct[acctInfo.Name] = acctInfo
				kr.addr2acct[string(acctInfo.Key.Address[:])] = acctInfo
				kr.lck.Unlock()

				return 1
			}
		})

	return nil
}

// EOF
