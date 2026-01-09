"""Main evaluation script for BEIR benchmark on Dgraph."""

import os
import csv
import time
import requests
from pathlib import Path
from typing import Dict, List, Optional

from beir.datasets.data_loader import GenericDataLoader
from beir.retrieval.evaluation import EvaluateRetrieval

from config import get_config
from embeddings import EmbeddingGenerator
from dgraph_client import DgraphVectorClient


def load_beir_dataset(dataset_name: str, data_dir: str = "./data"):
    """
    Load BEIR dataset, downloading if necessary.
    
    Args:
        dataset_name: Name of the BEIR dataset (e.g., 'scifact')
        data_dir: Directory to store/load data
        
    Returns:
        Tuple of (corpus, queries, qrels)
    """
    print(f"\nLoading BEIR dataset: {dataset_name}")
    
    data_path = os.path.join(data_dir, dataset_name)
    
    # Check if dataset exists, if not download it
    corpus_file = os.path.join(data_path, "corpus.jsonl")
    if not os.path.exists(corpus_file):
        print(f"Dataset not found locally. Downloading {dataset_name}...")
        from beir import util
        
        # Create data directory if it doesn't exist
        os.makedirs(data_dir, exist_ok=True)
        
        # Download dataset
        url = f"https://public.ukp.informatik.tu-darmstadt.de/thakur/BEIR/datasets/{dataset_name}.zip"
        data_path = util.download_and_unzip(url, data_dir)
        print(f"Dataset downloaded to: {data_path}")
    
    corpus, queries, qrels = GenericDataLoader(data_folder=data_path).load(split="test")
    
    print(f"Loaded {len(corpus)} documents, {len(queries)} queries")
    return corpus, queries, qrels


def get_dgraph_version(host: str = "localhost", port: int = 8080) -> Optional[str]:
    """
    Get Dgraph version from health endpoint.
    
    Args:
        host: Dgraph alpha host
        port: Dgraph alpha HTTP port
        
    Returns:
        Version string or None if unable to fetch
    """
    try:
        response = requests.get(f"http://{host}:{port}/health", timeout=5)
        response.raise_for_status()
        health_data = response.json()
        
        # Extract version from first instance
        if health_data and len(health_data) > 0:
            return health_data[0].get("version", "unknown")
        
        return "unknown"
    except Exception as e:
        print(f"Warning: Could not fetch Dgraph version from health endpoint: {e}")
        return "unknown"


def fetch_hnsw_internal_metrics(host: str = "localhost", port: int = 8080) -> Dict[str, float]:
    """Fetch internal HNSW metrics (e.g., visited_vectors_level_*) from Dgraph.

    Metrics are read from the Prometheus endpoint and filtered down to keys that
    start with "visited_vectors_level_". This is best-effort; on any error we
    simply return an empty dict so benchmarks still complete.
    """
    metrics: Dict[str, float] = {}

    try:
        # Prometheus endpoint for Alpha metrics
        resp = requests.get(f"http://{host}:{port}/metrics", timeout=5)
        resp.raise_for_status()

        for line in resp.text.splitlines():
            # Skip comments and empty lines
            if not line or line.startswith("#"):
                continue

            if line.startswith("visited_vectors_level_"):
                parts = line.split()
                if len(parts) != 2:
                    continue
                name, value = parts
                try:
                    metrics[name] = float(value)
                except ValueError:
                    continue
    except Exception as e:
        print(f"Warning: Could not fetch HNSW internal metrics: {e}")

    return metrics


def fetch_cache_metrics(host: str = "localhost", port: int = 8080) -> Dict[str, float]:
    """Fetch posting list cache metrics from Dgraph Prometheus endpoint."""
    target_metrics = [
        "dgraph_hit_ratio_posting_cache",
    ]
    metrics: Dict[str, float] = {}

    try:
        resp = requests.get(f"http://{host}:{port}/debug/prometheus_metrics", timeout=5)
        resp.raise_for_status()

        for line in resp.text.splitlines():
            if not line or line.startswith("#"):
                continue
            for target in target_metrics:
                if line.startswith(target):
                    # Handle labels like {method="",status=""} - value is after the closing brace
                    if "{" in line:
                        value_part = line.split("}")[-1].strip()
                    else:
                        value_part = line.split()[-1]
                    try:
                        metrics[target] = float(value_part)
                    except ValueError:
                        pass
                    break
    except Exception as e:
        print(f"Warning: Could not fetch cache metrics: {e}")

    return metrics


def save_results_to_csv(results: Dict, k_values: List[int], output_file: str = "./results/benchmark_results.csv"):
    """Append evaluation results to CSV file."""
    output_path = Path(output_file)
    output_path.parent.mkdir(parents=True, exist_ok=True)
    
    # Build flattened row (some fields are optional for cross-platform compatibility)
    config = results["config"]
    hnsw_params = config.get("hnsw_params", {})
    search_params = config.get("search_params", {})
    
    row = {
        "timestamp": results["timestamp"],
        "platform": config["platform"],
        "description": config.get("description", ""),
        "dataset": config["dataset"],
        "embedding_model": config["embedding_model"],
        "version": config.get("dgraph_version", config.get("weaviate_version", "")),
        "hnsw_config": config.get("hnsw_config", ""),
        "hnsw_metric": hnsw_params.get("metric", ""),
        "hnsw_max_levels": hnsw_params.get("max_levels", ""),
        "hnsw_ef_search": hnsw_params.get("ef_search", ""),
        "hnsw_ef_construction": hnsw_params.get("ef_construction", ""),
        "ef_search_query": search_params.get("ef_search_query", ""),
        "distance_threshold": search_params.get("distance_threshold", ""),
        "use_new_syntax": search_params.get("use_new_syntax", ""),
        "num_documents": results["dataset_info"]["num_documents"],
        "num_queries": results["dataset_info"]["num_queries"],
        # Performance metrics
        "insert_time_seconds": results["timing"]["insert_time_seconds"],
        "search_time_seconds": results["timing"]["search_time_seconds"],
        "avg_query_time_seconds": results["timing"]["avg_query_time_seconds"],
    }
    
    # Add NDCG, MAP, Recall, Precision for each k value
    for k in k_values:
        row[f"ndcg_{k}"] = results["metrics"]["ndcg"].get(f"NDCG@{k}", 0.0)
        row[f"map_{k}"] = results["metrics"]["map"].get(f"MAP@{k}", 0.0)
        row[f"recall_{k}"] = results["metrics"]["recall"].get(f"Recall@{k}", 0.0)
        row[f"precision_{k}"] = results["metrics"]["precision"].get(f"P@{k}", 0.0)
    
    # Check if file exists to determine if we need to write header
    file_exists = output_path.exists()
    
    with open(output_file, 'a', newline='') as f:
        writer = csv.DictWriter(f, fieldnames=row.keys())
        if not file_exists:
            writer.writeheader()
        writer.writerow(row)
    
    print(f"\nResults appended to: {output_file}")


def main():
    """Main evaluation pipeline."""
    # Load configuration
    config = get_config()
    
    print("=" * 80)
    print("BEIR Dgraph Vector Search Benchmark")
    print("=" * 80)
    print("\nConfiguration:")
    print(f"  Dataset: {config.beir.dataset_name}")
    print(f"  Embedding Model: {config.beir.embedding_model}")
    print(f"  Dgraph Version: {config.dgraph.version}")
    print(f"  Dgraph Endpoint: {config.dgraph.grpc_endpoint}")
    print(f"  HNSW Config: {config.hnsw.to_index_string()}")
    print(f"  K Values: {config.evaluation.k_values}")
    print("=" * 80)
    
    # Load BEIR dataset
    corpus, queries, qrels = load_beir_dataset(
        config.beir.dataset_name, 
        config.beir.data_dir
    )
    
    # Initialize embedding generator
    embedding_gen = EmbeddingGenerator(
        model_name=config.beir.embedding_model,
        batch_size=config.beir.batch_size
    )
    # For partitioned HNSW indexes, record the embedding dimension so the
    # index definition can include vectorDimension.
    if config.hnsw.index_type == "partionedhnsw":
        config.hnsw.vector_dim = embedding_gen.embedding_dim
    
    # Generate embeddings for corpus
    print("\n" + "=" * 80)
    print("Generating Embeddings")
    print("=" * 80)
    start_time = time.time()
    
    corpus_embeddings = embedding_gen.embed_corpus(corpus, dataset_name=config.beir.dataset_name)
    query_embeddings = embedding_gen.embed_queries(queries, dataset_name=config.beir.dataset_name)
    
    embedding_time = time.time() - start_time
    print(f"\nEmbedding generation took {embedding_time:.2f} seconds")
    
    # Initialize Dgraph client
    print("\n" + "=" * 80)
    print("Setting up Dgraph")
    print("=" * 80)
    dgraph_client = DgraphVectorClient(config.dgraph)
    
    # Reset and setup schema
    dgraph_client.reset_database()
    dgraph_client.setup_schema(config.hnsw)
    
    # Get actual Dgraph version from health endpoint
    dgraph_version = get_dgraph_version(config.dgraph.host, 8080)
    print(f"Detected Dgraph version: {dgraph_version}")
    
    # Insert documents
    print("\n" + "=" * 80)
    print("Inserting Documents")
    print("=" * 80)
    insert_start = time.time()
    
    dgraph_client.insert_documents(
        corpus, 
        corpus_embeddings,
        batch_size=config.beir.insert_batch_size
    )
    
    insert_time = time.time() - insert_start
    print(f"\nDocument insertion took {insert_time:.2f} seconds")
    
    # Perform vector search
    print("\n" + "=" * 80)
    print("Performing Vector Search")
    print("=" * 80)
    search_start = time.time()
    
    max_k = max(config.evaluation.k_values)
    search_results = dgraph_client.batch_vector_search(
        query_embeddings,
        top_k=max_k,
        ef=config.search.ef_search_query,
        distance_threshold=config.search.distance_threshold,
        use_new_syntax=config.search.use_new_syntax
    )
    
    search_time = time.time() - search_start
    print(f"\nVector search took {search_time:.2f} seconds")
    print(f"Average time per query: {search_time / len(queries):.4f} seconds")
    
    # Evaluate results
    print("\n" + "=" * 80)
    print("Evaluating Results")
    print("=" * 80)
    
    evaluator = EvaluateRetrieval()
    ndcg, _map, recall, precision = evaluator.evaluate(
        qrels, 
        search_results, 
        k_values=config.evaluation.k_values
    )
    
    # Print results
    print("\n" + "=" * 80)
    print("RESULTS")
    print("=" * 80)
    
    print("\nNDCG@k:")
    for k in config.evaluation.k_values:
        score = ndcg.get(f"NDCG@{k}", 0.0)
        print(f"  NDCG@{k}: {score:.4f}")
    
    print("\nMAP@k:")
    for k in config.evaluation.k_values:
        score = _map.get(f"MAP@{k}", 0.0)
        print(f"  MAP@{k}: {score:.4f}")
    
    print("\nRecall@k:")
    for k in config.evaluation.k_values:
        score = recall.get(f"Recall@{k}", 0.0)
        print(f"  Recall@{k}: {score:.4f}")
    
    print("\nPrecision@k:")
    for k in config.evaluation.k_values:
        score = precision.get(f"P@{k}", 0.0)
        print(f"  P@{k}: {score:.4f}")
    
    # Compile all results
    all_results = {
        "timestamp": time.strftime("%Y-%m-%d %H:%M:%S"),
        "config": {
            "platform": "Dgraph",
            "dataset": config.beir.dataset_name,
            "embedding_model": config.beir.embedding_model,
            "dgraph_version": dgraph_version,
            "hnsw_config": config.hnsw.to_index_string(),
            "hnsw_params": {
                "metric": config.hnsw.metric,
                "max_levels": config.hnsw.max_levels,
                "ef_search": config.hnsw.ef_search,
                "ef_construction": config.hnsw.ef_construction,
            },
            "search_params": {
                "ef_search_query": config.search.ef_search_query,
                "distance_threshold": config.search.distance_threshold,
                "use_new_syntax": config.search.use_new_syntax,
            },
            "description": config.evaluation.description,
        },
        "timing": {
            "insert_time_seconds": insert_time,
            "search_time_seconds": search_time,
            "avg_query_time_seconds": search_time / len(queries),
        },
        "metrics": {
            "ndcg": ndcg,
            "map": _map,
            "recall": recall,
            "precision": precision,
        },
        "dataset_info": {
            "num_documents": len(corpus),
            "num_queries": len(queries),
        }
    }
    
    # Save results to CSV (appends to existing file)
    save_results_to_csv(all_results, config.evaluation.k_values)
    
    # Fetch and display cache metrics before stopping Dgraph
    print("\n" + "=" * 80)
    print("Cache Metrics")
    print("=" * 80)
    cache_metrics = fetch_cache_metrics(config.dgraph.host, 8080)
    if cache_metrics:
        for name, value in cache_metrics.items():
            print(f"  {name}: {value}")
    else:
        print("  No cache metrics available")
    
    # Close connection
    dgraph_client.close()
    
    print("\n" + "=" * 80)
    print("Evaluation Complete!")
    print("=" * 80)


if __name__ == "__main__":
    main()
