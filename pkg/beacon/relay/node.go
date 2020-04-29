package relay

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"

	bn256 "github.com/ethereum/go-ethereum/crypto/bn256/cloudflare"
	"github.com/keep-network/keep-core/pkg/altbn128"

	relayChain "github.com/keep-network/keep-core/pkg/beacon/relay/chain"
	"github.com/keep-network/keep-core/pkg/beacon/relay/group"

	relaychain "github.com/keep-network/keep-core/pkg/beacon/relay/chain"
	"github.com/keep-network/keep-core/pkg/beacon/relay/config"
	"github.com/keep-network/keep-core/pkg/beacon/relay/dkg"
	"github.com/keep-network/keep-core/pkg/beacon/relay/groupselection"
	"github.com/keep-network/keep-core/pkg/beacon/relay/registry"
	"github.com/keep-network/keep-core/pkg/chain"
	"github.com/keep-network/keep-core/pkg/net"
)

// Node represents the current state of a relay node.
type Node struct {
	mutex sync.Mutex

	// Staker is an on-chain identity that this node is using to prove its
	// stake in the system.
	Staker chain.Staker

	// External interactors.
	netProvider  net.Provider
	blockCounter chain.BlockCounter
	chainConfig  *config.Chain

	groupRegistry *registry.Groups
}

// IsInGroup checks if this node is a member of the group which was selected to
// join a group which undergoes the process of generating a threshold relay entry.
func (n *Node) IsInGroup(groupPublicKey []byte) bool {
	return len(n.groupRegistry.GetGroup(groupPublicKey)) > 0
}

// JoinGroupIfEligible takes a threshold relay entry value and undergoes the
// process of joining a group if this node's virtual stakers prove eligible for
// the group generated by that entry. This is an interactive on-chain process,
// and JoinGroupIfEligible can block for an extended period of time while it
// completes the on-chain operation.
//
// Indirectly, the completion of the process is signaled by the formation of an
// on-chain group containing at least one of this node's virtual stakers.
func (n *Node) JoinGroupIfEligible(
	relayChain relaychain.Interface,
	signing chain.Signing,
	groupSelectionResult *groupselection.Result,
	newEntry *big.Int,
) {
	dkgStartBlockHeight := groupSelectionResult.GroupSelectionEndBlock

	if len(groupSelectionResult.SelectedStakers) > maxGroupSize {
		logger.Errorf(
			"group size larger than supported: [%v]",
			len(groupSelectionResult.SelectedStakers),
		)
		return
	}

	indexes := make([]uint8, 0)
	for index, selectedStaker := range groupSelectionResult.SelectedStakers {
		// See if we are amongst those chosen
		if bytes.Compare(selectedStaker, n.Staker.Address()) == 0 {
			indexes = append(indexes, uint8(index))
		}
	}

	// create temporary broadcast channel name for DKG using the
	// group selection seed
	channelName := newEntry.Text(16)

	if len(indexes) > 0 {
		broadcastChannel, err := n.netProvider.BroadcastChannelFor(channelName)
		if err != nil {
			logger.Errorf("failed to get broadcast channel: [%v]", err)
			return
		}

		membershipValidator := group.NewStakersMembershipValidator(
			groupSelectionResult.SelectedStakers,
			signing,
		)

		err = broadcastChannel.SetFilter(membershipValidator.IsInGroup)
		if err != nil {
			logger.Errorf(
				"could not set filter for channel [%v]: [%v]",
				broadcastChannel.Name(),
				err,
			)
		}

		for _, index := range indexes {
			// capture player index for goroutine
			playerIndex := index

			go func() {
				signer, err := dkg.ExecuteDKG(
					newEntry,
					playerIndex,
					n.chainConfig.GroupSize,
					n.chainConfig.DishonestThreshold(),
					membershipValidator,
					dkgStartBlockHeight,
					n.blockCounter,
					relayChain,
					signing,
					broadcastChannel,
				)
				if err != nil {
					logger.Errorf("failed to execute dkg: [%v]", err)
					return
				}

				// final broadcast channel name for group is the compressed
				// public key of the group
				channelName := hex.EncodeToString(
					signer.GroupPublicKeyBytesCompressed(),
				)

				err = n.groupRegistry.RegisterGroup(signer, channelName)
				if err != nil {
					logger.Errorf("failed to register a group: [%v]", err)
				}

				logger.Infof(
					"[member:%v] ready to operate in the group",
					signer.MemberID(),
				)
			}()
		}
	}

	return
}

// ForwardSignatureShares enables the ability to forward signature shares
// messages to other nodes even if this node is not a part of the group which
// signs the relay entry.
func (n *Node) ForwardSignatureShares(groupPublicKeyBytes []byte) {
	name, err := channelNameForPublicKeyBytes(groupPublicKeyBytes)
	if err != nil {
		logger.Warningf("could not forward signature shares: [%v]", err)
		return
	}

	n.netProvider.BroadcastChannelForwarderFor(name)
}

// ResumeSigningIfEligible enables a client to rejoin the ongoing signing process
// after it was crashed or restarted and if it belongs to the signing group.
func (n *Node) ResumeSigningIfEligible(
	relayChain relayChain.Interface,
	signing chain.Signing,
) {
	isEntryInProgress, err := relayChain.IsEntryInProgress()
	if err != nil {
		logger.Errorf(
			"failed checking if an entry is in progress: [%v]",
			err,
		)
		return
	}

	if isEntryInProgress {
		previousEntry, err := relayChain.CurrentRequestPreviousEntry()
		if err != nil {
			logger.Errorf(
				"failed to get a previous entry for the current request: [%v]",
				err,
			)
			return
		}
		entryStartBlock, err := relayChain.CurrentRequestStartBlock()
		if err != nil {
			logger.Errorf(
				"failed to get a start block for the current request: [%v]",
				err,
			)
			return
		}
		groupPublicKey, err := relayChain.CurrentRequestGroupPublicKey()
		if err != nil {
			logger.Errorf(
				"failed to get a group public key for the current request: [%v]",
				err,
			)
			return
		}

		logger.Infof(
			"atempting to rejoin the current signing processs [0x%x]",
			groupPublicKey,
		)
		n.GenerateRelayEntry(
			previousEntry,
			relayChain,
			signing,
			groupPublicKey,
			entryStartBlock.Uint64(),
		)
	}
}

// channelNameForPublicKey takes group public key represented by marshalled
// G2 point and transforms it into a broadcast channel name.
// Broadcast channel name for group is the hexadecimal representation of
// compressed public key of the group.
func channelNameForPublicKeyBytes(groupPublicKey []byte) (string, error) {
	g2 := new(bn256.G2)

	if _, err := g2.Unmarshal(groupPublicKey); err != nil {
		return "", fmt.Errorf("could not create channel name: [%v]", err)
	}

	return channelNameForPublicKey(g2), nil
}

// channelNameForPublicKey takes group public key represented by G2 point
// and transforms it into a broadcast channel name.
// Broadcast channel name for group is the hexadecimal representation of
// compressed public key of the group.
func channelNameForPublicKey(groupPublicKey *bn256.G2) string {
	altbn128GroupPublicKey := altbn128.G2Point{G2: groupPublicKey}
	return hex.EncodeToString(altbn128GroupPublicKey.Compress())
}
