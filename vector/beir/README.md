# BEIR Vector Search Benchmark for Dgraph

This directory contains a comprehensive benchmark suite for testing Dgraph's vector search capabilities using the [BEIR](https://github.com/beir-cellar/beir) (Benchmarking IR) dataset.

## Overview

The BEIR benchmark allows you to:
- Test Dgraph's HNSW vector index implementation
- Compare performance across different Dgraph versions
- Evaluate retrieval quality using standard IR metrics (NDCG, MAP, Recall, Precision)
- Experiment with different HNSW parameters

## Prerequisites

- **Docker**: For running Dgraph containers
- **uv**: Python package manager ([installation guide](https://github.com/astral-sh/uv))
- **Python 3.12+**

### Installing uv

```bash
curl -LsSf https://astral.sh/uv/install.sh | sh
```

## Project Structure

```
beir/
├── .env.example              # Example environment configuration
├── docker-compose.yml        # Dgraph container configuration
├── pyproject.toml            # Python dependencies (managed by uv)
├── README.md                 # This file
│
├── config.py                 # Configuration management
├── embeddings.py             # Embedding generation with caching
├── dgraph_client.py          # Dgraph client wrapper
├── evaluate.py               # Main evaluation script
├── view_results.py           # Terminal-based results viewer with ASCII charts
├── insert_benchmark.py       # Standalone insertion benchmark
├── generate_test_query.py    # Generate test queries for debugging
├── run_benchmark.sh          # Automated benchmark runner
│
├── benchmarks/               # Reference benchmark thresholds (JSON)
├── data/                     # BEIR datasets (auto-downloaded)
└── results/                  # Evaluation results (CSV)
```

## Quick Start

### 1. Install Dependencies

```bash
uv sync
```

This will create a virtual environment and install all required dependencies.

### 2. Configure Environment

Copy the example environment file:

```bash
cp .env.example .env
```

Edit `.env` to configure your benchmark. Key settings:

```bash
# Dgraph version to test
DGRAPH_VERSION=v25.1.0      # Docker image tag (e.g., v25.1.0, local)

# Dataset and embedding model
DATASET_NAME=scifact        # BEIR dataset (scifact, nfcorpus, fiqa, etc.)
EMBEDDING_MODEL=all-mpnet-base-v2  # Sentence transformer model

# HNSW index parameters
HNSW_METRIC=euclidean       # euclidean, cosine, or dotproduct
HNSW_EF_SEARCH=32           # Search-time beam width
HNSW_EF_CONSTRUCTION=64     # Build-time beam width

# Optional description for this run
DESC="Testing v25.1.0 with default params"
```

See `.env.example` for all available options including experimental features.

### 3. Run the Benchmark

The easiest way to run the benchmark is with the provided shell script:

```bash
./run_benchmark.sh
```

This will:
1. Start Dgraph using Docker Compose
2. Download the BEIR dataset (cached in `./data/`)
3. Generate embeddings (cached in `~/.cache/beir_embeddings/`)
4. Insert documents and run vector search queries
5. Evaluate results and append to `./results/benchmark_results.csv`
6. Stop Dgraph

For historical reasons, please don't commit removed existing rows from `./results/benchmark_results.csv`.

To keep Dgraph running after the benchmark for manual testing:

```bash
KEEP_RUNNING=true ./run_benchmark.sh
```

**Note on Embedding Caching:** Embeddings are cached in `~/.cache/beir_embeddings/` based on dataset name and model. If you change the `EMBEDDING_MODEL`, you must manually delete the cached embeddings for that dataset:

```bash
# Clear all cached embeddings
rm -rf ~/.cache/beir_embeddings/

# Or clear for a specific dataset
rm ~/.cache/beir_embeddings/scifact_*.npz
```

### 4. View Results

Results are appended to `./results/benchmark_results.csv`. Use the `view_results.py` script to view them:

```bash
# View summary table (default)
uv run python view_results.py

# View with ASCII bar charts for all metrics
uv run python view_results.py --bars

# View specific metric
uv run python view_results.py --bars --metric ndcg

# View timing comparison
uv run python view_results.py --timing

# View only the last N results
uv run python view_results.py --last 5

# Filter by platform
uv run python view_results.py --platform Dgraph

# Show raw table
uv run python view_results.py --raw
```

Example output:
```
Loaded 5 result(s) from benchmark_results.csv

====================================================================================================
SUMMARY - NDCG@k
====================================================================================================
Run                              |       @1 |       @3 |       @5 |      @10 |     @100
---------------------------------+----------+----------+----------+----------+---------
Dgraph v25.1.0                   |   0.6167 |   0.6310 |   0.6466 |   0.6806 |   0.7353
Dgraph v25.1.0 (higher ef)       |   0.6200 |   0.6389 |   0.6521 |   0.6852 |   0.7398
```

## BEIR Datasets

The framework supports any BEIR dataset. Recommended datasets for testing:

| Dataset | Documents | Queries | Domain | Size |
|---------|-----------|---------|--------|------|
| `scifact` | 5.2K | 300 | Scientific Claims | Small |
| `nfcorpus` | 3.6K | 323 | Medical | Small |
| `fiqa` | 57K | 648 | Financial | Medium |
| `arguana` | 8.7K | 1406 | Argumentation | Medium |
| `trec-covid` | 171K | 50 | COVID-19 | Large |

Datasets are automatically downloaded on first use and cached in `./data/`.

## Configuration Options

### Environment Variables

All configuration can be set via `.env` file or environment variables:

```bash
# Dgraph Configuration
DGRAPH_VERSION=v25.0.0
DGRAPH_HOST=localhost
DGRAPH_PORT=9080

# BEIR Configuration
DATASET_NAME=scifact
EMBEDDING_MODEL=all-MiniLM-L6-v2
BATCH_SIZE=32

# HNSW Index Configuration
HNSW_METRIC=euclidean
HNSW_MAX_LEVELS=3
HNSW_EF_SEARCH=40
HNSW_EF_CONSTRUCTION=100

# Evaluation Configuration
K_VALUES=1,3,5,10,100
DESC=  # Optional: Add a description for this benchmark run
```

### Configuration Options

#### HNSW Parameters

- **metric**: Distance metric (`euclidean`, `cosine`, `dotproduct`)
- **maxLevels**: Maximum number of layers in HNSW graph
- **efSearch**: Size of dynamic candidate list during search (higher = more accurate, slower)
- **efConstruction**: Size of dynamic candidate list during construction (higher = better quality, slower build)

#### Evaluation Options

- **K_VALUES**: Comma-separated list of k-values for metrics (default: `1,3,5,10,100`)
- **DESC**: Optional description for the benchmark run (e.g., `"Testing with higher efConstruction"`)
  - Included in results JSON and plot legends
  - Useful for tracking configuration changes across runs

## Evaluation Metrics

The benchmark reports standard IR metrics:

- **NDCG@k**: Normalized Discounted Cumulative Gain - measures ranking quality.
  It compares your ranking to an ideal ranking where all relevant documents are at the top, while discounting gains from lower-ranked positions. Higher NDCG@k means relevant documents are not just retrieved, but also ranked closer to the top of the list.
- **MAP@k**: Mean Average Precision - measures precision across recall levels.
  It averages the precision at every rank position where a relevant document appears, and then averages this over all queries. Higher MAP@k indicates that relevant documents tend to appear earlier and more consistently in the ranked results.
- **Recall@k**: Fraction of relevant documents retrieved in top-k.
  It focuses on coverage: out of all relevant documents for a query, how many are found within the first `k` results. Higher Recall@k means the system is missing fewer relevant documents, even if some may be ranked lower.
- **Precision@k**: Fraction of retrieved documents that are relevant.
  It focuses on purity: among the first `k` results, how many are actually relevant to the query. Higher Precision@k means users see fewer non-relevant documents in the top part of the ranking.
  
In this benchmark, common settings are `k=10` to capture the quality of the top search results, and `k=100` to understand overall coverage. If your use case is user-facing search where only the first page matters, prioritize **Precision@k** and **NDCG@k**. If you care more about finding as many relevant items as possible (e.g., analysis or recall-oriented workflows), prioritize **Recall@k** and **MAP@k**.

## Performance Analysis

Results include timing information:
- Embedding generation time
- Document insertion time
- Total search time and average per-query time

Example output:
```
NDCG@k:
  NDCG@1: 0.6890
  NDCG@10: 0.7234
  
Timing:
  embedding_time_seconds: 45.23
  insert_time_seconds: 12.67
  search_time_seconds: 8.92
  avg_query_time_seconds: 0.0297
```

## Running Manually

You can also run the benchmark manually without the shell script:

```bash
# Start Dgraph
docker-compose up -d

# Wait for Dgraph to be ready
curl http://localhost:8080/health

# Run the evaluation
uv run python evaluate.py

# Stop Dgraph
docker-compose down -v
```

## Troubleshooting

### Docker Issues

If Dgraph fails to start:
```bash
# Check container logs
docker-compose logs

# Clean restart
docker-compose down -v
docker-compose up -d
```

### Memory Issues

For large datasets, you may need to increase Docker memory limits or use a smaller dataset/batch size.

### Connection Issues

Ensure ports 8080 and 9080 are not already in use:
```bash
lsof -i :8080
lsof -i :9080
```

## Extending the Benchmark

### Adding New Datasets

Simply change `DATASET_NAME` in `.env` to any BEIR dataset name. The dataset will be downloaded automatically.

### Custom Embedding Models

Change `EMBEDDING_MODEL` to any sentence-transformers model:
- `all-MiniLM-L6-v2` (384 dim, fast)
- `all-mpnet-base-v2` (768 dim, better quality)
- `multi-qa-MiniLM-L6-cos-v1` (384 dim, optimized for QA)

## References

- [BEIR Benchmark](https://github.com/beir-cellar/beir)
- [Dgraph Documentation](https://dgraph.io/docs/)
- [Sentence Transformers](https://www.sbert.net/)
- [HNSW Algorithm](https://arxiv.org/abs/1603.09320)

## License

This benchmark suite follows the same license as the parent repository.
