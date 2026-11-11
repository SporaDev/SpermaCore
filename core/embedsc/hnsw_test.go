package embedsc

import (
	"math"
	"math/rand"
	"sort"
	"sync"
	"testing"
)

// helper to create a random unit vector of dimension d
func randomUnitVector(rnd *rand.Rand, d int) []float32 {
	v := make([]float32, d)
	var sumSq float64
	for i := 0; i < d; i++ {
		val := rnd.NormFloat64()
		v[i] = float32(val)
		sumSq += val * val
	}
	norm := float32(math.Sqrt(sumSq))
	if norm > 0 {
		for i := 0; i < d; i++ {
			v[i] /= norm
		}
	}
	return v
}

func TestHNSWBasic(t *testing.T) {
	cfg := DefaultHNSWConfig()
	idx := NewHNSWIndex(cfg)

	if idx.Len() != 0 {
		t.Fatalf("expected empty index, got len=%d", idx.Len())
	}

	// Empty search should return nil
	if hits := idx.Search([]float32{1, 0, 0}, 5, 10); hits != nil {
		t.Fatalf("expected nil for empty search, got %v", hits)
	}

	// Insert single vector
	idx.Insert(1, []float32{1, 0, 0})
	if idx.Len() != 1 {
		t.Fatalf("expected len=1, got %d", idx.Len())
	}

	hits := idx.Search([]float32{1, 0, 0}, 5, 10)
	if len(hits) != 1 || hits[0].ID != 1 {
		t.Fatalf("expected hit ID 1, got %v", hits)
	}
	if math.Abs(float64(hits[0].Similarity-1.0)) > 1e-4 {
		t.Fatalf("expected similarity ~1.0, got %f", hits[0].Similarity)
	}
}

func TestHNSWDelete(t *testing.T) {
	idx := NewHNSWIndex(DefaultHNSWConfig())

	v1 := []float32{1, 0, 0}
	v2 := []float32{0, 1, 0}
	v3 := []float32{0, 0, 1}

	idx.Insert(1, v1)
	idx.Insert(2, v2)
	idx.Insert(3, v3)

	if idx.Len() != 3 {
		t.Fatalf("expected len=3, got %d", idx.Len())
	}

	idx.Delete(2)
	if idx.Len() != 2 {
		t.Fatalf("expected len=2 after delete, got %d", idx.Len())
	}

	hits := idx.Search(v2, 5, 10)
	for _, h := range hits {
		if h.ID == 2 {
			t.Fatalf("deleted item 2 still found in hits")
		}
	}
}

func TestHNSWRecallAccuracy(t *testing.T) {
	rnd := rand.New(rand.NewSource(42))
	dim := 64
	numVectors := 500
	numQueries := 30
	k := 5

	vectors := make([][]float32, numVectors)
	cfg := DefaultHNSWConfig()
	cfg.EfSearch = 64
	idx := NewHNSWIndex(cfg)

	for i := 0; i < numVectors; i++ {
		vectors[i] = randomUnitVector(rnd, dim)
		idx.Insert(i+1, vectors[i])
	}

	if idx.Len() != numVectors {
		t.Fatalf("expected len=%d, got %d", numVectors, idx.Len())
	}

	totalMatches := 0
	totalExpected := numQueries * k

	for q := 0; q < numQueries; q++ {
		queryVec := randomUnitVector(rnd, dim)

		// Exact brute-force ground truth
		type bruteHit struct {
			id   int
			sim  float32
		}
		all := make([]bruteHit, numVectors)
		for i := 0; i < numVectors; i++ {
			all[i] = bruteHit{id: i + 1, sim: dot(queryVec, vectors[i])}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].sim > all[j].sim })
		groundTruth := make(map[int]bool, k)
		for i := 0; i < k; i++ {
			groundTruth[all[i].id] = true
		}

		// HNSW ANN search
		hits := idx.Search(queryVec, k, cfg.EfSearch)
		if len(hits) != k {
			t.Fatalf("expected %d hits, got %d", k, len(hits))
		}

		for _, h := range hits {
			if groundTruth[h.ID] {
				totalMatches++
			}
		}
	}

	recall := float64(totalMatches) / float64(totalExpected)
	t.Logf("HNSW recall@%d across %d queries: %.2f%% (%d/%d)", k, numQueries, recall*100, totalMatches, totalExpected)

	// In high dimensions with HNSW, recall@5 should easily exceed 90%
	if recall < 0.90 {
		t.Fatalf("recall too low: got %.2f%%, want >= 90%%", recall*100)
	}
}

func TestHNSWConcurrent(t *testing.T) {
	rnd := rand.New(rand.NewSource(1234))
	dim := 16
	idx := NewHNSWIndex(DefaultHNSWConfig())

	for i := 1; i <= 50; i++ {
		idx.Insert(i, randomUnitVector(rnd, dim))
	}

	var wg sync.WaitGroup
	// 5 readers
	for r := 0; r < 5; r++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			localRnd := rand.New(rand.NewSource(seed))
			for i := 0; i < 50; i++ {
				q := randomUnitVector(localRnd, dim)
				_ = idx.Search(q, 3, 16)
			}
		}(int64(r * 100))
	}

	// 2 writers
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			localRnd := rand.New(rand.NewSource(int64(workerID * 555)))
			for i := 0; i < 20; i++ {
				id := 100 + workerID*100 + i
				idx.Insert(id, randomUnitVector(localRnd, dim))
			}
		}(w)
	}

	wg.Wait()
	if idx.Len() != 90 {
		t.Fatalf("expected 90 items, got %d", idx.Len())
	}
}
