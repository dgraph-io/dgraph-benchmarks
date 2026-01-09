#!/usr/bin/env python3
"""Insert BEIR benchmark reference data into benchmark_results.csv."""

import csv
import json
import tempfile
import shutil
from pathlib import Path


def insert_benchmark(benchmark_file: str, csv_file: str, platform_name: str = "BEIR Reference"):
    """Insert a benchmark JSON file as the first data row in the CSV."""
    
    # Load benchmark data
    with open(benchmark_file, 'r') as f:
        bench = json.load(f)
    
    csv_path = Path(csv_file)
    
    if not csv_path.exists():
        print(f"Error: CSV file not found: {csv_path}")
        return
    
    # Read existing CSV
    with open(csv_path, 'r', newline='') as f:
        reader = csv.DictReader(f)
        fieldnames = reader.fieldnames
        existing_rows = list(reader)
    
    if not fieldnames:
        print("Error: CSV file has no headers")
        return
    
    # Build benchmark row matching CSV columns
    metrics = bench.get("metrics", {})
    config = bench.get("config", {})
    
    benchmark_row = {
        "timestamp": "",
        "platform": platform_name,
        "description": bench.get("description", ""),
        "dataset": config.get("dataset", "scifact"),
        "embedding_model": config.get("embedding_model", ""),
        "version": "",
        "hnsw_config": "",
        "hnsw_metric": "",
        "hnsw_max_levels": "",
        "hnsw_ef_search": "",
        "hnsw_ef_construction": "",
        "ef_search_query": "",
        "distance_threshold": "",
        "use_new_syntax": "",
        "num_documents": "",
        "num_queries": "",
        "insert_time_seconds": "",
        "search_time_seconds": "",
        "avg_query_time_seconds": "",
    }
    
    # Add metric columns
    for k in [1, 3, 5, 10, 100]:
        benchmark_row[f"ndcg_{k}"] = metrics.get("ndcg", {}).get(f"NDCG@{k}", 0.0)
        benchmark_row[f"map_{k}"] = metrics.get("map", {}).get(f"MAP@{k}", 0.0)
        benchmark_row[f"recall_{k}"] = metrics.get("recall", {}).get(f"Recall@{k}", 0.0)
        benchmark_row[f"precision_{k}"] = metrics.get("precision", {}).get(f"P@{k}", 0.0)
    
    # Only include fields that exist in the CSV
    benchmark_row = {k: v for k, v in benchmark_row.items() if k in fieldnames}
    
    # Write back with benchmark as first row
    with tempfile.NamedTemporaryFile(mode='w', delete=False, newline='', suffix='.csv') as tmp:
        writer = csv.DictWriter(tmp, fieldnames=fieldnames)
        writer.writeheader()
        writer.writerow(benchmark_row)
        for row in existing_rows:
            writer.writerow(row)
        tmp_path = tmp.name
    
    # Replace original file
    shutil.move(tmp_path, csv_path)
    print(f"Inserted '{platform_name}' as first row in {csv_path}")


def main():
    import argparse
    parser = argparse.ArgumentParser(description='Insert BEIR benchmark into CSV')
    parser.add_argument('--benchmark', default='benchmarks/scifact_acceptable.json',
                       help='Path to benchmark JSON file')
    parser.add_argument('--csv', default='./results/benchmark_results.csv',
                       help='Path to CSV file')
    parser.add_argument('--name', default='BEIR Acceptable',
                       help='Platform name for the benchmark row')
    
    args = parser.parse_args()
    insert_benchmark(args.benchmark, args.csv, args.name)


if __name__ == "__main__":
    main()
