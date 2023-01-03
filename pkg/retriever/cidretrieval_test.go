package retriever

import (
	"context"
	"errors"
	mbig "math/big"
	"sync"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-fil-markets/retrievalmarket"
	"github.com/filecoin-project/go-fil-markets/shared"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/google/go-cmp/cmp"
	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime"
	"github.com/libp2p/go-libp2p/core/peer"

	qt "github.com/frankban/quicktest"
	selectorparse "github.com/ipld/go-ipld-prime/traversal/selector/parse"
)

func TestQueryFiltering(t *testing.T) {
	testCases := []struct {
		name           string
		paid           bool
		queryResponses map[string]retrievalmarket.QueryResponse
		expectedPeers  []string
	}{
		{
			name: "PaidRetrievals: true",
			paid: true,
			queryResponses: map[string]retrievalmarket.QueryResponse{
				"foo": {Status: retrievalmarket.QueryResponseUnavailable},
				"bar": {Status: retrievalmarket.QueryResponseAvailable, MinPricePerByte: big.NewInt(1), Size: 2, UnsealPrice: big.Zero()},
				"baz": {Status: retrievalmarket.QueryResponseAvailable, MinPricePerByte: big.Zero(), Size: 2, UnsealPrice: big.Zero()},
			},
			expectedPeers: []string{"bar", "baz"},
		},
		{
			name: "PaidRetrievals: false",
			paid: false,
			queryResponses: map[string]retrievalmarket.QueryResponse{
				"foo": {Status: retrievalmarket.QueryResponseUnavailable},
				"bar": {Status: retrievalmarket.QueryResponseAvailable, MinPricePerByte: big.NewInt(1), Size: 2, UnsealPrice: big.Zero()},
				"baz": {Status: retrievalmarket.QueryResponseAvailable, MinPricePerByte: big.Zero(), Size: 2, UnsealPrice: big.Zero()},
			},
			expectedPeers: []string{"baz"},
		},
		{
			name: "PaidRetrievals: true, /w only paid",
			paid: true,
			queryResponses: map[string]retrievalmarket.QueryResponse{
				"foo": {Status: retrievalmarket.QueryResponseUnavailable},
				"bar": {Status: retrievalmarket.QueryResponseAvailable, MinPricePerByte: big.NewInt(1), Size: 2, UnsealPrice: big.Zero()},
				"baz": {Status: retrievalmarket.QueryResponseAvailable, MinPricePerByte: big.NewInt(1), Size: 2, UnsealPrice: big.Zero()},
			},
			expectedPeers: []string{"bar", "baz"},
		},
		{
			name: "PaidRetrievals: false, /w only paid",
			paid: false,
			queryResponses: map[string]retrievalmarket.QueryResponse{
				"foo": {Status: retrievalmarket.QueryResponseUnavailable},
				"bar": {Status: retrievalmarket.QueryResponseAvailable, MinPricePerByte: big.NewInt(1), Size: 2, UnsealPrice: big.Zero()},
				"baz": {Status: retrievalmarket.QueryResponseAvailable, MinPricePerByte: big.NewInt(1), Size: 2, UnsealPrice: big.Zero()},
			},
			expectedPeers: []string{},
		},
		{
			name: "PaidRetrievals: true, w/ no paid",
			paid: true,
			queryResponses: map[string]retrievalmarket.QueryResponse{
				"foo": {Status: retrievalmarket.QueryResponseUnavailable},
				"bar": {Status: retrievalmarket.QueryResponseAvailable, MinPricePerByte: big.Zero(), Size: 2, UnsealPrice: big.Zero()},
				"baz": {Status: retrievalmarket.QueryResponseAvailable, MinPricePerByte: big.Zero(), Size: 2, UnsealPrice: big.Zero()},
			},
			expectedPeers: []string{"bar", "baz"},
		},
		{
			name: "PaidRetrievals: false, w/ no paid",
			paid: false,
			queryResponses: map[string]retrievalmarket.QueryResponse{
				"foo": {Status: retrievalmarket.QueryResponseUnavailable},
				"bar": {Status: retrievalmarket.QueryResponseAvailable, MinPricePerByte: big.Zero(), Size: 2, UnsealPrice: big.Zero()},
				"baz": {Status: retrievalmarket.QueryResponseAvailable, MinPricePerByte: big.Zero(), Size: 2, UnsealPrice: big.Zero()},
			},
			expectedPeers: []string{"bar", "baz"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockFilc := &mockFilClient{returns_queryResponses: tc.queryResponses}
			mockInstrumentation := &mockInstrumentation{}
			candidates := []RetrievalCandidate{}
			for p := range tc.queryResponses {
				candidates = append(candidates, RetrievalCandidate{MinerPeer: peer.AddrInfo{ID: peer.ID(p)}})
			}
			mockEndpoint := &mockEndpoint{candidates}
			retriever := &Retriever{config: RetrieverConfig{PaidRetrievals: tc.paid}} // used for isAcceptableQueryResponse() only

			retrieval := NewCidRetrieval(
				mockEndpoint,
				mockFilc,
				mockInstrumentation,
				func(peer peer.ID) time.Duration { return time.Second },
				func(peer peer.ID) bool { return true },
				retriever.isAcceptableQueryResponse,
				cid.Undef,
			)

			stats, err := retrieval.RetrieveCid(context.Background())
			qt.Assert(t, stats, qt.IsNil)
			qt.Assert(t, err, qt.IsNotNil)

			// expected all queries
			qt.Assert(t, mockInstrumentation.errorQueryingRetrievalCandidate, qt.IsNil)
			qt.Assert(t, len(mockFilc.received_queriedPeers), qt.Equals, len(tc.queryResponses))
			qt.Assert(t, len(mockInstrumentation.retrievalQueryForCandidate), qt.Equals, len(tc.queryResponses))
			for p, qr := range tc.queryResponses {
				pid := peer.ID(p)
				qt.Assert(t, mockFilc.received_queriedPeers, qt.Contains, pid)
				found := false
				for _, rqfc := range mockInstrumentation.retrievalQueryForCandidate {
					if rqfc.candidate.MinerPeer.ID == pid {
						found = true
						qt.Assert(t, rqfc.queryResponse, qt.CmpEquals(cmp.AllowUnexported(address.Address{}, mbig.Int{})), &qr)
					}
				}
				qt.Assert(t, found, qt.IsTrue)
			}

			// verify that the list of retrievals matches the expected filtered list
			qt.Assert(t, len(mockFilc.received_retrievedPeers), qt.Equals, len(tc.expectedPeers))
			qt.Assert(t, len(mockInstrumentation.filteredRetrievalQueryForCandidate), qt.Equals, len(tc.expectedPeers))
			qt.Assert(t, len(mockInstrumentation.retrievingFromCandidate), qt.Equals, len(tc.expectedPeers))
			qt.Assert(t, len(mockInstrumentation.errorRetrievingFromCandidate), qt.Equals, len(tc.expectedPeers)) // they all error!
			for _, p := range tc.expectedPeers {
				pid := peer.ID(p)
				qt.Assert(t, mockFilc.received_retrievedPeers, qt.Contains, pid)
				qt.Assert(t, mockInstrumentation.retrievingFromCandidate, qt.Any(qt.CmpEquals(cmp.AllowUnexported(cid.Cid{}))), RetrievalCandidate{peer.AddrInfo{ID: pid}, cid.Undef})
				found := false
				for _, rqfc := range mockInstrumentation.filteredRetrievalQueryForCandidate {
					if rqfc.candidate.MinerPeer.ID == pid {
						found = true
						qr := tc.queryResponses[p]
						qt.Assert(t, rqfc.queryResponse, qt.CmpEquals(cmp.AllowUnexported(address.Address{}, mbig.Int{})), &qr)
					}
				}
				qt.Assert(t, found, qt.IsTrue)
			}
		})
	}
}

var _ FilClient = (*mockFilClient)(nil)
var _ Endpoint = (*mockEndpoint)(nil)
var _ BlockConfirmer = dummyBlockConfirmer
var _ Instrumentation = (*mockInstrumentation)(nil)
var testDealIdGen = shared.NewTimeCounter()

type mockFilClient struct {
	lk                      sync.Mutex
	received_queriedPeers   []peer.ID
	received_retrievedPeers []peer.ID

	returns_queryResponses map[string]retrievalmarket.QueryResponse
}

func (dfc *mockFilClient) RetrievalProposalForAsk(ask *retrievalmarket.QueryResponse, c cid.Cid, optionalSelector ipld.Node) (*retrievalmarket.DealProposal, error) {
	if optionalSelector == nil {
		optionalSelector = selectorparse.CommonSelector_ExploreAllRecursively
	}

	params, err := retrievalmarket.NewParamsV1(
		ask.MinPricePerByte,
		ask.MaxPaymentInterval,
		ask.MaxPaymentIntervalIncrease,
		optionalSelector,
		nil,
		ask.UnsealPrice,
	)
	if err != nil {
		return nil, err
	}
	return &retrievalmarket.DealProposal{
		PayloadCID: c,
		ID:         retrievalmarket.DealID(testDealIdGen.Next()),
		Params:     params,
	}, nil
}

func (dfc *mockFilClient) RetrievalQueryToPeer(ctx context.Context, minerPeer peer.AddrInfo, pcid cid.Cid) (*retrievalmarket.QueryResponse, error) {
	dfc.lk.Lock()
	dfc.received_queriedPeers = append(dfc.received_queriedPeers, minerPeer.ID)
	dfc.lk.Unlock()

	if qr, ok := dfc.returns_queryResponses[string(minerPeer.ID)]; ok {
		return &qr, nil
	}
	time.Sleep(time.Millisecond * 200) // delay retrieval to ensure we don't race and end up with a query+retrieval before all queries end
	return &retrievalmarket.QueryResponse{Status: retrievalmarket.QueryResponseUnavailable}, nil
}

func (dfc *mockFilClient) RetrieveContentFromPeerAsync(
	ctx context.Context,
	peerID peer.ID,
	minerWallet address.Address,
	proposal *retrievalmarket.DealProposal,
) (<-chan RetrievalResult, <-chan uint64, func()) {
	dfc.lk.Lock()
	dfc.received_retrievedPeers = append(dfc.received_retrievedPeers, peerID)
	dfc.lk.Unlock()
	resChan := make(chan RetrievalResult, 1)
	resChan <- RetrievalResult{RetrievalStats: nil, Err: errors.New("nope")}
	return resChan, nil, func() {}
}

func (*mockFilClient) SubscribeToRetrievalEvents(subscriber RetrievalSubscriber) {}

type mockEndpoint struct {
	candidates []RetrievalCandidate
}

func (me *mockEndpoint) FindCandidates(context.Context, cid.Cid) ([]RetrievalCandidate, error) {
	return me.candidates, nil
}

func dummyBlockConfirmer(c cid.Cid) (bool, error) {
	return true, nil
}

type instrumentationCandidateError struct {
	candidate RetrievalCandidate
	err       error
}
type instrumentationCandidateQuery struct {
	candidate     RetrievalCandidate
	queryResponse *retrievalmarket.QueryResponse
}
type mockInstrumentation struct {
	lk                                 sync.Mutex
	foundCount                         *int
	filteredCount                      *int
	errorQueryingRetrievalCandidate    []instrumentationCandidateError
	errorRetrievingFromCandidate       []instrumentationCandidateError
	retrievalQueryForCandidate         []instrumentationCandidateQuery
	filteredRetrievalQueryForCandidate []instrumentationCandidateQuery
	retrievingFromCandidate            []RetrievalCandidate
}

func (mi *mockInstrumentation) OnRetrievalCandidatesFound(foundCount int) error {
	mi.lk.Lock()
	defer mi.lk.Unlock()
	mi.foundCount = &foundCount
	return nil
}
func (mi *mockInstrumentation) OnRetrievalCandidatesFiltered(filteredCount int) error {
	mi.lk.Lock()
	defer mi.lk.Unlock()
	mi.filteredCount = &filteredCount
	return nil
}
func (mi *mockInstrumentation) OnErrorQueryingRetrievalCandidate(candidate RetrievalCandidate, err error) {
	mi.lk.Lock()
	defer mi.lk.Unlock()
	if mi.errorQueryingRetrievalCandidate == nil {
		mi.errorQueryingRetrievalCandidate = make([]instrumentationCandidateError, 0)
	}
	mi.errorQueryingRetrievalCandidate = append(mi.errorQueryingRetrievalCandidate, instrumentationCandidateError{candidate, err})
}
func (mi *mockInstrumentation) OnErrorRetrievingFromCandidate(candidate RetrievalCandidate, err error) {
	mi.lk.Lock()
	defer mi.lk.Unlock()
	if mi.errorRetrievingFromCandidate == nil {
		mi.errorRetrievingFromCandidate = make([]instrumentationCandidateError, 0)
	}
	mi.errorRetrievingFromCandidate = append(mi.errorRetrievingFromCandidate, instrumentationCandidateError{candidate, err})
}
func (mi *mockInstrumentation) OnRetrievalQueryForCandidate(candidate RetrievalCandidate, queryResponse *retrievalmarket.QueryResponse) {
	mi.lk.Lock()
	defer mi.lk.Unlock()
	if mi.retrievalQueryForCandidate == nil {
		mi.retrievalQueryForCandidate = make([]instrumentationCandidateQuery, 0)
	}
	mi.retrievalQueryForCandidate = append(mi.retrievalQueryForCandidate, instrumentationCandidateQuery{candidate, queryResponse})
}
func (mi *mockInstrumentation) OnFilteredRetrievalQueryForCandidate(candidate RetrievalCandidate, queryResponse *retrievalmarket.QueryResponse) {
	mi.lk.Lock()
	defer mi.lk.Unlock()
	if mi.filteredRetrievalQueryForCandidate == nil {
		mi.filteredRetrievalQueryForCandidate = make([]instrumentationCandidateQuery, 0)
	}
	mi.filteredRetrievalQueryForCandidate = append(mi.filteredRetrievalQueryForCandidate, instrumentationCandidateQuery{candidate, queryResponse})
}
func (mi *mockInstrumentation) OnRetrievingFromCandidate(candidate RetrievalCandidate) {
	mi.lk.Lock()
	defer mi.lk.Unlock()
	if mi.retrievingFromCandidate == nil {
		mi.retrievingFromCandidate = make([]RetrievalCandidate, 0)
	}
	mi.retrievingFromCandidate = append(mi.retrievingFromCandidate, candidate)
}
