package relay

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/big"
	"os"
	"sync"

	relaychain "github.com/keep-network/keep-core/pkg/beacon/relay/chain"
	"github.com/keep-network/keep-core/pkg/beacon/relay/config"
	"github.com/keep-network/keep-core/pkg/beacon/relay/dkg2"
	"github.com/keep-network/keep-core/pkg/beacon/relay/groupselection"
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

	// The IDs of the known stakes in the system, including this node's StakeID.
	stakeIDs      []string
	maxStakeIndex int

	groupPublicKeys [][]byte
	seenPublicKeys  map[string]bool
	myGroups        map[string][]*membership
	pendingGroups   map[string][]*membership
}

type membership struct {
	member  *dkg2.ThresholdSigner
	channel net.BroadcastChannel
	index   int
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
	groupSelectionResult *groupselection.Result,
	entryRequestID *big.Int,
	entrySeed *big.Int,
) {
	if !n.initializePendingGroup(entryRequestID.String()) {
		// Failed to initialize; in progress for this entry.
		return
	}
	// Release control of this group if we error.
	defer n.flushPendingGroup(entryRequestID.String())

	for index, ticket := range groupSelectionResult.SelectedTickets {
		// If our ticket is amongst those chosen, kick
		// off an instance of DKG. We may have multiple
		// tickets in the selected tickets (which would
		// result in multiple instances of DKG).
		if ticket.IsFromStaker(n.Staker.ID()) {
			fmt.Println("elligible for group")
			// capture player index for goroutine
			playerIndex := index

			// build the channel name and get the broadcast channel
			broadcastChannelName := channelNameFromSelectedTickets(
				groupSelectionResult.SelectedTickets,
			)

			// We should only join the broadcast channel if we're
			// elligible for the group
			broadcastChannel, err := n.netProvider.ChannelFor(
				broadcastChannelName,
			)
			if err != nil {
				fmt.Fprintf(
					os.Stderr,
					"Failed to get broadcastChannel for name %s with err: [%v].",
					broadcastChannelName,
					err,
				)
				return
			}

			fmt.Printf("Executing dkg with index = %v...\n", index)
			go func() {
				signer, err := dkg2.ExecuteDKG(
					entryRequestID,
					entrySeed,
					playerIndex,
					n.chainConfig.GroupSize,
					n.chainConfig.Threshold,
					n.blockCounter,
					relayChain,
					broadcastChannel,
				)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Failed to execute dkg: [%v].", err)
					return
				}

				n.registerPendingGroup(entryRequestID.String(), signer, broadcastChannel)
			}()
		}
	}
	// exit on signal
	return
}

// channelNameFromSelectedTickets takes the selected tickets, and does the
// following to construct the broadcastChannel name:
// * grabs the value from each ticket
// * concatenates all of the values
// * returns the hashed concatenated values
func channelNameFromSelectedTickets(
	tickets []*groupselection.Ticket,
) string {
	var channelNameBytes []byte
	for _, ticket := range tickets {
		channelNameBytes = append(
			channelNameBytes,
			ticket.Value.Bytes()...,
		)
	}
	hashedChannelName := groupselection.SHAValue(
		sha256.Sum256(channelNameBytes),
	)
	return string(hashedChannelName.Bytes())
}

// RegisterGroup registers that a group was successfully created by the given
// requestID, and its group public key is groupPublicKey.
func (n *Node) RegisterGroup(requestID string, groupPublicKey []byte) {
	n.mutex.Lock()
	defer n.mutex.Unlock()

	// If we've already registered a group for this request ID, no need to
	// add to our list of known group public keys.
	if _, exists := n.seenPublicKeys[requestID]; !exists {
		n.seenPublicKeys[requestID] = true
		n.groupPublicKeys = append(n.groupPublicKeys, groupPublicKey)
	}

	if memberships, found := n.pendingGroups[requestID]; found {
		for _, membership := range memberships {
			membership.index = len(n.groupPublicKeys) - 1
			n.myGroups[requestID] = append(n.myGroups[requestID], membership)
		}
		delete(n.pendingGroups, requestID)
	}
}

// initializePendingGroup grabs ownership of an attempt at group creation for a
// given goroutine. If it returns false, we're already in progress and failed to
// initialize.
func (n *Node) initializePendingGroup(requestID string) bool {
	n.mutex.Lock()
	defer n.mutex.Unlock()

	// If the pending group exists, we're already active
	if _, found := n.pendingGroups[requestID]; found {
		return false
	}

	// Pending group does not exist, take control
	n.pendingGroups[requestID] = nil

	return true
}

// flushPendingGroup if group creation fails, we clean our references to creating
// a group for a given request ID.
func (n *Node) flushPendingGroup(requestID string) {
	n.mutex.Lock()
	defer n.mutex.Unlock()

	if membership, found := n.pendingGroups[requestID]; found && membership == nil {
		delete(n.pendingGroups, requestID)
	}
}

// registerPendingGroup assigns a new membership for a given request ID.
// We overwrite our placeholder membership set by initializePendingGroup.
func (n *Node) registerPendingGroup(
	requestID string,
	signer *dkg2.ThresholdSigner,
	channel net.BroadcastChannel,
) {
	n.mutex.Lock()
	defer n.mutex.Unlock()

	if _, seen := n.seenPublicKeys[requestID]; seen {
		groupPublicKey := signer.GroupPublicKeyBytes()
		// Start at the end since it's likely the public key was closer to the
		// end if it happened to come in before we had a chance to register it
		// as pending.
		existingIndex := len(n.groupPublicKeys) - 1
		for index := existingIndex; index >= 0; index-- {
			if bytes.Compare(n.groupPublicKeys[index], groupPublicKey[:]) == 0 {
				existingIndex = index
				break
			}
		}

		n.myGroups[requestID] = append(n.myGroups[requestID], &membership{
			index:   existingIndex,
			member:  signer,
			channel: channel,
		})
		delete(n.pendingGroups, requestID)
	} else {
		n.pendingGroups[requestID] = append(n.pendingGroups[requestID], &membership{
			member:  signer,
			channel: channel,
		})
	}
}
