package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsensusValidatorCount(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{"default", "", "1"},
		{"override", "64", "64"},
		{"decimal", "008", "8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cl := filepath.Join(root, "cl")
			genesisArgs := filepath.Join(root, "genesis-args")
			keystoreArgs := filepath.Join(root, "keystore-args")
			t.Setenv("BASE_DEVNET_VALIDATOR_COUNT", tc.value)
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GENESIS_ARGS", genesisArgs)
			t.Setenv("KEYSTORE_ARGS", keystoreArgs)
			t.Setenv("KEYSTORE_DIR", filepath.Join(cl, "validator_keys"))
			// Stub the external tools to observe their inputs without generating real BLS keys.
			for name, script := range map[string]string{
				"eth-genesis-state-generator": "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GENESIS_ARGS\"\n",
				"eth2-val-tools":              "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$KEYSTORE_ARGS\"\nmkdir -p \"$KEYSTORE_DIR/keys\"\n",
			} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(script), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := consensus(root, nil); err != nil {
				t.Fatal(err)
			}
			mnemonics, err := os.ReadFile(filepath.Join(cl, "mnemonics.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			want := "- mnemonic: \"test test test test test test test test test test test junk\"\n  count: " + tc.want + "\n"
			if string(mnemonics) != want {
				t.Fatalf("mnemonics=%q want=%q", mnemonics, want)
			}
			for path, fragments := range map[string][]string{
				genesisArgs:  {"--mnemonics\n" + filepath.Join(cl, "mnemonics.yaml") + "\n"},
				keystoreArgs: {"--source-min=0\n", "--source-max=" + tc.want + "\n", "--out-loc=" + filepath.Join(cl, "validator_keys") + "\n"},
			} {
				args, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, fragment := range fragments {
					if !strings.Contains(string(args), fragment) {
						t.Fatalf("%s arguments %q missing %q", path, args, fragment)
					}
				}
			}
		})
	}
}

func TestConsensusRejectsInvalidValidatorCount(t *testing.T) {
	for _, value := range []string{"0", "-1", "abc", "1.5", "18446744073709551616"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("BASE_DEVNET_VALIDATOR_COUNT", value)
			root := t.TempDir()
			err := consensus(root, nil)
			if err == nil || err.Error() != "BASE_DEVNET_VALIDATOR_COUNT must be a positive integer" {
				t.Fatalf("expected validator count validation error, got %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("invalid validator count wrote consensus output")
			}
		})
	}
}
