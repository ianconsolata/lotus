package messagesigner

import (
	"bytes"
	"context"

	"github.com/filecoin-project/lotus/chain/wallet"

	"github.com/filecoin-project/lotus/chain/messagepool"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/node/modules/dtypes"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	cbg "github.com/whyrusleeping/cbor-gen"
	"golang.org/x/xerrors"
)

const dsKeyActorNonce = "ActorNonce"

type mpoolAPI interface {
	GetNonce(address.Address) (uint64, error)
}

// MessageSigner keeps track of nonces per address, and increments the nonce
// when signing a message
type MessageSigner struct {
	wallet *wallet.Wallet
	mpool  mpoolAPI
	ds     datastore.Batching
}

func NewMessageSigner(wallet *wallet.Wallet, mpool *messagepool.MessagePool, ds dtypes.MetadataDS) *MessageSigner {
	return newMessageSigner(wallet, mpool, ds)
}

func newMessageSigner(wallet *wallet.Wallet, mpool mpoolAPI, ds dtypes.MetadataDS) *MessageSigner {
	ds = namespace.Wrap(ds, datastore.NewKey("/message-signer/"))
	return &MessageSigner{
		wallet: wallet,
		mpool:  mpool,
		ds:     ds,
	}
}

// SignMessage increments the nonce for the message From address, and signs
// the message
func (ms *MessageSigner) SignMessage(ctx context.Context, msg *types.Message) (*types.SignedMessage, error) {
	nonce, err := ms.nextNonce(msg.From)
	if err != nil {
		return nil, xerrors.Errorf("failed to create nonce: %w", err)
	}

	msg.Nonce = nonce
	sig, err := ms.wallet.Sign(ctx, msg.From, msg.Cid().Bytes())
	if err != nil {
		return nil, xerrors.Errorf("failed to sign message: %w", err)
	}

	return &types.SignedMessage{
		Message:   *msg,
		Signature: *sig,
	}, nil
}

// nextNonce increments the nonce.
// If there is no nonce in the datastore, gets the nonce from the message pool.
func (ms *MessageSigner) nextNonce(addr address.Address) (uint64, error) {
	addrNonceKey := datastore.KeyWithNamespaces([]string{dsKeyActorNonce, addr.String()})

	// Get the nonce for this address from the datastore
	nonceBytes, err := ms.ds.Get(addrNonceKey)

	var nonce uint64
	switch {
	case xerrors.Is(err, datastore.ErrNotFound):
		// If a nonce for this address hasn't yet been created in the
		// datastore, check the mempool - nonces used to be created by
		// the mempool so we need to support nodes that still have mempool
		// nonces. Note that the mempool returns the actor state's nonce by
		// default.
		nonce, err = ms.mpool.GetNonce(addr)
		if err != nil {
			return 0, xerrors.Errorf("failed to get nonce from mempool: %w", err)
		}

	case err != nil:
		return 0, xerrors.Errorf("failed to get nonce from datastore: %w", err)

	default:
		// There is a nonce in the mempool, so unmarshall and increment it
		maj, val, err := cbg.CborReadHeader(bytes.NewReader(nonceBytes))
		if err != nil {
			return 0, xerrors.Errorf("failed to parse nonce from datastore: %w", err)
		}
		if maj != cbg.MajUnsignedInt {
			return 0, xerrors.Errorf("bad cbor type parsing nonce from datastore")
		}

		nonce = val + 1
	}

	// Write the nonce for this address to the datastore
	buf := bytes.Buffer{}
	_, err = buf.Write(cbg.CborEncodeMajorType(cbg.MajUnsignedInt, nonce))
	if err != nil {
		return 0, xerrors.Errorf("failed to marshall nonce: %w", err)
	}
	err = ms.ds.Put(addrNonceKey, buf.Bytes())
	if err != nil {
		return 0, xerrors.Errorf("failed to write nonce to datastore: %w", err)
	}

	return nonce, nil
}
