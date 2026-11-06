package embedsc

import (
	"container/heap"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// HNSWConfig contains configuration parameters for HNSW index.
type HNSWConfig struct {
	// M is the maximum number of bidirectional links per node in layers > 0.
	M int `json:"m"`
	// M0 is the maximum number of bidirectional links per node in layer 0.
	M0 int `json:"m0"`
	// EfConstruction is the size of dynamic candidate list during graph construction.
	EfConstruction int `json:"ef_construction"`
	// EfSearch is the size of dynamic candidate list during query time.
	EfSearch int `json:"ef_search"`
	// Heuristic enables the diverse neighbor selection heuristic (Algorithm 4).
	Heuristic bool `json:"heuristic"`
}

// DefaultHNSWConfig returns production-ready default HNSW parameters.
func DefaultHNSWConfig() HNSWConfig {
	return HNSWConfig{
		M:              16,
		M0:             32,
		EfConstruction: 64,
		EfSearch:       32,
		Heuristic:      true,
	}
}

// SearchResult represents a matched nearest neighbor item.
type SearchResult struct {
	ID         int     `json:"id"`
	Similarity float32 `json:"similarity"`
	Distance   float32 `json:"distance"`
}

// HNSWStats provides runtime metrics about the HNSW index.
type HNSWStats struct {
	Count      int `json:"count"`
	MaxLevel   int `json:"max_level"`
	EntryPoint int `json:"entry_point"`
	M          int `json:"m"`
	M0         int `json:"m0"`
	EfSearch   int `json:"ef_search"`
}

type distPair struct {
	id   int
	dist float32
}

// minPairHeap is a min-heap where root has the minimum distance.
type minPairHeap []distPair

func (h minPairHeap) Len() int           { return len(h) }
func (h minPairHeap) Less(i, j int) bool { return h[i].dist < h[j].dist }
func (h minPairHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minPairHeap) Push(x any)        { *h = append(*h, x.(distPair)) }
func (h *minPairHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

// maxPairHeap is a max-heap where root has the maximum distance.
type maxPairHeap []distPair

func (h maxPairHeap) Len() int           { return len(h) }
func (h maxPairHeap) Less(i, j int) bool { return h[i].dist > h[j].dist }
func (h maxPairHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxPairHeap) Push(x any)        { *h = append(*h, x.(distPair)) }
func (h *maxPairHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

type hnswNode struct {
	id        int
	vec       []float32
	level     int
	neighbors [][]int
}

// HNSWIndex is a thread-safe hierarchical navigable small world graph index.
type HNSWIndex struct {
	mu         sync.RWMutex
	cfg        HNSWConfig
	maxLevel   int
	entryPoint int
	nodes      map[int]*hnswNode
	mL         float64
	rnd        *rand.Rand
	rndMu      sync.Mutex
}

// NewHNSWIndex creates a new HNSW index with the given configuration.
func NewHNSWIndex(cfg HNSWConfig) *HNSWIndex {
	if cfg.M <= 0 {
		cfg.M = 16
	}
	if cfg.M0 <= 0 {
		cfg.M0 = 2 * cfg.M
	}
	if cfg.EfConstruction <= 0 {
		cfg.EfConstruction = 64
	}
	if cfg.EfSearch <= 0 {
		cfg.EfSearch = 32
	}

	mL := 1.0 / math.Log(float64(cfg.M))
	return &HNSWIndex{
		cfg:        cfg,
		maxLevel:   -1,
		entryPoint: -1,
		nodes:      make(map[int]*hnswNode),
		mL:         mL,
		rnd:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// cosineDistance returns cosine distance in [0, 2] for unit vectors.
func cosineDistance(a, b []float32) float32 {
	d := dot(a, b)
	if d > 1.0 {
		d = 1.0
	} else if d < -1.0 {
		d = -1.0
	}
	return 1.0 - d
}

func (idx *HNSWIndex) randomLevel() int {
	idx.rndMu.Lock()
	r := idx.rnd.Float64()
	idx.rndMu.Unlock()

	if r == 0 {
		r = 1e-9
	}
	return int(-math.Log(r) * idx.mL)
}

// searchLayer performs greedy best-first search on a single layer.
func (idx *HNSWIndex) searchLayer(q []float32, enterPoints []int, ef int, level int) *maxPairHeap {
	visited := make(map[int]bool, ef*2)
	candidates := &minPairHeap{}
	w := &maxPairHeap{}
	heap.Init(candidates)
	heap.Init(w)

	for _, ep := range enterPoints {
		node := idx.nodes[ep]
		if node == nil {
			continue
		}
		d := cosineDistance(q, node.vec)
		visited[ep] = true
		pair := distPair{id: ep, dist: d}
		heap.Push(candidates, pair)
		heap.Push(w, pair)
	}

	for candidates.Len() > 0 {
		curr := heap.Pop(candidates).(distPair)
		furthest := (*w)[0]

		if curr.dist > furthest.dist {
			break
		}

		currNode := idx.nodes[curr.id]
		if currNode == nil || level >= len(currNode.neighbors) {
			continue
		}

		for _, neighborID := range currNode.neighbors[level] {
			if visited[neighborID] {
				continue
			}
			visited[neighborID] = true

			neighborNode := idx.nodes[neighborID]
			if neighborNode == nil {
				continue
			}

			d := cosineDistance(q, neighborNode.vec)
			furthest = (*w)[0]

			if d < furthest.dist || w.Len() < ef {
				pair := distPair{id: neighborID, dist: d}
				heap.Push(candidates, pair)
				heap.Push(w, pair)
				if w.Len() > ef {
					heap.Pop(w)
				}
			}
		}
	}

	return w
}

// selectNeighborsHeuristic implements Algorithm 4 (heuristic with diversity).
func (idx *HNSWIndex) selectNeighborsHeuristic(baseVec []float32, candidates []distPair, m int) []int {
	if len(candidates) <= m {
		res := make([]int, len(candidates))
		for i, c := range candidates {
			res[i] = c.id
		}
		return res
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].dist < candidates[j].dist
	})

	result := make([]int, 0, m)
	var discarded []distPair

	for _, c := range candidates {
		cNode := idx.nodes[c.id]
		if cNode == nil {
			continue
		}

		good := true
		for _, rID := range result {
			rNode := idx.nodes[rID]
			if rNode == nil {
				continue
			}
			distToR := cosineDistance(cNode.vec, rNode.vec)
			if distToR < c.dist {
				good = false
				break
			}
		}

		if good {
			result = append(result, c.id)
			if len(result) >= m {
				break
			}
		} else {
			discarded = append(discarded, c)
		}
	}

	// fill up to m from discarded closest candidates if needed
	if len(result) < m {
		for _, d := range discarded {
			result = append(result, d.id)
			if len(result) >= m {
				break
			}
		}
	}

	return result
}

// Insert adds a vector with its ID into the HNSW index.
func (idx *HNSWIndex) Insert(id int, vec []float32) {
	if len(vec) == 0 {
		return
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	// if node already exists, update vector
	if existing, ok := idx.nodes[id]; ok {
		existing.vec = vec
		return
	}

	nodeLevel := idx.randomLevel()
	newNode := &hnswNode{
		id:        id,
		vec:       vec,
		level:     nodeLevel,
		neighbors: make([][]int, nodeLevel+1),
	}
	idx.nodes[id] = newNode

	if idx.entryPoint == -1 || idx.maxLevel == -1 {
		idx.entryPoint = id
		idx.maxLevel = nodeLevel
		return
	}

	currObj := idx.entryPoint
	maxL := idx.maxLevel

	// 1. greedy 1-NN search down to nodeLevel + 1
	for l := maxL; l > nodeLevel; l-- {
		w := idx.searchLayer(vec, []int{currObj}, 1, l)
		if w.Len() > 0 {
			currObj = (*w)[0].id
		}
	}

	// 2. connect from min(maxL, nodeLevel) down to 0
	startLevel := maxL
	if nodeLevel < startLevel {
		startLevel = nodeLevel
	}

	for l := startLevel; l >= 0; l-- {
		w := idx.searchLayer(vec, []int{currObj}, idx.cfg.EfConstruction, l)

		candidates := make([]distPair, 0, w.Len())
		for _, item := range *w {
			candidates = append(candidates, item)
		}

		m := idx.cfg.M
		if l == 0 {
			m = idx.cfg.M0
		}

		var selected []int
		if idx.cfg.Heuristic {
			selected = idx.selectNeighborsHeuristic(vec, candidates, m)
		} else {
			sort.Slice(candidates, func(i, j int) bool { return candidates[i].dist < candidates[j].dist })
			for i := 0; i < len(candidates) && i < m; i++ {
				selected = append(selected, candidates[i].id)
			}
		}

		newNode.neighbors[l] = selected

		// bidirectional link and shrink if needed
		for _, neighborID := range selected {
			neighborNode := idx.nodes[neighborID]
			if neighborNode == nil || l >= len(neighborNode.neighbors) {
				continue
			}

			neighborNode.neighbors[l] = append(neighborNode.neighbors[l], id)

			maxM := idx.cfg.M
			if l == 0 {
				maxM = idx.cfg.M0
			}
			if len(neighborNode.neighbors[l]) > maxM {
				cands := make([]distPair, 0, len(neighborNode.neighbors[l]))
				for _, nID := range neighborNode.neighbors[l] {
					n := idx.nodes[nID]
					if n != nil {
						cands = append(cands, distPair{id: nID, dist: cosineDistance(neighborNode.vec, n.vec)})
					}
				}
				if idx.cfg.Heuristic {
					neighborNode.neighbors[l] = idx.selectNeighborsHeuristic(neighborNode.vec, cands, maxM)
				} else {
					sort.Slice(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
					newN := make([]int, 0, maxM)
					for i := 0; i < len(cands) && i < maxM; i++ {
						newN = append(newN, cands[i].id)
					}
					neighborNode.neighbors[l] = newN
				}
			}
		}

		if w.Len() > 0 {
			best := (*w)[0]
			for _, item := range *w {
				if item.dist < best.dist {
					best = item
				}
			}
			currObj = best.id
		}
	}

	if nodeLevel > idx.maxLevel {
		idx.maxLevel = nodeLevel
		idx.entryPoint = id
	}
}

// Search performs approximate nearest neighbor search for query vector q.
func (idx *HNSWIndex) Search(q []float32, k int, efSearch int) []SearchResult {
	if len(q) == 0 || k <= 0 {
		return nil
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	totalNodes := len(idx.nodes)
	if totalNodes == 0 {
		return nil
	}

	// if total nodes is small, return all sorted by cosine similarity
	if totalNodes <= k {
		res := make([]SearchResult, 0, totalNodes)
		for id, node := range idx.nodes {
			d := cosineDistance(q, node.vec)
			res = append(res, SearchResult{ID: id, Similarity: 1.0 - d, Distance: d})
		}
		sort.Slice(res, func(i, j int) bool { return res[i].Similarity > res[j].Similarity })
		return res
	}

	if efSearch < k {
		efSearch = k
	}
	if efSearch < idx.cfg.EfSearch {
		efSearch = idx.cfg.EfSearch
	}

	currObj := idx.entryPoint
	for l := idx.maxLevel; l > 0; l-- {
		w := idx.searchLayer(q, []int{currObj}, 1, l)
		if w.Len() > 0 {
			best := (*w)[0]
			for _, item := range *w {
				if item.dist < best.dist {
					best = item
				}
			}
			currObj = best.id
		}
	}

	// level 0 search
	w := idx.searchLayer(q, []int{currObj}, efSearch, 0)
	if w.Len() == 0 {
		return nil
	}

	all := make([]distPair, 0, w.Len())
	for _, item := range *w {
		all = append(all, item)
	}

	sort.Slice(all, func(i, j int) bool { return all[i].dist < all[j].dist })

	if len(all) > k {
		all = all[:k]
	}

	results := make([]SearchResult, len(all))
	for i, item := range all {
		results[i] = SearchResult{
			ID:         item.id,
			Similarity: 1.0 - item.dist,
			Distance:   item.dist,
		}
	}

	return results
}

// Delete removes a vector ID from the index.
func (idx *HNSWIndex) Delete(id int) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	node, ok := idx.nodes[id]
	if !ok {
		return
	}

	for l := 0; l <= node.level; l++ {
		for _, neighborID := range node.neighbors[l] {
			neighbor := idx.nodes[neighborID]
			if neighbor == nil || l >= len(neighbor.neighbors) {
				continue
			}
			filtered := make([]int, 0, len(neighbor.neighbors[l]))
			for _, nid := range neighbor.neighbors[l] {
				if nid != id {
					filtered = append(filtered, nid)
				}
			}
			neighbor.neighbors[l] = filtered
		}
	}

	delete(idx.nodes, id)

	if idx.entryPoint == id {
		idx.entryPoint = -1
		idx.maxLevel = -1
		for nid, n := range idx.nodes {
			if n.level > idx.maxLevel {
				idx.maxLevel = n.level
				idx.entryPoint = nid
			}
		}
	}
}

// Len returns the number of vectors in the index.
func (idx *HNSWIndex) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.nodes)
}

// Stats returns runtime statistics of the index.
func (idx *HNSWIndex) Stats() HNSWStats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return HNSWStats{
		Count:      len(idx.nodes),
		MaxLevel:   idx.maxLevel,
		EntryPoint: idx.entryPoint,
		M:          idx.cfg.M,
		M0:         idx.cfg.M0,
		EfSearch:   idx.cfg.EfSearch,
	}
}
