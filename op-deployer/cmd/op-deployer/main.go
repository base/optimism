// op-deployer generates the offline Base L1/L2 devnet configuration.
package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-chain-ops/addresses"
	"github.com/ethereum-optimism/optimism/op-deployer/pkg/deployer/pipeline"
	"github.com/ethereum-optimism/optimism/op-deployer/pkg/deployer/state"
	"github.com/ethereum-optimism/optimism/op-deployer/pkg/deployer/version"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/log"
)

//go:embed assets/*
var assets embed.FS

type document map[string]json.RawMessage

type futureForks struct {
	amsterdamTime uint64
	gloasEpoch    uint64
}

func main() {
	if err := run(); err != nil {
		if err != flag.ErrHelp {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return write(path, append(data, '\n'))
}

func object(value any) (document, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result document
	err = json.Unmarshal(data, &result)
	return result, err
}

func set(obj document, key string, value any) error {
	data, err := json.Marshal(value)
	if err == nil {
		obj[key] = data
	}
	return err
}

func template(name string, values map[string]string) ([]byte, error) {
	data, err := assets.ReadFile("assets/" + name)
	if err != nil {
		return nil, err
	}
	return []byte(os.Expand(string(data), func(key string) string {
		if v, ok := values[key]; ok {
			return v
		}
		return os.Getenv(key)
	})), nil
}

func readFutureForks(getenv func(string) string, genesisTime, slotDuration uint64) (*futureForks, error) {
	amsterdam := getenv("BASE_DEVNET_AMSTERDAM_TIME")
	gloas := getenv("BASE_DEVNET_GLOAS_EPOCH")
	if amsterdam == "" && gloas == "" {
		return nil, nil
	}
	if amsterdam == "" || gloas == "" {
		return nil, fmt.Errorf("BASE_DEVNET_AMSTERDAM_TIME and BASE_DEVNET_GLOAS_EPOCH must be set together")
	}
	amsterdamTime, err := strconv.ParseUint(amsterdam, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("BASE_DEVNET_AMSTERDAM_TIME must be an unsigned integer")
	}
	gloasEpoch, err := strconv.ParseUint(gloas, 10, 64)
	if err != nil || gloasEpoch == 0 {
		return nil, fmt.Errorf("BASE_DEVNET_GLOAS_EPOCH must be a positive integer")
	}
	expected, overflow := gloasEpoch*8, false
	if expected/8 != gloasEpoch {
		overflow = true
	} else {
		expected *= slotDuration
		if slotDuration != 0 && expected/slotDuration != gloasEpoch*8 {
			overflow = true
		} else if expected > ^uint64(0)-genesisTime {
			overflow = true
		} else {
			expected += genesisTime
		}
	}
	if overflow || amsterdamTime != expected {
		return nil, fmt.Errorf("Amsterdam time must equal the Gloas epoch boundary")
	}
	return &futureForks{amsterdamTime: amsterdamTime, gloasEpoch: gloasEpoch}, nil
}

func applyFutureForks(genesis any, forks *futureForks) (document, document, error) {
	genesisDoc, err := object(genesis)
	if err != nil {
		return nil, nil, err
	}
	var config document
	if err := json.Unmarshal(genesisDoc["config"], &config); err != nil {
		return nil, nil, err
	}
	if forks != nil {
		if err := set(config, "amsterdamTime", forks.amsterdamTime); err != nil {
			return nil, nil, err
		}
		var schedule document
		if err := json.Unmarshal(config["blobSchedule"], &schedule); err != nil {
			return nil, nil, err
		}
		bpo2, ok := schedule["bpo2"]
		if !ok {
			return nil, nil, fmt.Errorf("BPO2 blob schedule is missing")
		}
		schedule["amsterdam"] = bpo2
		if err := set(config, "blobSchedule", schedule); err != nil {
			return nil, nil, err
		}
	}
	if err := set(genesisDoc, "config", config); err != nil {
		return nil, nil, err
	}
	return genesisDoc, config, nil
}

func run() error {
	if err := parseArgs(os.Args[1:]); err != nil {
		return err
	}

	l1Dir := env("OUTPUT_DIR", "/output")
	l2Dir := env("L2_OUTPUT_DIR", "/devnet/l2/configs")
	shared := env("SHARED_DIR", l1Dir)
	// Compose mounts L1 configs read-only at /genesis, then mounts L2 configs
	// at /genesis/l2. The nested mountpoint must exist before container creation.
	// Also repair setups completed by versions that omitted the empty directory.
	if err := os.MkdirAll(filepath.Join(l1Dir, "l2"), 0755); err != nil {
		return err
	}
	// Mark completion only after both chains and consensus keys have succeeded.
	if _, err := os.Stat(filepath.Join(l1Dir, ".setup-complete")); err == nil {
		for _, path := range []string{filepath.Join(l1Dir, "el/genesis.json"), filepath.Join(l1Dir, "cl/genesis.ssz"), filepath.Join(l2Dir, ".setup-complete")} {
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("completed setup is missing %s: %w", path, err)
			}
		}
		fmt.Println("Devnet configuration already complete; reusing files")
		return nil
	}
	if err := os.Remove(filepath.Join(l2Dir, ".setup-complete")); err != nil && !os.IsNotExist(err) {
		return err
	}
	timestamp, err := strconv.ParseUint(env("BASE_DEVNET_TIMESTAMP", strconv.FormatInt(time.Now().Unix(), 10)), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid genesis timestamp: %w", err)
	}
	slotDuration, err := strconv.ParseUint(env("SLOT_DURATION", "12"), 10, 64)
	if err != nil || slotDuration == 0 {
		return fmt.Errorf("SLOT_DURATION must be a positive integer")
	}
	future, err := readFutureForks(os.Getenv, timestamp, slotDuration)
	if err != nil {
		return err
	}
	l1ID := env("CHAIN_ID", env("L1_CHAIN_ID", "1337"))
	l2ID, err := strconv.ParseUint(env("L2_CHAIN_ID", "84538453"), 10, 64)
	if err != nil {
		return err
	}
	values := map[string]string{
		"CHAIN_ID": l1ID, "L1_CHAIN_ID": l1ID,
		"GENESIS_TIME": strconv.FormatUint(timestamp, 10), "GENESIS_TIME_HEX": fmt.Sprintf("0x%x", timestamp),
		"BALANCE": "0xd3c21bcecceda1000000", "SLOT_DURATION": strconv.FormatUint(slotDuration, 10),
	}
	admin := env("L2_ACTIVATION_ADMIN_ADDR", os.Getenv("SEQUENCER_ADDR"))
	if !common.IsHexAddress(admin) {
		return fmt.Errorf("invalid activation admin address")
	}
	upgrades, err := readUpgrades(os.Getenv)
	if err != nil {
		return err
	}
	intent, err := baseIntent(l1ID, l2ID)
	if err != nil {
		return err
	}

	st := &state.State{Version: 1, OpDeployerVersion: version.VersionWithMeta}
	if salt := os.Getenv("BASE_DEVNET_SALT"); salt != "" {
		if len(strings.TrimPrefix(salt, "0x")) != 64 {
			return fmt.Errorf("BASE_DEVNET_SALT must be 32 bytes")
		}
		decoded, err := hex.DecodeString(strings.TrimPrefix(salt, "0x"))
		if err != nil {
			return err
		}
		st.Create2Salt = common.BytesToHash(decoded)
	}
	cache, err := os.MkdirTemp("", "base-devnet-artifacts-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(cache)
	start := time.Now()
	logger := log.NewLogger(log.NewTerminalHandler(os.Stderr, false))
	if err := deploy(intent, st, logger, cache); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "deployment=%s\n", time.Since(start))
	start = time.Now()
	l1Data, err := template("l1-el-genesis.json.template", values)
	if err != nil {
		return err
	}
	var l1 core.Genesis
	if err := json.Unmarshal(l1Data, &l1); err != nil {
		return err
	}
	for addr, account := range l1.Alloc {
		if deployed, ok := st.L1StateDump.Data.Accounts[addr]; ok {
			deployed.Balance = account.Balance
			st.L1StateDump.Data.Accounts[addr] = deployed
		} else {
			st.L1StateDump.Data.Accounts[addr] = account
		}
	}
	l1.Alloc = st.L1StateDump.Data.Accounts
	block := l1.ToBlock()
	st.Chains[0].StartBlock = &state.L1BlockRefJSON{Hash: block.Hash(), ParentHash: block.ParentHash(), Time: hexutil.Uint64(block.Time())}
	l2, rollup, err := pipeline.RenderGenesisAndRollup(st, intent.Chains[0].ID, nil)
	if err != nil {
		return err
	}
	l2Doc, err := object(l2)
	if err != nil {
		return err
	}
	rollupDoc, err := object(rollup)
	if err != nil {
		return err
	}
	if err := applyUpgrades(l2Doc, rollupDoc, admin, upgrades, rollup.Genesis.L2Time, rollup.BlockTime); err != nil {
		return err
	}
	if env("UPGRADE_SIGNAL_PREINSTALL", "true") == "true" {
		artifact, err := assets.ReadFile("assets/upgrade-signal.json")
		if err != nil {
			return err
		}
		if err := installUpgradeSignal(&l1, rollupDoc, artifact); err != nil {
			return err
		}
	}
	// Match the existing scripts: Base patches do not recompute the L2 block hash.
	var refs document
	if err := json.Unmarshal(rollupDoc["genesis"], &refs); err != nil {
		return err
	}
	if err := set(refs, "l1", map[string]any{"hash": l1.ToBlock().Hash(), "number": 0}); err != nil {
		return err
	}
	if err := set(rollupDoc, "genesis", refs); err != nil {
		return err
	}
	l1Addresses := addresses.L1Contracts{
		SuperchainContracts:      *st.SuperchainDeployment,
		ImplementationsContracts: *st.ImplementationsDeployment,
		OpChainContracts:         st.Chains[0].OpChainContracts,
	}
	l1Doc, l1Config, err := applyFutureForks(&l1, future)
	if err != nil {
		return err
	}

	for _, output := range []struct {
		path  string
		value any
	}{
		{filepath.Join(l1Dir, "el/genesis.json"), l1Doc}, {filepath.Join(l1Dir, "el/chain-config.json"), l1Config},
		{filepath.Join(l2Dir, "genesis.json"), l2Doc}, {filepath.Join(l2Dir, "rollup.json"), rollupDoc},
		{filepath.Join(l2Dir, "l1-addresses.json"), l1Addresses},
	} {
		if err := writeJSON(output.path, output.value); err != nil {
			return err
		}
	}
	delete(rollupDoc, "base")
	if err := writeJSON(filepath.Join(l2Dir, "rollup-conductor.json"), rollupDoc); err != nil {
		return err
	}
	if err := writeKeys(l1Dir, l2Dir); err != nil {
		return err
	}
	if err := write(filepath.Join(shared, "genesis_timestamp"), []byte(values["GENESIS_TIME"]+"\n")); err != nil {
		return err
	}
	if env("UPGRADE_SIGNAL_PREINSTALL", "true") == "true" {
		signal := "BASE_NODE_UPGRADE_SIGNAL_CONTRACT=" + upgradeSignalAddress + "\n" +
			"BASE_NODE_UPGRADE_SIGNAL_L1_RPC=http://l1-el:" + env("L1_HTTP_PORT", "4545") + "\n" +
			"BASE_NODE_UPGRADE_SIGNAL_MODE=" + env("UPGRADE_SIGNAL_MODE", "runtime-admin") + "\n" +
			"BASE_NODE_UPGRADE_SIGNAL_L1_BLOCK_TAG=" + env("UPGRADE_SIGNAL_L1_BLOCK_TAG", "latest") + "\n"
		if err := write(filepath.Join(l2Dir, "upgrade-signal.env"), []byte(signal)); err != nil {
			return err
		}
	} else if err := os.Remove(filepath.Join(l2Dir, "upgrade-signal.env")); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Fprintf(os.Stderr, "base_finalize_and_export=%s\n", time.Since(start))
	start = time.Now()
	if err := consensus(l1Dir, values, future); err != nil {
		return err
	}
	if err := write(filepath.Join(l2Dir, ".setup-complete"), nil); err != nil {
		return err
	}
	if err := write(filepath.Join(l1Dir, ".setup-complete"), nil); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "consensus_and_keys=%s\n", time.Since(start))
	return nil
}

func writeKeys(l1Dir, l2Dir string) error {
	jwt := make([]byte, 32)
	if _, err := rand.Read(jwt); err != nil {
		return err
	}
	if err := write(filepath.Join(l1Dir, "jwt.hex"), []byte(hex.EncodeToString(jwt)+"\n")); err != nil {
		return err
	}
	elID := env("L2_EL_BOOTNODE_ENODE_ID", "4f355bdcb7cc0af728ef3cceb9615d90684bb5b2ca5f859ab0f0b704075871aa385b6b1b8ead809ca67454d9683fcf2ba03456d6fe2c4abe2b07f0fbdbb2f1c1")
	files := map[string]string{
		"builder-p2p-key.txt": os.Getenv("BUILDER_P2P_KEY") + "\n", "builder-enode-id.txt": os.Getenv("BUILDER_ENODE_ID") + "\n",
		"el-bootnode-p2p-key.txt":  env("L2_EL_BOOTNODE_P2P_KEY", strings.Repeat("1", 64)),
		"el-bootnode-enode-id.txt": elID + "\n", "el-bootnode-enode.txt": env("L2_EL_BOOTNODE_ENODE", "enode://"+elID+"@172.30.0.10:9303") + "\n",
		"cl-bootnode-p2p-key.txt":  env("L2_CL_BOOTNODE_P2P_KEY", strings.Repeat("2", 64)),
		"cl-bootnode-enr-path.txt": env("L2_CL_BOOTNODE_ENR_PATH", "/bootnodes/cl-bootnode.enr") + "\n",
		"sequencer-1-p2p-key.txt":  os.Getenv("SEQ1_P2P_KEY") + "\n", "sequencer-2-p2p-key.txt": os.Getenv("SEQ2_P2P_KEY") + "\n",
	}
	for name, data := range files {
		if err := write(filepath.Join(l2Dir, name), []byte(data)); err != nil {
			return err
		}
	}
	return nil
}
