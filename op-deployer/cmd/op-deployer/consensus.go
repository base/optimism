package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

const validatorMnemonic = "test test test test test test test test test test test junk"

func command(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func consensus(l1Dir string, values map[string]string) error {
	validatorCount, err := strconv.ParseUint(env("BASE_DEVNET_VALIDATOR_COUNT", "1"), 10, 64)
	if err != nil || validatorCount == 0 {
		return fmt.Errorf("BASE_DEVNET_VALIDATOR_COUNT must be a positive integer")
	}
	cl := filepath.Join(l1Dir, "cl")
	config, err := template("l1-cl-config.yaml.template", values)
	if err != nil {
		return err
	}
	if err := write(filepath.Join(cl, "config.yaml"), config); err != nil {
		return err
	}
	if err := write(filepath.Join(cl, "mnemonics.yaml"), []byte(fmt.Sprintf("- mnemonic: %q\n  count: %d\n", validatorMnemonic, validatorCount))); err != nil {
		return err
	}
	if err := command("eth-genesis-state-generator", "beaconchain", "--eth1-config", filepath.Join(l1Dir, "el/genesis.json"), "--config", filepath.Join(cl, "config.yaml"), "--mnemonics", filepath.Join(cl, "mnemonics.yaml"), "--state-output", filepath.Join(cl, "genesis.ssz")); err != nil {
		return err
	}
	for _, dir := range []string{"validator_keys", "validator_data"} {
		if err := os.RemoveAll(filepath.Join(cl, dir)); err != nil {
			return err
		}
	}
	if err := command("eth2-val-tools", "keystores", "--insecure", "--source-mnemonic="+validatorMnemonic, "--source-min=0", "--source-max="+strconv.FormatUint(validatorCount, 10), "--out-loc="+filepath.Join(cl, "validator_keys")); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(cl, "validator_keys/keys"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pubkey := entry.Name()
		for _, pair := range [][2]string{
			{filepath.Join(cl, "validator_keys/keys", pubkey, "voting-keystore.json"), filepath.Join(cl, "validator_data/validators", pubkey, "voting-keystore.json")},
			{filepath.Join(cl, "validator_keys/secrets", pubkey), filepath.Join(cl, "validator_data/secrets", pubkey)},
		} {
			data, err := os.ReadFile(pair[0])
			if err != nil {
				return err
			}
			if err := write(pair[1], data); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"deploy_block.txt", "deposit_contract_block.txt"} {
		if err := write(filepath.Join(cl, name), []byte("0\n")); err != nil {
			return err
		}
	}
	return nil
}
