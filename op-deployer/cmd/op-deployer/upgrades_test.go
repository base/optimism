package main

import (
	"encoding/json"
	"math"
	"testing"
)

func TestUpgradeSchedule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		vars        map[string]string
		wantError   bool
		nameToCheck string
		want        uint64
	}{
		{"unset", map[string]string{}, false, "", 0},
		{"genesis", map[string]string{"L2_BASE_AZUL_BLOCK": "0"}, false, "azul", 1000},
		{"before denim", map[string]string{"L2_BASE_AZUL_BLOCK": "20", "L2_BASE_DENIM_BLOCK": "25"}, false, "azul", 1040},
		{"after denim", map[string]string{"L2_BASE_ZENITH_BLOCK": "100", "L2_BASE_DENIM_BLOCK": "25"}, false, "zenith", 1065},
		{"everest", map[string]string{"L2_BASE_EVEREST_BLOCK": "100", "L2_BASE_DENIM_BLOCK": "25"}, false, "everest", 1065},
		{"misaligned", map[string]string{"L2_BASE_ZENITH_BLOCK": "26", "L2_BASE_DENIM_BLOCK": "25"}, true, "", 0},
		{"negative", map[string]string{"L2_BASE_AZUL_BLOCK": "-1"}, true, "", 0},
		{"noninteger", map[string]string{"L2_ISTHMUS_BLOCK": "1.5"}, true, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := readUpgrades(func(k string) string { return tc.vars[k] })
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
			if err != nil {
				return
			}
			g := document{"config": json.RawMessage(`{"chainId":123,"isthmusTime":0}`)}
			r := document{"isthmus_time": json.RawMessage(`0`)}
			if err := applyUpgrades(g, r, "0x123", u, 1000, 2); err != nil {
				t.Fatal(err)
			}
			if tc.nameToCheck == "" {
				if _, ok := r["base"]; ok {
					t.Fatal("unset upgrades added base config")
				}
				return
			}
			var base map[string]uint64
			if err := json.Unmarshal(r["base"], &base); err != nil {
				t.Fatal(err)
			}
			if base[tc.nameToCheck] != tc.want {
				t.Fatalf("timestamp=%d want=%d", base[tc.nameToCheck], tc.want)
			}
			var cfg map[string]json.RawMessage
			if err := json.Unmarshal(g["config"], &cfg); err != nil {
				t.Fatal(err)
			}
			if string(cfg["chainId"]) != "123" {
				t.Fatal("lost unrelated config")
			}
			if tc.nameToCheck == "azul" && string(cfg["osakaTime"]) != string(mustJSON(t, tc.want)) {
				t.Fatal("Azul did not set Osaka")
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTimestampOverflow(t *testing.T) {
	if _, err := (upgrades{}).timestamp(math.MaxUint64, 1000, 2); err == nil {
		t.Fatal("expected overflow rejection")
	}
}
