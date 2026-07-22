package derive

import (
	"bytes"
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum-optimism/optimism/op-service/testutils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type spanBatchTxTest struct {
	name      string
	trials    int
	mkTx      func(rng *rand.Rand, signer types.Signer) *types.Transaction
	protected bool
}

func TestSpanBatchTxConvert(t *testing.T) {
	cases := []spanBatchTxTest{
		{"unprotected legacy tx", 32, testutils.RandomLegacyTx, false},
		{"legacy tx", 32, testutils.RandomLegacyTx, true},
		{"access list tx", 32, testutils.RandomAccessListTx, true},
		{"dynamic fee tx", 32, testutils.RandomDynamicFeeTx, true},
		{"setcode tx", 32, testutils.RandomSetCodeTx, true},
	}

	for i, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(0x1331 + i)))
			chainID := big.NewInt(rng.Int63n(1000))
			signer := types.NewIsthmusSigner(chainID)
			if !testCase.protected {
				signer = types.HomesteadSigner{}
			}

			for txIdx := 0; txIdx < testCase.trials; txIdx++ {
				tx := testCase.mkTx(rng, signer)

				v, r, s := tx.RawSignatureValues()
				sbtx, err := newSpanBatchTx(tx)
				require.NoError(t, err)

				tx2, err := sbtx.convertToFullTx(tx.Nonce(), tx.Gas(), tx.To(), chainID, v, r, s)
				require.NoError(t, err)

				// compare after marshal because we only need inner field of transaction
				txEncoded, err := tx.MarshalBinary()
				require.NoError(t, err)
				tx2Encoded, err := tx2.MarshalBinary()
				require.NoError(t, err)

				assert.Equal(t, txEncoded, tx2Encoded)
			}
		})
	}
}

func TestSpanBatchTxRoundTrip(t *testing.T) {
	cases := []spanBatchTxTest{
		{"unprotected legacy tx", 32, testutils.RandomLegacyTx, false},
		{"legacy tx", 32, testutils.RandomLegacyTx, true},
		{"access list tx", 32, testutils.RandomAccessListTx, true},
		{"dynamic fee tx", 32, testutils.RandomDynamicFeeTx, true},
		{"setcode tx", 32, testutils.RandomSetCodeTx, true},
	}

	for i, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(0x1332 + i)))
			chainID := big.NewInt(rng.Int63n(1000))
			signer := types.NewIsthmusSigner(chainID)
			if !testCase.protected {
				signer = types.HomesteadSigner{}
			}

			for txIdx := 0; txIdx < testCase.trials; txIdx++ {
				tx := testCase.mkTx(rng, signer)

				sbtx, err := newSpanBatchTx(tx)
				require.NoError(t, err)

				sbtxEncoded, err := sbtx.MarshalBinary()
				require.NoError(t, err)

				var sbtx2 spanBatchTx
				err = sbtx2.UnmarshalBinary(sbtxEncoded)
				require.NoError(t, err)

				assert.Equal(t, sbtx, &sbtx2)
			}
		})
	}
}

type spanBatchDummyTxData struct{}

func (txData *spanBatchDummyTxData) txType() byte { return types.DepositTxType }

func TestSpanBatchTxInvalidTxType(t *testing.T) {
	// span batch never contain deposit tx
	depositTx := types.NewTx(&types.DepositTx{})
	_, err := newSpanBatchTx(depositTx)
	require.ErrorContains(t, err, "invalid tx type")

	var sbtx spanBatchTx
	sbtx.inner = &spanBatchDummyTxData{}
	_, err = sbtx.convertToFullTx(0, 0, nil, nil, nil, nil, nil)
	require.ErrorContains(t, err, "invalid tx type")
}

func TestSpanBatchTxDecodeInvalid(t *testing.T) {
	var sbtx spanBatchTx
	_, err := sbtx.decodeTyped([]byte{})
	require.ErrorIs(t, err, ErrTypedTxTooShort)

	tx := types.NewTx(&types.LegacyTx{})
	txEncoded, err := tx.MarshalBinary()
	require.NoError(t, err)

	// legacy tx is not typed tx
	_, err = sbtx.decodeTyped(txEncoded)
	require.EqualError(t, err, types.ErrTxTypeNotSupported.Error())

	tx2 := types.NewTx(&types.AccessListTx{})
	tx2Encoded, err := tx2.MarshalBinary()
	require.NoError(t, err)

	tx2Encoded[0] = types.DynamicFeeTxType
	_, err = sbtx.decodeTyped(tx2Encoded)
	require.ErrorContains(t, err, "failed to decode spanBatchDynamicFeeTxData")

	tx3 := types.NewTx(&types.DynamicFeeTx{})
	tx3Encoded, err := tx3.MarshalBinary()
	require.NoError(t, err)

	tx3Encoded[0] = types.AccessListTxType
	_, err = sbtx.decodeTyped(tx3Encoded)
	require.ErrorContains(t, err, "failed to decode spanBatchAccessListTxData")

	invalidLegacyTxDecoded := []byte{0xFF, 0xFF}
	err = sbtx.UnmarshalBinary(invalidLegacyTxDecoded)
	require.ErrorContains(t, err, "failed to decode spanBatchLegacyTxData")
}

func TestSpanBatchTxSetCodeInvalidTo(t *testing.T) {
	// invalid to for setcode tx
	var sbtx spanBatchTx
	sbtx.inner = &spanBatchSetCodeTxData{}
	_, err := sbtx.convertToFullTx(0, 0, nil, nil, nil, nil, nil)
	require.ErrorContains(t, err, "to address is required for SetCodeTx")
}

// TestSpanBatchTxEip8130AuthBinding locks the decode-side invariant that ties an actor's
// presence to the length of its authenticator column: a configured actor must carry a
// 20-byte authenticator and an EOA / self-pay actor must carry none. Each case breaks
// exactly one binding, so decodeTyped must reject it.
func TestSpanBatchTxEip8130AuthBinding(t *testing.T) {
	addr := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	auth20 := bytes.Repeat([]byte{0x01}, common.AddressLength)

	// Configured sender, self-pay, with a valid 20-byte sender authenticator.
	base := spanBatchEip8130TxData{
		NonceKey:            big.NewInt(0),
		GasTipCap:           big.NewInt(0),
		GasFeeCap:           big.NewInt(0),
		Sender:              &addr,
		SenderAuthenticator: auth20,
	}
	encode := func(inner spanBatchEip8130TxData) []byte {
		var buf bytes.Buffer
		buf.WriteByte(types.Eip8130TxType)
		require.NoError(t, rlp.Encode(&buf, &inner))
		return buf.Bytes()
	}

	var sbtx spanBatchTx

	// Sanity: the untampered, consistent tx decodes cleanly.
	_, err := sbtx.decodeTyped(encode(base))
	require.NoError(t, err)

	// Configured sender must carry a 20-byte authenticator, not empty.
	senderEmpty := base
	senderEmpty.SenderAuthenticator = nil
	_, err = sbtx.decodeTyped(encode(senderEmpty))
	require.ErrorContains(t, err, "eip8130 sender authenticator")

	// Configured sender must carry exactly 20 bytes, not more.
	senderLong := base
	senderLong.SenderAuthenticator = bytes.Repeat([]byte{0x01}, common.AddressLength+1)
	_, err = sbtx.decodeTyped(encode(senderLong))
	require.ErrorContains(t, err, "eip8130 sender authenticator")

	// EOA sender (nil) must carry an empty authenticator.
	senderEOA := base
	senderEOA.Sender = nil
	_, err = sbtx.decodeTyped(encode(senderEOA))
	require.ErrorContains(t, err, "eip8130 sender authenticator")

	// Absent payer must carry an empty authenticator.
	payerSet := base
	payerSet.PayerAuthenticator = auth20
	_, err = sbtx.decodeTyped(encode(payerSet))
	require.ErrorContains(t, err, "eip8130 payer authenticator")
}
