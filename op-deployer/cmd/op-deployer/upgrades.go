package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type upgrades map[string]uint64

func readUpgrades(getenv func(string) string) (upgrades, error) {
	result := make(upgrades)
	for _, name := range []string{"isthmus", "azul", "beryl", "cobalt", "denim", "everest", "zenith"} {
		key := "L2_BASE_" + strings.ToUpper(name) + "_BLOCK"
		if name == "isthmus" {
			key = "L2_ISTHMUS_BLOCK"
		}
		value := getenv(key)
		if value == "" {
			continue
		}
		for _, ch := range value {
			if ch < '0' || ch > '9' {
				return nil, fmt.Errorf("%s must be a non-negative integer", key)
			}
		}
		block, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		result[name] = block
	}
	if denim, ok := result["denim"]; ok {
		for name, block := range result {
			if block > denim && (block-denim)%5 != 0 {
				return nil, fmt.Errorf("%s must align to a whole second after Denim", name)
			}
		}
	}
	return result, nil
}

func (u upgrades) timestamp(block, genesis, blockTime uint64) (uint64, error) {
	before := block
	after := uint64(0)
	if denim, ok := u["denim"]; ok && block > denim {
		before = denim
		after = (block - denim) / 5
	}
	if blockTime == 0 || before > (math.MaxUint64-genesis)/blockTime {
		return 0, fmt.Errorf("activation timestamp overflow or zero block time")
	}
	timestamp := genesis + before*blockTime
	if after > math.MaxUint64-timestamp {
		return 0, fmt.Errorf("activation timestamp overflow")
	}
	return timestamp + after, nil
}

func applyUpgrades(genesis, rollup document, admin string, u upgrades, genesisTime, blockTime uint64) error {
	var config document
	if err := json.Unmarshal(genesis["config"], &config); err != nil {
		return err
	}
	if err := set(config, "activationAdminAddress", admin); err != nil {
		return err
	}
	var baseGenesis, baseRollup document
	if raw := config["base"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &baseGenesis); err != nil {
			return err
		}
	}
	if raw := rollup["base"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &baseRollup); err != nil {
			return err
		}
	}
	if baseGenesis == nil {
		baseGenesis = make(document)
	}
	if baseRollup == nil {
		baseRollup = make(document)
	}
	changedBase := false
	for name, block := range u {
		timestamp, err := u.timestamp(block, genesisTime, blockTime)
		if err != nil {
			return err
		}
		if name == "isthmus" {
			if err := set(config, "isthmusTime", timestamp); err != nil {
				return err
			}
			if err := set(rollup, "isthmus_time", timestamp); err != nil {
				return err
			}
		} else {
			changedBase = true
			if err := set(baseGenesis, name, timestamp); err != nil {
				return err
			}
			if err := set(baseRollup, name, timestamp); err != nil {
				return err
			}
			if name == "azul" {
				if err := set(config, "osakaTime", timestamp); err != nil {
					return err
				}
			}
		}
	}
	if changedBase {
		if err := set(config, "base", baseGenesis); err != nil {
			return err
		}
		if err := set(rollup, "base", baseRollup); err != nil {
			return err
		}
	}
	return set(genesis, "config", config)
}
