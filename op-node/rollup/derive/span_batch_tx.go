package derive

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

type spanBatchTxData interface {
	txType() byte // returns the type ID
}

type spanBatchTx struct {
	inner spanBatchTxData
}

type spanBatchLegacyTxData struct {
	Value    *big.Int // wei amount
	GasPrice *big.Int // wei per gas
	Data     []byte
}

func (txData *spanBatchLegacyTxData) txType() byte { return types.LegacyTxType }

type spanBatchAccessListTxData struct {
	Value      *big.Int // wei amount
	GasPrice   *big.Int // wei per gas
	Data       []byte
	AccessList types.AccessList // EIP-2930 access list
}

func (txData *spanBatchAccessListTxData) txType() byte { return types.AccessListTxType }

type spanBatchDynamicFeeTxData struct {
	Value      *big.Int
	GasTipCap  *big.Int // a.k.a. maxPriorityFeePerGas
	GasFeeCap  *big.Int // a.k.a. maxFeePerGas
	Data       []byte
	AccessList types.AccessList
}

func (txData *spanBatchDynamicFeeTxData) txType() byte { return types.DynamicFeeTxType }

type spanBatchSetCodeTxData struct {
	Value             *uint256.Int
	GasTipCap         *uint256.Int // a.k.a. maxPriorityFeePerGas
	GasFeeCap         *uint256.Int // a.k.a. maxFeePerGas
	Data              []byte
	AccessList        types.AccessList
	AuthorizationList []types.SetCodeAuthorization
}

func (txData *spanBatchSetCodeTxData) txType() byte { return types.SetCodeTxType }

type spanBatchEip8130TxData struct {
	// Low-entropy remainder, RLP-encoded into the span-batch tx_data column. ChainID
	// is dropped and reinjected from the batch chainID; nonce/gas come from the shared
	// nonce/gas columns; to/value/v/r/s are always nil/zero for EIP-8130.
	Sender              *common.Address `rlp:"nil"` // nil means the empty EOA path
	NonceKey            *big.Int
	Expiry              uint64
	GasTipCap           *big.Int
	GasFeeCap           *big.Int
	Payer               *common.Address `rlp:"nil"` // nil means self-pay
	AccountChanges      []types.AccountChange
	Calls               [][]types.Call
	Metadata            []byte
	SenderAuthenticator []byte // 20-byte account address on the configured path, empty on EOA
	PayerAuthenticator  []byte // 20-byte account address on the configured path, empty on EOA

	// High-entropy auth proofs, carried in the separate eip8130_auth_data column.
	// Unexported so RLP does not serialize them into the tx_data column.
	senderProof []byte
	payerProof  []byte
}

func (txData *spanBatchEip8130TxData) txType() byte { return types.Eip8130TxType }

// splitEip8130Auth splits an EIP-8130 auth blob into the low-entropy authenticator
// (the leading 20-byte account address, present only on the configured path) and the
// high-entropy proof bytes. On the EOA path (configured false) the whole blob is proof.
// On the configured path the leading authenticator is expected to be 20 bytes; this runs
// on the encode side over engine-validated txs, but the length is guarded defensively to
// avoid a panic on malformed input (mirroring the Rust split_auth check).
func splitEip8130Auth(auth []byte, configured bool) (authenticator, proof []byte, err error) {
	if configured {
		if len(auth) < common.AddressLength {
			return nil, nil, fmt.Errorf("eip8130 auth data too short: got %d, want >= %d", len(auth), common.AddressLength)
		}
		// Normalize an empty proof tail to nil so the encode-side proof matches the
		// decode-side proof (decodeEip8130Proof returns nil for a zero-length column),
		// keeping a round-tripped spanBatchTxs deeply equal. Wire bytes are identical:
		// an empty proof encodes to a single uvarint 0 either way.
		if len(auth) == common.AddressLength {
			return auth[:common.AddressLength], nil, nil
		}
		return auth[:common.AddressLength], auth[common.AddressLength:], nil
	}
	if len(auth) == 0 {
		return nil, nil, nil
	}
	return nil, auth, nil
}

// joinEip8130Auth reassembles an EIP-8130 auth blob from its authenticator and proof
// parts. On the configured path the authenticator precedes the proof; on the EOA path
// the blob is the proof alone.
func joinEip8130Auth(authenticator, proof []byte, configured bool) []byte {
	if !configured {
		return proof
	}
	if len(proof) == 0 {
		return authenticator
	}
	return append(append([]byte(nil), authenticator...), proof...)
}

// Type returns the transaction type.
func (tx *spanBatchTx) Type() uint8 {
	return tx.inner.txType()
}

// encodeTyped writes the canonical encoding of a typed transaction to w.
func (tx *spanBatchTx) encodeTyped(w *bytes.Buffer) error {
	w.WriteByte(tx.Type())
	return rlp.Encode(w, tx.inner)
}

// MarshalBinary returns the canonical encoding of the transaction.
// For legacy transactions, it returns the RLP encoding. For EIP-2718 typed
// transactions, it returns the type and payload.
func (tx *spanBatchTx) MarshalBinary() ([]byte, error) {
	if tx.Type() == types.LegacyTxType {
		return rlp.EncodeToBytes(tx.inner)
	}
	var buf bytes.Buffer
	err := tx.encodeTyped(&buf)
	return buf.Bytes(), err
}

// setDecoded sets the inner transaction after decoding.
func (tx *spanBatchTx) setDecoded(inner spanBatchTxData, size uint64) {
	tx.inner = inner
}

// decodeTyped decodes a typed transaction from the canonical format.
func (tx *spanBatchTx) decodeTyped(b []byte) (spanBatchTxData, error) {
	if len(b) <= 1 {
		return nil, fmt.Errorf("failed to decode span batch: %w", ErrTypedTxTooShort)
	}
	switch b[0] {
	case types.AccessListTxType:
		var inner spanBatchAccessListTxData
		err := rlp.DecodeBytes(b[1:], &inner)
		if err != nil {
			return nil, fmt.Errorf("failed to decode spanBatchAccessListTxData: %w", err)
		}
		return &inner, nil
	case types.DynamicFeeTxType:
		var inner spanBatchDynamicFeeTxData
		err := rlp.DecodeBytes(b[1:], &inner)
		if err != nil {
			return nil, fmt.Errorf("failed to decode spanBatchDynamicFeeTxData: %w", err)
		}
		return &inner, nil
	case types.SetCodeTxType:
		var inner spanBatchSetCodeTxData
		err := rlp.DecodeBytes(b[1:], &inner)
		if err != nil {
			return nil, fmt.Errorf("failed to decode spanBatchSetCodeTxData: %w", err)
		}
		return &inner, nil
	case types.Eip8130TxType:
		var inner spanBatchEip8130TxData
		err := rlp.DecodeBytes(b[1:], &inner)
		if err != nil {
			return nil, fmt.Errorf("failed to decode spanBatchEip8130TxData: %w", err)
		}
		return &inner, nil
	default:
		return nil, types.ErrTxTypeNotSupported
	}
}

// UnmarshalBinary decodes the canonical encoding of transactions.
// It supports legacy RLP transactions and EIP2718 typed transactions.
func (tx *spanBatchTx) UnmarshalBinary(b []byte) error {
	if len(b) > 0 && b[0] > 0x7f {
		// It's a legacy transaction.
		var data spanBatchLegacyTxData
		err := rlp.DecodeBytes(b, &data)
		if err != nil {
			return fmt.Errorf("failed to decode spanBatchLegacyTxData: %w", err)
		}
		tx.setDecoded(&data, uint64(len(b)))
		return nil
	}
	// It's an EIP2718 typed transaction envelope.
	inner, err := tx.decodeTyped(b)
	if err != nil {
		return err
	}
	tx.setDecoded(inner, uint64(len(b)))
	return nil
}

// convertToFullTx takes values and convert spanBatchTx to types.Transaction
func (tx *spanBatchTx) convertToFullTx(nonce, gas uint64, to *common.Address, chainID, V, R, S *big.Int) (*types.Transaction, error) {
	var inner types.TxData
	switch tx.Type() {
	case types.LegacyTxType:
		batchTxInner := tx.inner.(*spanBatchLegacyTxData)
		inner = &types.LegacyTx{
			Nonce:    nonce,
			GasPrice: batchTxInner.GasPrice,
			Gas:      gas,
			To:       to,
			Value:    batchTxInner.Value,
			Data:     batchTxInner.Data,
			V:        V,
			R:        R,
			S:        S,
		}
	case types.AccessListTxType:
		batchTxInner := tx.inner.(*spanBatchAccessListTxData)
		inner = &types.AccessListTx{
			ChainID:    chainID,
			Nonce:      nonce,
			GasPrice:   batchTxInner.GasPrice,
			Gas:        gas,
			To:         to,
			Value:      batchTxInner.Value,
			Data:       batchTxInner.Data,
			AccessList: batchTxInner.AccessList,
			V:          V,
			R:          R,
			S:          S,
		}
	case types.DynamicFeeTxType:
		batchTxInner := tx.inner.(*spanBatchDynamicFeeTxData)
		inner = &types.DynamicFeeTx{
			ChainID:    chainID,
			Nonce:      nonce,
			GasTipCap:  batchTxInner.GasTipCap,
			GasFeeCap:  batchTxInner.GasFeeCap,
			Gas:        gas,
			To:         to,
			Value:      batchTxInner.Value,
			Data:       batchTxInner.Data,
			AccessList: batchTxInner.AccessList,
			V:          V,
			R:          R,
			S:          S,
		}
	case types.SetCodeTxType:
		if to == nil {
			return nil, fmt.Errorf("to address is required for SetCodeTx")
		}

		setCodeTxInner := tx.inner.(*spanBatchSetCodeTxData)
		inner = &types.SetCodeTx{
			ChainID:    uint256.MustFromBig(chainID),
			Nonce:      nonce,
			GasTipCap:  setCodeTxInner.GasTipCap,
			GasFeeCap:  setCodeTxInner.GasFeeCap,
			Gas:        gas,
			To:         *to,
			Value:      setCodeTxInner.Value,
			Data:       setCodeTxInner.Data,
			AccessList: setCodeTxInner.AccessList,
			AuthList:   setCodeTxInner.AuthorizationList,
			V:          uint256.MustFromBig(V),
			R:          uint256.MustFromBig(R),
			S:          uint256.MustFromBig(S),
		}
	case types.Eip8130TxType:
		batchTxInner := tx.inner.(*spanBatchEip8130TxData)
		inner = &types.Eip8130Tx{
			ChainID:        chainID,
			Sender:         batchTxInner.Sender,
			NonceKey:       batchTxInner.NonceKey,
			NonceSequence:  nonce,
			Expiry:         batchTxInner.Expiry,
			GasTipCap:      batchTxInner.GasTipCap,
			GasFeeCap:      batchTxInner.GasFeeCap,
			GasLimit:       gas,
			AccountChanges: batchTxInner.AccountChanges,
			Calls:          batchTxInner.Calls,
			Metadata:       batchTxInner.Metadata,
			Payer:          batchTxInner.Payer,
			SenderAuth:     joinEip8130Auth(batchTxInner.SenderAuthenticator, batchTxInner.senderProof, batchTxInner.Sender != nil),
			PayerAuth:      joinEip8130Auth(batchTxInner.PayerAuthenticator, batchTxInner.payerProof, batchTxInner.Payer != nil),
		}
	default:
		return nil, fmt.Errorf("invalid tx type: %d", tx.Type())
	}
	return types.NewTx(inner), nil
}

// newSpanBatchTx converts types.Transaction to spanBatchTx
func newSpanBatchTx(tx *types.Transaction) (*spanBatchTx, error) {
	var inner spanBatchTxData
	switch tx.Type() {
	case types.LegacyTxType:
		inner = &spanBatchLegacyTxData{
			GasPrice: tx.GasPrice(),
			Value:    tx.Value(),
			Data:     tx.Data(),
		}
	case types.AccessListTxType:
		inner = &spanBatchAccessListTxData{
			GasPrice:   tx.GasPrice(),
			Value:      tx.Value(),
			Data:       tx.Data(),
			AccessList: tx.AccessList(),
		}
	case types.DynamicFeeTxType:
		inner = &spanBatchDynamicFeeTxData{
			GasTipCap:  tx.GasTipCap(),
			GasFeeCap:  tx.GasFeeCap(),
			Value:      tx.Value(),
			Data:       tx.Data(),
			AccessList: tx.AccessList(),
		}
	case types.SetCodeTxType:
		inner = &spanBatchSetCodeTxData{
			GasTipCap:         uint256.MustFromBig(tx.GasTipCap()),
			GasFeeCap:         uint256.MustFromBig(tx.GasFeeCap()),
			Value:             uint256.MustFromBig(tx.Value()),
			Data:              tx.Data(),
			AccessList:        tx.AccessList(),
			AuthorizationList: tx.SetCodeAuthorizations(),
		}
	case types.Eip8130TxType:
		e := tx.Eip8130()
		senderAuthenticator, _, err := splitEip8130Auth(e.SenderAuth, e.Sender != nil)
		if err != nil {
			return nil, fmt.Errorf("failed to split eip8130 sender auth: %w", err)
		}
		payerAuthenticator, _, err := splitEip8130Auth(e.PayerAuth, e.Payer != nil)
		if err != nil {
			return nil, fmt.Errorf("failed to split eip8130 payer auth: %w", err)
		}
		inner = &spanBatchEip8130TxData{
			Sender:              e.Sender,
			NonceKey:            e.NonceKey,
			Expiry:              e.Expiry,
			GasTipCap:           e.GasTipCap,
			GasFeeCap:           e.GasFeeCap,
			Payer:               e.Payer,
			AccountChanges:      e.AccountChanges,
			Calls:               e.Calls,
			Metadata:            e.Metadata,
			SenderAuthenticator: senderAuthenticator,
			PayerAuthenticator:  payerAuthenticator,
		}
	default:
		return nil, fmt.Errorf("invalid tx type: %d", tx.Type())
	}
	return &spanBatchTx{inner: inner}, nil
}
