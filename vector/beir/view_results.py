#!/usr/bin/env python3
"""Terminal-based viewer for benchmark results CSV with ASCII bar charts."""

import csv
import argparse
import shutil
from pathlib import Path
from typing import List, Dict, Any


# Global width override (set via --width flag)
_terminal_width_override: int = 0


def get_terminal_width() -> int:
    """Get terminal width, checking override, env var, then shutil."""
    import os
    
    # Use override if set
    if _terminal_width_override > 0:
        return _terminal_width_override
    
    # Try COLUMNS env var (often set correctly in terminals)
    try:
        columns = os.environ.get('COLUMNS')
        if columns:
            return int(columns)
    except (ValueError, TypeError):
        pass
    
    # Try shutil (may return 80 if not a real terminal)
    try:
        width = shutil.get_terminal_size().columns
        # If we get exactly 80, it's likely the default fallback
        # Use a larger default for better display
        if width == 80:
            return 140
        return width
    except Exception:
        return 140


def load_csv_results(csv_path: str) -> List[Dict[str, Any]]:
    """Load results from CSV file."""
    results = []
    with open(csv_path, 'r') as f:
        reader = csv.DictReader(f)
        for row in reader:
            # Convert numeric fields
            for key in row:
                if key in ['num_documents', 'num_queries']:
                    row[key] = int(row[key]) if row[key] else 0
                elif any(key.startswith(p) for p in ['ndcg_', 'map_', 'recall_', 'precision_', 'insert_time', 'search_time', 'avg_query']):
                    row[key] = float(row[key]) if row[key] else 0.0
            results.append(row)
    return results


def get_label(row: Dict[str, Any]) -> str:
    """Generate a label for a result row."""
    platform = row.get('platform', 'Unknown')
    version = row.get('version', '')
    desc = row.get('description', '')
    
    label = platform
    if version:
        label += f" {version}"
    if desc:
        label += f" ({desc})"
    return label  # Don't truncate here, let display functions handle it


def ascii_bar(value: float, max_value: float = 1.0, width: int = 30) -> str:
    """Create an ASCII bar representation."""
    if max_value == 0:
        return ""
    filled = int((value / max_value) * width)
    filled = min(filled, width)
    bar = "█" * filled + "░" * (width - filled)
    return bar


def print_table(results: List[Dict[str, Any]]):
    """Print results as a formatted table."""
    if not results:
        print("No results found.")
        return
    
    # Get all columns
    cols = list(results[0].keys())
    
    # Calculate column widths
    widths = {col: max(len(col), max(len(str(row.get(col, ''))) for row in results)) for col in cols}
    
    # Print header
    header = " | ".join(f"{col:<{widths[col]}}" for col in cols)
    print(header)
    print("-" * len(header))
    
    # Print rows
    for row in results:
        line = " | ".join(f"{str(row.get(col, '')):<{widths[col]}}" for col in cols)
        print(line)


def print_metrics_comparison(results: List[Dict[str, Any]], metric: str = 'ndcg'):
    """Print ASCII bar chart comparison for a metric."""
    k_values = [1, 3, 5, 10, 100]
    
    metric_names = {
        'ndcg': 'NDCG',
        'map': 'MAP', 
        'recall': 'Recall',
        'precision': 'Precision'
    }
    
    term_width = get_terminal_width()
    print(f"\n{'=' * term_width}")
    print(f"{metric_names.get(metric, metric).upper()}@k Comparison")
    print(f"{'=' * term_width}")
    
    # Calculate max label length based on terminal width
    # Format: "  {label} {bar:30} {value:.4f}" = 2 + label + 1 + 30 + 1 + 7 = 41 + label
    bar_width = 30
    labels = [get_label(r) for r in results]
    max_label_len = term_width - bar_width - 12  # 12 = indent(2) + spaces(2) + value(8)
    max_label_len = max(20, min(max_label_len, max(len(label) for label in labels) if labels else 20))
    
    # Truncate labels to fit
    labels = [label[:max_label_len] for label in labels]
    
    for k in k_values:
        col_name = f"{metric}_{k}"
        print(f"\n{metric_names.get(metric, metric)}@{k}:")
        print("-" * term_width)
        
        # Get all values for this metric to determine scaling
        values = []
        for row in results:
            v = row.get(col_name, 0.0)
            values.append(float(v) if isinstance(v, str) and v else float(v or 0.0))
        
        # Use relative scaling if max value is small, otherwise scale to 1.0
        max_val = max(values) if values else 0.0
        scale_to = 1.0 if max_val > 0.5 else (max_val if max_val > 0 else 1.0)
        
        for label, value in zip(labels, values, strict=True):
            bar = ascii_bar(value, scale_to, bar_width)
            print(f"  {label:<{max_label_len}} {bar} {value:.4f}")


def print_timing_comparison(results: List[Dict[str, Any]]):
    """Print ASCII bar chart comparison for timing metrics."""
    term_width = get_terminal_width()
    bar_width = 30
    
    # Filter to only rows with timing data
    timed_results = [
        r for r in results 
        if float(r.get('insert_time_seconds', 0) or 0) > 0 
        or float(r.get('search_time_seconds', 0) or 0) > 0
    ]
    
    if not timed_results:
        print("\nNo timing data available.")
        return
    
    print(f"\n{'=' * term_width}")
    print("Timing Comparison")
    print(f"{'=' * term_width}")
    
    labels = [get_label(r) for r in timed_results]
    max_label_len = term_width - bar_width - 15  # extra space for units like "ms"
    max_label_len = max(20, min(max_label_len, max(len(label) for label in labels) if labels else 20))
    labels = [label[:max_label_len] for label in labels]
    
    # Insert time
    print("\nInsert Time (seconds):")
    print("-" * term_width)
    max_insert = max(float(r.get('insert_time_seconds', 0) or 0) for r in timed_results)
    for row, label in zip(timed_results, labels, strict=True):
        value = float(row.get('insert_time_seconds', 0) or 0)
        bar = ascii_bar(value, max_insert, bar_width) if max_insert > 0 else ""
        print(f"  {label:<{max_label_len}} {bar} {value:.2f}s")
    
    # Search time
    print("\nSearch Time (seconds):")
    print("-" * term_width)
    max_search = max(float(r.get('search_time_seconds', 0) or 0) for r in timed_results)
    for row, label in zip(timed_results, labels, strict=True):
        value = float(row.get('search_time_seconds', 0) or 0)
        bar = ascii_bar(value, max_search, bar_width) if max_search > 0 else ""
        print(f"  {label:<{max_label_len}} {bar} {value:.2f}s")
    
    # Avg query time (in ms)
    print("\nAvg Query Time (milliseconds):")
    print("-" * term_width)
    max_avg = max(float(r.get('avg_query_time_seconds', 0) or 0) * 1000 for r in timed_results)
    for row, label in zip(timed_results, labels, strict=True):
        value = float(row.get('avg_query_time_seconds', 0) or 0) * 1000
        bar = ascii_bar(value, max_avg, bar_width) if max_avg > 0 else ""
        print(f"  {label:<{max_label_len}} {bar} {value:.2f}ms")


def print_summary_table(results: List[Dict[str, Any]]):
    """Print a compact summary table."""
    print(f"\n{'=' * 100}")
    print("SUMMARY - NDCG@k")
    print(f"{'=' * 100}")
    
    labels = [get_label(r) for r in results]
    max_label_len = max(len(label) for label in labels) if labels else 20
    
    # Header
    header = f"{'Run':<{max_label_len}} | {'@1':>8} | {'@3':>8} | {'@5':>8} | {'@10':>8} | {'@100':>8}"
    print(header)
    print("-" * len(header))
    
    # Rows
    for row, label in zip(results, labels, strict=True):
        line = f"{label:<{max_label_len}} | "
        for k in [1, 3, 5, 10, 100]:
            value = float(row.get(f'ndcg_{k}', 0) or 0)
            line += f"{value:>8.4f} | "
        print(line)
    
    print(f"{'=' * 100}")
    
    # Timing summary
    print(f"\n{'=' * 100}")
    print("TIMING SUMMARY")
    print(f"{'=' * 100}")
    
    header = f"{'Run':<{max_label_len}} | {'Insert (s)':>12} | {'Search (s)':>12} | {'Avg Query (ms)':>15}"
    print(header)
    print("-" * len(header))
    
    for row, label in zip(results, labels, strict=True):
        insert = float(row.get('insert_time_seconds', 0) or 0)
        search = float(row.get('search_time_seconds', 0) or 0)
        avg_query = float(row.get('avg_query_time_seconds', 0) or 0) * 1000
        print(f"{label:<{max_label_len}} | {insert:>12.2f} | {search:>12.2f} | {avg_query:>15.2f}")
    
    print(f"{'=' * 100}\n")


def main():
    parser = argparse.ArgumentParser(
        description='View benchmark results from CSV with ASCII bar charts',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # View summary table
  python view_results.py
  
  # View with bar charts for NDCG
  python view_results.py --bars
  
  # View specific metric
  python view_results.py --bars --metric recall
  
  # View timing comparison
  python view_results.py --timing
  
  # View raw table
  python view_results.py --raw
  
  # Filter by platform
  python view_results.py --platform Dgraph
        """
    )
    
    parser.add_argument('csv_file', nargs='?', default='./results/benchmark_results.csv',
                       help='CSV file to view (default: ./results/benchmark_results.csv)')
    parser.add_argument('--bars', action='store_true',
                       help='Show ASCII bar charts for metrics')
    parser.add_argument('--metric', choices=['ndcg', 'map', 'recall', 'precision', 'all'], default='all',
                       help='Metric to display in bar charts (default: all)')
    parser.add_argument('--timing', action='store_true',
                       help='Show timing comparison with bars')
    parser.add_argument('--raw', action='store_true',
                       help='Show raw table view')
    parser.add_argument('--platform', help='Filter by platform (e.g., Dgraph, Weaviate)')
    parser.add_argument('--last', type=int, metavar='N',
                       help='Show only the last N results')
    parser.add_argument('--width', type=int, metavar='W',
                       help='Override terminal width (default: auto-detect)')
    
    args = parser.parse_args()
    
    # Set width override if specified
    global _terminal_width_override
    if args.width:
        _terminal_width_override = args.width
    
    csv_path = Path(args.csv_file)
    if not csv_path.exists():
        print(f"Error: CSV file not found: {csv_path}")
        return
    
    results = load_csv_results(str(csv_path))
    
    if not results:
        print("No results found in CSV file.")
        return
    
    # Apply filters
    if args.platform:
        results = [r for r in results if r.get('platform', '').lower() == args.platform.lower()]
    
    if args.last:
        results = results[-args.last:]
    
    if not results:
        print("No results match the filter criteria.")
        return
    
    print(f"Loaded {len(results)} result(s) from {csv_path.name}")
    
    if args.raw:
        print_table(results)
    elif args.bars:
        if args.metric == 'all':
            for metric in ['ndcg', 'map', 'recall', 'precision']:
                print_metrics_comparison(results, metric)
        else:
            print_metrics_comparison(results, args.metric)
        if args.timing:
            print_timing_comparison(results)
    elif args.timing:
        print_timing_comparison(results)
    else:
        # Default: summary table
        print_summary_table(results)
        

if __name__ == "__main__":
    main()
