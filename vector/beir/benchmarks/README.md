# BEIR Benchmark Reference Scores

This directory contains reference benchmark scores from the BEIR (Benchmarking IR) research for comparison with your Dgraph vector search results.

## Files

### SciFact Dataset

- **`scifact_acceptable.json`** - Baseline acceptable performance
  - Model: all-MiniLM-L6-v2 (384-dim)
  - NDCG@10: ~0.65
  - Use case: Fast, efficient retrieval

- **`scifact_excellent.json`** - State-of-the-art performance
  - Model: all-mpnet-base-v2 or larger (768-dim)
  - NDCG@10: ~0.70
  - Use case: High-quality retrieval

## How to Use

### Compare Your Results

```bash
# Your result
cat ../results/results_scifact_*.json

# Compare with acceptable baseline
cat scifact_acceptable.json

# Compare with excellent target
cat scifact_excellent.json
```

### Performance Ranges

| Level | NDCG@10 | Description |
|-------|---------|-------------|
| **Poor** | < 0.50 | Significant issues with retrieval or ranking |
| **Acceptable** | 0.50 - 0.65 | Reasonable baseline with small models |
| **Good** | 0.65 - 0.68 | Solid performance, well-tuned system |
| **Excellent** | 0.68 - 0.72 | State-of-the-art, optimal configuration |
| **Outstanding** | > 0.72 | Beyond published benchmarks |

## Factors Affecting Performance

### Model Size
- **384-dim** (all-MiniLM-L6-v2): Faster, less accurate (~0.65 NDCG@10)
- **768-dim** (all-mpnet-base-v2): Slower, more accurate (~0.70 NDCG@10)

### HNSW Parameters
- **Low** (efConstruction=100, efSearch=40): Fast, lower quality
- **Medium** (efConstruction=400, efSearch=100): Balanced
- **High** (efConstruction=1000, efSearch=200): Slow, highest quality

### Dataset Characteristics
SciFact is a well-structured scientific claims dataset:
- 5,183 documents
- 300 test queries
- Clear relevance judgments
- Good for testing semantic search quality

## Sources

- [BEIR GitHub](https://github.com/beir-cellar/beir)
- [BEIR Paper](https://arxiv.org/abs/2104.08663)
- [Sentence Transformers Performance](https://www.sbert.net/docs/pretrained_models.html)

## Notes

- These scores are approximate targets based on published research
- Actual performance varies based on implementation details
- HNSW approximate search may score slightly lower than exact k-NN
- Your Dgraph implementation should aim for the "acceptable" range as a minimum
