package consensus

import (
	"bytes"
	"errors"
	"io"
	"sync"

	. "github.com/tendermint/tendermint/binary"
	. "github.com/tendermint/tendermint/blocks"
	. "github.com/tendermint/tendermint/state"
)

const (
	VoteTypeBare      = byte(0x00)
	VoteTypePrecommit = byte(0x01)
	VoteTypeCommit    = byte(0x02)
)

var (
	ErrVoteUnexpectedPhase      = errors.New("Unexpected phase")
	ErrVoteInvalidAccount       = errors.New("Invalid round vote account")
	ErrVoteInvalidSignature     = errors.New("Invalid round vote signature")
	ErrVoteInvalidHash          = errors.New("Invalid hash")
	ErrVoteConflictingSignature = errors.New("Conflicting round vote signature")
)

// Represents a bare, precommit, or commit vote for proposals.
type Vote struct {
	Height uint32
	Round  uint16 // zero if commit vote.
	Type   byte
	Hash   []byte // empty if vote is nil.
	Signature
}

func ReadVote(r io.Reader, n *int64, err *error) *Vote {
	return &Vote{
		Height:    ReadUInt32(r, n, err),
		Round:     ReadUInt16(r, n, err),
		Type:      ReadByte(r, n, err),
		Hash:      ReadByteSlice(r, n, err),
		Signature: ReadSignature(r, n, err),
	}
}

func (v *Vote) WriteTo(w io.Writer) (n int64, err error) {
	WriteUInt32(w, v.Height, &n, &err)
	WriteUInt16(w, v.Round, &n, &err)
	WriteByte(w, v.Type, &n, &err)
	WriteByteSlice(w, v.Hash, &n, &err)
	WriteBinary(w, v.Signature, &n, &err)
	return
}

func (v *Vote) GetDocument() string {
	return GenVoteDocument(v.Type, v.Height, v.Round, v.Hash)
}

//-----------------------------------------------------------------------------

// VoteSet helps collect signatures from validators at each height+round
// for a predefined vote type.
type VoteSet struct {
	mtx              sync.Mutex
	height           uint32
	round            uint16
	type_            byte
	validators       *ValidatorSet
	votes            map[uint64]*Vote
	votesByHash      map[string]uint64
	totalVotes       uint64
	totalVotingPower uint64
}

// Constructs a new VoteSet struct used to accumulate votes for each round.
func NewVoteSet(height uint32, round uint16, type_ byte, validators *ValidatorSet) *VoteSet {
	if type_ == VoteTypeCommit && round != 0 {
		panic("Expected round 0 for commit vote set")
	}
	totalVotingPower := uint64(0)
	for _, val := range validators.Map() {
		totalVotingPower += val.VotingPower
	}
	return &VoteSet{
		height:           height,
		round:            round,
		type_:            type_,
		validators:       validators,
		votes:            make(map[uint64]*Vote, validators.Size()),
		votesByHash:      make(map[string]uint64),
		totalVotes:       0,
		totalVotingPower: totalVotingPower,
	}
}

// True if added, false if not.
// Returns ErrVote[UnexpectedPhase|InvalidAccount|InvalidSignature|InvalidHash|ConflictingSignature]
func (vs *VoteSet) AddVote(vote *Vote) (bool, error) {
	vs.mtx.Lock()
	defer vs.mtx.Unlock()

	// Make sure the phase matches.
	if vote.Height != vs.height ||
		(vote.Type != VoteTypeCommit && vote.Round != vs.round) ||
		vote.Type != vs.type_ {
		return false, ErrVoteUnexpectedPhase
	}

	val := vs.validators.Get(vote.SignerId)
	// Ensure that signer is a validator.
	if val == nil {
		return false, ErrVoteInvalidAccount
	}
	// Check signature.
	if !val.Verify([]byte(vote.GetDocument()), vote.Signature) {
		// Bad signature.
		return false, ErrVoteInvalidSignature
	}
	// If vote already exists, return false.
	if existingVote, ok := vs.votes[vote.SignerId]; ok {
		if bytes.Equal(existingVote.Hash, vote.Hash) {
			return false, nil
		} else {
			return false, ErrVoteConflictingSignature
		}
	}
	vs.votes[vote.SignerId] = vote
	vs.votesByHash[string(vote.Hash)] += val.VotingPower
	vs.totalVotes += val.VotingPower
	return true, nil
}

// Returns either a blockhash (or nil) that received +2/3 majority.
// If there exists no such majority, returns (nil, false).
func (vs *VoteSet) TwoThirdsMajority() (hash []byte, ok bool) {
	vs.mtx.Lock()
	defer vs.mtx.Unlock()
	twoThirdsMajority := (vs.totalVotingPower*2 + 2) / 3
	if vs.totalVotes < twoThirdsMajority {
		return nil, false
	}
	for hash, votes := range vs.votesByHash {
		if votes >= twoThirdsMajority {
			if hash == "" {
				return nil, true
			} else {
				return []byte(hash), true
			}
		}
	}
	return nil, false
}

// Returns blockhashes (or nil) that received a +1/3 majority.
// If there exists no such majority, returns nil.
func (vs *VoteSet) OneThirdMajority() (hashes []interface{}) {
	vs.mtx.Lock()
	defer vs.mtx.Unlock()
	oneThirdMajority := (vs.totalVotingPower + 2) / 3
	if vs.totalVotes < oneThirdMajority {
		return nil
	}
	for hash, votes := range vs.votesByHash {
		if votes >= oneThirdMajority {
			if hash == "" {
				hashes = append(hashes, nil)
			} else {
				hashes = append(hashes, []byte(hash))
			}
		}
	}
	return hashes
}