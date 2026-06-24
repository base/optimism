package derive

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"math/big"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/ethereum-optimism/optimism/op-service/testutils"
)

type txTypeTest struct {
	name   string
	mkTx   func(rng *rand.Rand, signer types.Signer) *types.Transaction
	signer types.Signer
}

func TestSpanBatchTxsContractCreationBits(t *testing.T) {
	rng := rand.New(rand.NewSource(0x1234567))
	chainID := big.NewInt(rng.Int63n(1000))

	rawSpanBatch := RandomRawSpanBatch(rng, chainID)
	contractCreationBits := rawSpanBatch.txs.contractCreationBits
	totalBlockTxCount := rawSpanBatch.txs.totalBlockTxCount

	var sbt spanBatchTxs
	sbt.contractCreationBits = contractCreationBits
	sbt.totalBlockTxCount = totalBlockTxCount

	var buf bytes.Buffer
	err := sbt.encodeContractCreationBits(&buf)
	require.NoError(t, err)

	// contractCreationBit field is fixed length: single bit
	contractCreationBitBufferLen := totalBlockTxCount / 8
	if totalBlockTxCount%8 != 0 {
		contractCreationBitBufferLen++
	}
	require.Equal(t, buf.Len(), int(contractCreationBitBufferLen))

	result := buf.Bytes()
	sbt.contractCreationBits = nil

	r := bytes.NewReader(result)
	err = sbt.decodeContractCreationBits(r)
	require.NoError(t, err)

	require.Equal(t, contractCreationBits, sbt.contractCreationBits)
}

func TestSpanBatchTxsContractCreationCount(t *testing.T) {
	rng := rand.New(rand.NewSource(0x1337))
	chainID := big.NewInt(rng.Int63n(1000))

	rawSpanBatch := RandomRawSpanBatch(rng, chainID)

	contractCreationBits := rawSpanBatch.txs.contractCreationBits
	contractCreationCount, err := rawSpanBatch.txs.contractCreationCount()
	require.NoError(t, err)
	totalBlockTxCount := rawSpanBatch.txs.totalBlockTxCount

	var sbt spanBatchTxs
	sbt.contractCreationBits = contractCreationBits
	sbt.totalBlockTxCount = totalBlockTxCount

	var buf bytes.Buffer
	err = sbt.encodeContractCreationBits(&buf)
	require.NoError(t, err)

	result := buf.Bytes()
	sbt.contractCreationBits = nil

	r := bytes.NewReader(result)
	err = sbt.decodeContractCreationBits(r)
	require.NoError(t, err)

	contractCreationCount2, err := sbt.contractCreationCount()
	require.NoError(t, err)

	require.Equal(t, contractCreationCount, contractCreationCount2)
}

func TestSpanBatchTxsYParityBits(t *testing.T) {
	rng := rand.New(rand.NewSource(0x7331))
	chainID := big.NewInt(rng.Int63n(1000))

	rawSpanBatch := RandomRawSpanBatch(rng, chainID)
	yParityBits := rawSpanBatch.txs.yParityBits
	totalBlockTxCount := rawSpanBatch.txs.totalBlockTxCount

	var sbt spanBatchTxs
	sbt.yParityBits = yParityBits
	sbt.totalBlockTxCount = totalBlockTxCount

	var buf bytes.Buffer
	err := sbt.encodeYParityBits(&buf)
	require.NoError(t, err)

	// yParityBit field is fixed length: single bit
	yParityBitBufferLen := totalBlockTxCount / 8
	if totalBlockTxCount%8 != 0 {
		yParityBitBufferLen++
	}
	require.Equal(t, buf.Len(), int(yParityBitBufferLen))

	result := buf.Bytes()
	sbt.yParityBits = nil

	r := bytes.NewReader(result)
	err = sbt.decodeYParityBits(r)
	require.NoError(t, err)

	require.Equal(t, yParityBits, sbt.yParityBits)
}

func TestSpanBatchTxsProtectedBits(t *testing.T) {
	rng := rand.New(rand.NewSource(0x7331))
	chainID := big.NewInt(rng.Int63n(1000))

	rawSpanBatch := RandomRawSpanBatch(rng, chainID)
	protectedBits := rawSpanBatch.txs.protectedBits
	txTypes := rawSpanBatch.txs.txTypes
	totalBlockTxCount := rawSpanBatch.txs.totalBlockTxCount
	totalLegacyTxCount := rawSpanBatch.txs.totalLegacyTxCount

	var sbt spanBatchTxs
	sbt.protectedBits = protectedBits
	sbt.totalBlockTxCount = totalBlockTxCount
	sbt.txTypes = txTypes
	sbt.totalLegacyTxCount = totalLegacyTxCount

	var buf bytes.Buffer
	err := sbt.encodeProtectedBits(&buf)
	require.NoError(t, err)

	// protectedBit field is fixed length: single bit
	protectedBitBufferLen := totalLegacyTxCount / 8
	require.NoError(t, err)
	if totalLegacyTxCount%8 != 0 {
		protectedBitBufferLen++
	}
	require.Equal(t, buf.Len(), int(protectedBitBufferLen))

	result := buf.Bytes()
	sbt.protectedBits = nil

	r := bytes.NewReader(result)
	err = sbt.decodeProtectedBits(r)
	require.NoError(t, err)

	require.Equal(t, protectedBits, sbt.protectedBits)
}

func TestSpanBatchTxsTxSigs(t *testing.T) {
	rng := rand.New(rand.NewSource(0x73311337))
	chainID := big.NewInt(rng.Int63n(1000))

	rawSpanBatch := RandomRawSpanBatch(rng, chainID)
	txSigs := rawSpanBatch.txs.txSigs
	totalBlockTxCount := rawSpanBatch.txs.totalBlockTxCount

	var sbt spanBatchTxs
	sbt.totalBlockTxCount = totalBlockTxCount
	sbt.txSigs = txSigs

	var buf bytes.Buffer
	err := sbt.encodeTxSigsRS(&buf)
	require.NoError(t, err)

	// txSig field is fixed length: 32 byte + 32 byte = 64 byte
	require.Equal(t, buf.Len(), 64*int(totalBlockTxCount))

	result := buf.Bytes()
	sbt.txSigs = nil

	r := bytes.NewReader(result)
	err = sbt.decodeTxSigsRS(r)
	require.NoError(t, err)

	// v field is not set
	for i := 0; i < int(totalBlockTxCount); i++ {
		require.Equal(t, txSigs[i].r, sbt.txSigs[i].r)
		require.Equal(t, txSigs[i].s, sbt.txSigs[i].s)
	}
}

func TestSpanBatchTxsTxNonces(t *testing.T) {
	rng := rand.New(rand.NewSource(0x123456))
	chainID := big.NewInt(rng.Int63n(1000))

	rawSpanBatch := RandomRawSpanBatch(rng, chainID)
	txNonces := rawSpanBatch.txs.txNonces
	totalBlockTxCount := rawSpanBatch.txs.totalBlockTxCount

	var sbt spanBatchTxs
	sbt.totalBlockTxCount = totalBlockTxCount
	sbt.txNonces = txNonces

	var buf bytes.Buffer
	err := sbt.encodeTxNonces(&buf)
	require.NoError(t, err)

	result := buf.Bytes()
	sbt.txNonces = nil

	r := bytes.NewReader(result)
	err = sbt.decodeTxNonces(r)
	require.NoError(t, err)

	require.Equal(t, txNonces, sbt.txNonces)
}

func TestSpanBatchTxsTxGases(t *testing.T) {
	rng := rand.New(rand.NewSource(0x12345))
	chainID := big.NewInt(rng.Int63n(1000))

	rawSpanBatch := RandomRawSpanBatch(rng, chainID)
	txGases := rawSpanBatch.txs.txGases
	totalBlockTxCount := rawSpanBatch.txs.totalBlockTxCount

	var sbt spanBatchTxs
	sbt.totalBlockTxCount = totalBlockTxCount
	sbt.txGases = txGases

	var buf bytes.Buffer
	err := sbt.encodeTxGases(&buf)
	require.NoError(t, err)

	result := buf.Bytes()
	sbt.txGases = nil

	r := bytes.NewReader(result)
	err = sbt.decodeTxGases(r)
	require.NoError(t, err)

	require.Equal(t, txGases, sbt.txGases)
}

func TestSpanBatchTxsTxTos(t *testing.T) {
	rng := rand.New(rand.NewSource(0x54321))
	chainID := big.NewInt(rng.Int63n(1000))

	rawSpanBatch := RandomRawSpanBatch(rng, chainID)
	txTos := rawSpanBatch.txs.txTos
	contractCreationBits := rawSpanBatch.txs.contractCreationBits
	totalBlockTxCount := rawSpanBatch.txs.totalBlockTxCount

	var sbt spanBatchTxs
	sbt.txTos = txTos
	// creation bits and block tx count must be se to decode tos
	sbt.contractCreationBits = contractCreationBits
	sbt.totalBlockTxCount = totalBlockTxCount

	var buf bytes.Buffer
	err := sbt.encodeTxTos(&buf)
	require.NoError(t, err)

	// to field is fixed length: 20 bytes
	require.Equal(t, buf.Len(), 20*len(txTos))

	result := buf.Bytes()
	sbt.txTos = nil

	r := bytes.NewReader(result)
	err = sbt.decodeTxTos(r)
	require.NoError(t, err)

	require.Equal(t, txTos, sbt.txTos)
}

func TestSpanBatchTxsTxDatas(t *testing.T) {
	rng := rand.New(rand.NewSource(0x1234))
	chainID := big.NewInt(rng.Int63n(1000))

	rawSpanBatch := RandomRawSpanBatch(rng, chainID)
	txDatas := rawSpanBatch.txs.txDatas
	txTypes := rawSpanBatch.txs.txTypes
	totalBlockTxCount := rawSpanBatch.txs.totalBlockTxCount

	var sbt spanBatchTxs
	sbt.totalBlockTxCount = totalBlockTxCount

	sbt.txDatas = txDatas

	var buf bytes.Buffer
	err := sbt.encodeTxDatas(&buf)
	require.NoError(t, err)

	result := buf.Bytes()
	sbt.txDatas = nil
	sbt.txTypes = nil

	r := bytes.NewReader(result)
	err = sbt.decodeTxDatas(r)
	require.NoError(t, err)

	require.Equal(t, txDatas, sbt.txDatas)
	require.Equal(t, txTypes, sbt.txTypes)
}

func TestSpanBatchTxsAddTxs(t *testing.T) {
	rng := rand.New(rand.NewSource(0x1234))
	chainID := big.NewInt(rng.Int63n(1000))
	// make batches to extract txs from
	batches := RandomValidConsecutiveSingularBatches(rng, chainID)
	allTxs := [][]byte{}

	iterativeSBTX, err := newSpanBatchTxs([][]byte{}, chainID)
	require.NoError(t, err)
	for i := 0; i < len(batches); i++ {
		// explicitly extract txs due to mismatch of [][]byte to []hexutil.Bytes
		txs := [][]byte{}
		for j := 0; j < len(batches[i].Transactions); j++ {
			txs = append(txs, batches[i].Transactions[j])
		}
		err = iterativeSBTX.AddTxs(txs, chainID)
		require.NoError(t, err)
		allTxs = append(allTxs, txs...)
	}

	fullSBTX, err := newSpanBatchTxs(allTxs, chainID)
	require.NoError(t, err)

	require.Equal(t, iterativeSBTX, fullSBTX)
}

func TestSpanBatchTxsRecoverV(t *testing.T) {
	rng := rand.New(rand.NewSource(0x123))

	chainID := big.NewInt(rng.Int63n(1000))
	isthmusSigner := types.NewIsthmusSigner(chainID)
	totalblockTxCount := 20 + rng.Intn(100)

	cases := []txTypeTest{
		{"unprotected legacy tx", testutils.RandomLegacyTx, types.HomesteadSigner{}},
		{"legacy tx", testutils.RandomLegacyTx, isthmusSigner},
		{"access list tx", testutils.RandomAccessListTx, isthmusSigner},
		{"dynamic fee tx", testutils.RandomDynamicFeeTx, isthmusSigner},
		{"setcode tx", testutils.RandomSetCodeTx, isthmusSigner},
		{"eip8130 tx", testutils.RandomEip8130Tx, isthmusSigner},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var spanBatchTxs spanBatchTxs
			var txTypes []int
			var txSigs []spanBatchSignature
			var originalVs []*big.Int
			yParityBits := new(big.Int)
			protectedBits := new(big.Int)
			totalLegacyTxCount := 0
			for idx := 0; idx < totalblockTxCount; idx++ {
				tx := testCase.mkTx(rng, testCase.signer)
				txType := tx.Type()
				txTypes = append(txTypes, int(txType))
				var txSig spanBatchSignature
				v, r, s := tx.RawSignatureValues()
				if txType == types.LegacyTxType {
					protectedBit := uint(0)
					if tx.Protected() {
						protectedBit = uint(1)
					}
					protectedBits.SetBit(protectedBits, int(totalLegacyTxCount), protectedBit)
					totalLegacyTxCount++
				}
				// Do not fill in txSig.V
				txSig.r, _ = uint256.FromBig(r)
				txSig.s, _ = uint256.FromBig(s)
				txSigs = append(txSigs, txSig)
				originalVs = append(originalVs, v)
				yParityBit, err := convertVToYParity(v, int(tx.Type()))
				require.NoError(t, err)
				yParityBits.SetBit(yParityBits, idx, yParityBit)
			}

			spanBatchTxs.yParityBits = yParityBits
			spanBatchTxs.txSigs = txSigs
			spanBatchTxs.txTypes = txTypes
			spanBatchTxs.protectedBits = protectedBits
			// recover txSig.v
			err := spanBatchTxs.recoverV(chainID)
			require.NoError(t, err)

			var recoveredVs []*big.Int
			for _, txSig := range spanBatchTxs.txSigs {
				recoveredVs = append(recoveredVs, txSig.v)
			}
			requireEqual(t, originalVs, recoveredVs, "recovered v mismatch")
		})
	}
}

func TestSpanBatchTxsRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(0x73311337))
	chainID := big.NewInt(rng.Int63n(1000))

	for i := 0; i < 4; i++ {
		rawSpanBatch := RandomRawSpanBatch(rng, chainID)
		sbt := rawSpanBatch.txs
		totalBlockTxCount := sbt.totalBlockTxCount

		var buf bytes.Buffer
		err := sbt.encode(&buf)
		require.NoError(t, err)

		result := buf.Bytes()
		r := bytes.NewReader(result)

		var sbt2 spanBatchTxs
		sbt2.totalBlockTxCount = totalBlockTxCount
		err = sbt2.decode(r)
		require.NoError(t, err)

		err = sbt2.recoverV(chainID)
		require.NoError(t, err)

		requireEqual(t, sbt, &sbt2)
	}
}

func TestSpanBatchTxsRoundTripFullTxs(t *testing.T) {
	rng := rand.New(rand.NewSource(0x13377331))
	chainID := big.NewInt(rng.Int63n(1000))
	isthmusSigner := types.NewIsthmusSigner(chainID)

	cases := []txTypeTest{
		{"unprotected legacy tx", testutils.RandomLegacyTx, types.HomesteadSigner{}},
		{"legacy tx", testutils.RandomLegacyTx, isthmusSigner},
		{"access list tx", testutils.RandomAccessListTx, isthmusSigner},
		{"dynamic fee tx", testutils.RandomDynamicFeeTx, isthmusSigner},
		{"setcode tx", testutils.RandomSetCodeTx, isthmusSigner},
		{"eip8130 tx", testutils.RandomEip8130Tx, isthmusSigner},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for i := 0; i < 4; i++ {
				totalblockTxCounts := uint64(1 + rng.Int()&0xFF)
				var txs [][]byte
				for i := 0; i < int(totalblockTxCounts); i++ {
					tx := testCase.mkTx(rng, testCase.signer)
					rawTx, err := tx.MarshalBinary()
					require.NoError(t, err)
					txs = append(txs, rawTx)
				}
				sbt, err := newSpanBatchTxs(txs, chainID)
				require.NoError(t, err)

				txs2, err := sbt.fullTxs(chainID)
				require.NoError(t, err)

				require.Equal(t, txs, txs2)
			}
		})
	}
}

func TestSpanBatchTxsRecoverVInvalidTxType(t *testing.T) {
	rng := rand.New(rand.NewSource(0x321))
	chainID := big.NewInt(rng.Int63n(1000))

	var sbt spanBatchTxs

	sbt.txTypes = []int{types.DepositTxType}
	sbt.txSigs = []spanBatchSignature{{v: big.NewInt(0), r: nil, s: nil}}
	sbt.yParityBits = new(big.Int)
	sbt.protectedBits = new(big.Int)

	err := sbt.recoverV(chainID)
	require.ErrorContains(t, err, "invalid tx type")
}

func TestSpanBatchTxsFullTxNotEnoughTxTos(t *testing.T) {
	rng := rand.New(rand.NewSource(0x13572468))
	chainID := big.NewInt(rng.Int63n(1000))
	isthmusSigner := types.NewIsthmusSigner(chainID)

	cases := []txTypeTest{
		{"unprotected legacy tx", testutils.RandomLegacyTx, types.HomesteadSigner{}},
		{"legacy tx", testutils.RandomLegacyTx, isthmusSigner},
		{"access list tx", testutils.RandomAccessListTx, isthmusSigner},
		{"dynamic fee tx", testutils.RandomDynamicFeeTx, isthmusSigner},
		{"setcode tx", testutils.RandomSetCodeTx, isthmusSigner},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			totalblockTxCounts := uint64(1 + rng.Int()&0xFF)
			var txs [][]byte
			for i := 0; i < int(totalblockTxCounts); i++ {
				tx := testCase.mkTx(rng, testCase.signer)
				rawTx, err := tx.MarshalBinary()
				require.NoError(t, err)
				txs = append(txs, rawTx)
			}
			sbt, err := newSpanBatchTxs(txs, chainID)
			require.NoError(t, err)

			// drop single to field
			sbt.txTos = sbt.txTos[:len(sbt.txTos)-2]

			_, err = sbt.fullTxs(chainID)
			require.EqualError(t, err, "tx to not enough")
		})
	}
}

func TestSpanBatchTxsMaxContractCreationBitsLength(t *testing.T) {
	var sbt spanBatchTxs
	sbt.totalBlockTxCount = 0xFFFFFFFFFFFFFFFF

	r := bytes.NewReader([]byte{})
	err := sbt.decodeContractCreationBits(r)
	require.ErrorIs(t, err, ErrTooBigSpanBatchSize)
}

func TestSpanBatchTxsMaxYParityBitsLength(t *testing.T) {
	var sb RawSpanBatch
	sb.blockCount = 0xFFFFFFFFFFFFFFFF

	r := bytes.NewReader([]byte{})
	err := sb.decodeOriginBits(r)
	require.ErrorIs(t, err, ErrTooBigSpanBatchSize)
}

func TestSpanBatchTxsMaxProtectedBitsLength(t *testing.T) {
	var sb RawSpanBatch
	sb.txs = &spanBatchTxs{}
	sb.txs.totalLegacyTxCount = 0xFFFFFFFFFFFFFFFF

	r := bytes.NewReader([]byte{})
	err := sb.txs.decodeProtectedBits(r)
	require.ErrorIs(t, err, ErrTooBigSpanBatchSize)
}

// TestDecodeEip8130ProofTruncatedLengthPrefix locks the truncated-uvarint branch of
// decodeEip8130Proof: an empty reader yields no length prefix at all.
func TestDecodeEip8130ProofTruncatedLengthPrefix(t *testing.T) {
	_, err := decodeEip8130Proof(bytes.NewReader([]byte{}))
	require.ErrorContains(t, err, "failed to read eip8130 auth data length")
}

// TestDecodeEip8130ProofOversizedLength locks the fail-fast byte-cap branch: a length
// prefix above maxEip8130AuthProofBytes is rejected before any allocation is attempted.
func TestDecodeEip8130ProofOversizedLength(t *testing.T) {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], uint64(maxEip8130AuthProofBytes)+1)
	_, err := decodeEip8130Proof(bytes.NewReader(buf[:n]))
	require.ErrorIs(t, err, ErrTooBigSpanBatchSize)
}

// TestDecodeEip8130ProofTruncatedBody locks the io.ReadFull branch: the declared length
// is valid but the reader carries fewer bytes than the prefix promises.
func TestDecodeEip8130ProofTruncatedBody(t *testing.T) {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], 8)
	body := append(buf[:n], 0x01, 0x02) // declares 8 bytes, supplies 2
	_, err := decodeEip8130Proof(bytes.NewReader(body))
	require.ErrorContains(t, err, "failed to read eip8130 auth data")
}

// TestFullTxsNotEnoughEip8130AuthData locks the fullTxs guard that the per-tx
// eip8130_auth_data column must hold an entry for every 0x7B tx in txDatas. It takes a
// valid round-trippable span batch and drops the auth-data column.
func TestFullTxsNotEnoughEip8130AuthData(t *testing.T) {
	chainID := big.NewInt(8453)
	signer := types.NewIsthmusSigner(chainID)
	tx := testutils.RandomEip8130Tx(rand.New(rand.NewSource(1)), signer)
	raw, err := tx.MarshalBinary()
	require.NoError(t, err)

	sbt, err := newSpanBatchTxs([][]byte{raw}, chainID)
	require.NoError(t, err)
	require.NoError(t, sbt.recoverV(chainID))

	sbt.eip8130AuthData = nil
	_, err = sbt.fullTxs(chainID)
	require.ErrorContains(t, err, "not enough eip8130 auth data")
}

// roundTripSpanBatchEip8130 runs txs through the full span-batch tx codec: column
// encode, decode, recoverV and reconstruction, asserting each reconstructed tx is
// byte-identical to its original 2718 encoding.
func roundTripSpanBatchEip8130(t *testing.T, chainID *big.Int, txs []*types.Transaction) {
	t.Helper()
	var raw [][]byte
	for _, tx := range txs {
		b, err := tx.MarshalBinary()
		require.NoError(t, err)
		raw = append(raw, b)
	}

	sbt, err := newSpanBatchTxs(raw, chainID)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, sbt.encode(&buf))

	var sbt2 spanBatchTxs
	sbt2.totalBlockTxCount = sbt.totalBlockTxCount
	require.NoError(t, sbt2.decode(bytes.NewReader(buf.Bytes())))
	require.NoError(t, sbt2.recoverV(chainID))

	out, err := sbt2.fullTxs(chainID)
	require.NoError(t, err)
	require.Equal(t, raw, out)
}

// fixedKey returns a deterministic secp256k1 key derived from a single fixed byte, so
// legacy / 1559 / 7702 signatures are deterministic and reproducible for debugging
// (signing is RFC-6979 deterministic).
func fixedKey(t *testing.T, b byte) *ecdsa.PrivateKey {
	t.Helper()
	key, err := crypto.ToECDSA(bytes.Repeat([]byte{b}, 32))
	require.NoError(t, err)
	return key
}

func mustSignTx(t *testing.T, signer types.Signer, key *ecdsa.PrivateKey, txData types.TxData) *types.Transaction {
	t.Helper()
	tx, err := types.SignNewTx(key, signer, txData)
	require.NoError(t, err)
	return tx
}

// TestSpanBatchEip8130RoundTrip drives every EIP-8130 (0x7B) shape through the full
// span-batch tx codec (encode -> decode -> recoverV -> fullTxs) and asserts each tx is
// reconstructed byte-for-byte, both in isolation and interleaved with legacy/1559/7702
// txs to exercise column alignment.
func TestSpanBatchEip8130RoundTrip(t *testing.T) {
	chainID := big.NewInt(8453)
	signer := types.NewIsthmusSigner(chainID)
	mk := func(inner *types.Eip8130Tx) *types.Transaction {
		inner.ChainID = chainID
		return types.NewTx(inner)
	}

	eoaAuth := bytes.Repeat([]byte{0xa1}, 65) // EOA path: r||s||v blob
	auth20A := bytes.Repeat([]byte{0xb2}, 20) // 20-byte authenticator A
	auth20B := bytes.Repeat([]byte{0xc3}, 20) // 20-byte authenticator B
	proofA := []byte{0x01, 0x02, 0x03, 0x04}  // proof tail A
	proofB := []byte{0x05, 0x06, 0x07}        // proof tail B
	addrA := common.HexToAddress("0x000000000000000000000000000000000000aaaa")
	addrB := common.HexToAddress("0x000000000000000000000000000000000000bbbb")

	variants := []struct {
		name string
		tx   *types.Transaction
	}{
		{"eoa_self_pay", mk(&types.Eip8130Tx{
			NonceKey:      big.NewInt(0),
			NonceSequence: 7,
			Expiry:        100,
			GasTipCap:     big.NewInt(1),
			GasFeeCap:     big.NewInt(2),
			GasLimit:      21000,
			SenderAuth:    eoaAuth,
		})},
		{"configured_self_pay", mk(&types.Eip8130Tx{
			Sender:        &addrA,
			NonceKey:      big.NewInt(3),
			NonceSequence: 8,
			Expiry:        200,
			GasTipCap:     big.NewInt(5),
			GasFeeCap:     big.NewInt(9),
			GasLimit:      50000,
			SenderAuth:    append(append([]byte(nil), auth20A...), proofA...),
		})},
		{"configured_no_proof", mk(&types.Eip8130Tx{
			Sender:        &addrA,
			NonceKey:      big.NewInt(3),
			NonceSequence: 8,
			GasTipCap:     big.NewInt(5),
			GasFeeCap:     big.NewInt(9),
			GasLimit:      50000,
			SenderAuth:    append([]byte(nil), auth20A...),
		})},
		{"configured_payer", mk(&types.Eip8130Tx{
			Sender:        &addrA,
			NonceKey:      big.NewInt(4),
			NonceSequence: 9,
			Expiry:        300,
			GasTipCap:     big.NewInt(6),
			GasFeeCap:     big.NewInt(10),
			GasLimit:      60000,
			Payer:         &addrB,
			SenderAuth:    append(append([]byte(nil), auth20A...), proofA...),
			PayerAuth:     append(append([]byte(nil), auth20B...), proofB...),
		})},
		{"account_changes", mk(&types.Eip8130Tx{
			NonceKey:      big.NewInt(11),
			NonceSequence: 12,
			Expiry:        400,
			GasTipCap:     big.NewInt(7),
			GasFeeCap:     big.NewInt(13),
			GasLimit:      70000,
			AccountChanges: []types.AccountChange{
				{Create: &types.CreateEntry{
					UserSalt: common.Hash{0x22},
					Code:     []byte{0x60, 0x80, 0x60, 0x40},
					InitialActors: []types.InitialActor{
						{ActorID: common.Hash{0x33}, Authenticator: common.Address{0xbb}},
						{ActorID: common.Hash{0x34}, Authenticator: common.Address{0xbc}},
					},
				}},
				{ConfigChange: &types.ConfigChange{
					ChainID:  8453,
					Sequence: 5,
					ActorChanges: []types.ActorChange{
						{ChangeType: types.ActorChangeAuthorize, ActorID: common.Hash{0x41}, Data: []byte{0xaa, 0xbb}},
						{ChangeType: types.ActorChangeRevoke, ActorID: common.Hash{0x42}},
					},
					Auth: []byte{0xde, 0xad, 0xbe, 0xef},
				}},
				{Delegation: &types.Delegation{Target: common.Address{0xdd}}},
			},
			Calls: [][]types.Call{
				{
					{To: common.Address{0xaa}, Data: []byte{}},
					{To: common.Address{0xab}, Data: []byte{0xde, 0xad, 0xbe, 0xef}},
				},
				{
					{To: common.Address{0xac}, Data: []byte{0x01}},
				},
			},
			Metadata:   []byte{0x09, 0x08, 0x07},
			SenderAuth: eoaAuth,
		})},
		{"minimal", mk(&types.Eip8130Tx{
			NonceKey:      big.NewInt(0),
			NonceSequence: 0,
			GasTipCap:     big.NewInt(0),
			GasFeeCap:     big.NewInt(0),
			GasLimit:      21000,
		})},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			roundTripSpanBatchEip8130(t, chainID, []*types.Transaction{v.tx})
		})
	}

	legacyTx := mustSignTx(t, signer, fixedKey(t, 0x11), &types.LegacyTx{
		Nonce:    3,
		GasPrice: big.NewInt(1_000_000_000),
		Gas:      21000,
		To:       ptr(common.HexToAddress("0x00000000000000000000000000000000000000ff")),
		Value:    big.NewInt(12345),
		Data:     []byte{0xca, 0xfe},
	})
	dynTx := mustSignTx(t, signer, fixedKey(t, 0x12), &types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     4,
		GasTipCap: big.NewInt(2_000_000_000),
		GasFeeCap: big.NewInt(20_000_000_000),
		Gas:       30000,
		To:        ptr(common.HexToAddress("0x00000000000000000000000000000000000000ee")),
		Value:     big.NewInt(67890),
		Data:      []byte{0xbe, 0xef},
	})
	setCodeAuth, err := types.SignSetCode(fixedKey(t, 0x13), types.SetCodeAuthorization{
		ChainID: *uint256.NewInt(8453),
		Address: common.HexToAddress("0x00000000000000000000000000000000000000dd"),
		Nonce:   1,
	})
	require.NoError(t, err)
	setCodeTx := mustSignTx(t, signer, fixedKey(t, 0x14), &types.SetCodeTx{
		ChainID:   uint256.NewInt(8453),
		Nonce:     5,
		GasTipCap: uint256.NewInt(3_000_000_000),
		GasFeeCap: uint256.NewInt(30_000_000_000),
		Gas:       40000,
		To:        common.HexToAddress("0x00000000000000000000000000000000000000cc"),
		Value:     uint256.NewInt(111),
		Data:      []byte{0xab, 0xcd},
		AuthList:  []types.SetCodeAuthorization{setCodeAuth},
	})

	t.Run("mixed_batch", func(t *testing.T) {
		roundTripSpanBatchEip8130(t, chainID, []*types.Transaction{
			legacyTx, variants[0].tx, dynTx, variants[3].tx, setCodeTx,
		})
	})
}
