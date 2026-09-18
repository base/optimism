# Base devnet setup

Offline, single-chain Base devnet genesis generation in one process. Replaces
`setup-l1.sh`, the genesis path of `setup-l2.sh`, and the `devnet-genesis` helper
for Compose devnets and the standard Base system-test stack. Tests specifically
covering live deployment use a separate upstream op-deployer image.

```sh
go build -o op-deployer ./op-deployer/cmd/op-deployer
go test ./op-deployer/cmd/op-deployer
```

Run with the environment from Base's `setup-devnet` Compose service. Outputs are
written to `OUTPUT_DIR` (L1, default `/output`), `L2_OUTPUT_DIR` (default
`/devnet/l2/configs`), and `SHARED_DIR` (defaults to `OUTPUT_DIR`). The runtime
must provide `eth-genesis-state-generator` and `eth2-val-tools` on PATH for beacon
genesis and validator keystores. No setup shell scripts, jq, envsubst, or separate
op-deployer process are used.

`BASE_DEVNET_VALIDATOR_COUNT` selects the number of validators in beacon genesis
and the matching validator keystores. It defaults to `1` and must be a positive
decimal integer. Set it before generating fresh state; completed setup is reused
without regenerating validators.

The embedded templates preserve Base's current prefunding and chain settings.
The binary merges deployed L1 allocations, sets the L1 starting reference,
patches activation admin and Isthmus/Azul/Beryl/Cobalt/Denim/Zenith schedules,
installs MockProtocolVersions, and emits all configs, P2P keys, JWT, upgrade-signal
environment, and validator data. Post-Denim schedules must align to whole seconds.
A completion marker is written only after both execution and consensus setup
succeed. Completed setup is reused on restart.

The runtime has no subcommands. `op-deployer --help` lists the supported flags:
output directories, chain IDs, slot duration, activation admin, and the six
upgrade-block settings. Existing Compose environment inputs provide role
addresses, P2P keys, and upgrade-signal settings. No intent file, live RPC,
deployment target, generic overrides, or checkpoint configuration is accepted.

The deployment path directly executes the fixed single-chain stages. Optional
alt-DA, additional dispute games, generic prefunding stages, interop/prestate,
intermediate L1 sealing, and disk checkpoints are omitted. The fixed Go intent
preserves the original Base parameters, including L2 dev-account funding.

For fast startup, pre-extract contract artifacts during the image build using
the build-only helper (do not install it in the runtime image):

```sh
go build -o extract-artifacts ./op-deployer/cmd/extract-artifacts
./extract-artifacts /artifacts
```

Set `BASE_DEVNET_ARTIFACTS` to the resulting `bundle-*/forge-artifacts` directory.
Without this setting, embedded artifacts are extracted during startup.
`BASE_DEVNET_TIMESTAMP` and a 32-byte `BASE_DEVNET_SALT` can pin otherwise fresh
inputs for comparisons; normal devnets should leave these unset.

## Validation

Against the original Base scripts in isolated Linux containers with matching
salt/timestamp: default upgrades, Zenith at block 100, and disabled upgrade-signal
preinstallation produced semantically identical JSON and byte-identical beacon
SSZ. Validator identities matched and each keystore validated against its own
random password. Restart preserved every output. JWTs and encrypted keystores
are intentionally randomized.

Measured Docker process wall time was 5.12s for the original setup and 1.11s for
this command with pre-extracted artifacts. Deployment took 524ms, Base finalizing
and export 191ms, and consensus/key generation 74ms. These are setup-container
measurements, not full devnet startup timings.

## Asset provenance

The contract archive in `pkg/deployer/artifacts/forge-artifacts/artifacts.tzst`
is the extracted embedded artifact set from upstream op-deployer v0.6.0-rc.3
(commit 28a63ff7b85c2083c987b405c5cb0b0c48acd401), repacked with zstd. It is checked
in to make this fork buildable without rebuilding Solidity contracts. Regenerate
with `just op-deployer/copy-contract-artifacts` when intentionally upgrading
contracts, and rerun output comparisons.

The Base templates and compiled upgrade-signal artifact were copied from
`base/base`'s local devnet setup. The artifact uses solc 0.8.30 and the
MockProtocolVersions contract. Keep it and its storage layout/upgrade IDs in sync
with Base's contract when changing upgrade-signal behavior.

The previous generic CLI and its command-specific integration tests were removed.
Shared upstream contract/EVM libraries remain in the monorepo; this command does
not import the generic apply/inspect/verify dispatcher.
