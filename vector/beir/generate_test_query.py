#!/usr/bin/env python3
"""Generate a test DQL query from the first corpus entry for manual testing in Ratel."""

import json
from pathlib import Path
from sentence_transformers import SentenceTransformer

# Configuration
CORPUS_FILE = "./data/scifact/corpus.jsonl"
EMBEDDING_MODEL = "all-MiniLM-L6-v2"
TOP_K = 10

def main():
    print("=" * 80)
    print("BEIR SciFact - Generate Test DQL Query")
    print("=" * 80)
    
    # Check if corpus file exists
    corpus_path = Path(CORPUS_FILE)
    if not corpus_path.exists():
        print(f"\nError: {CORPUS_FILE} not found!")
        print("Please run the benchmark first to download the dataset.")
        return
    
    # Read first entry from corpus
    print(f"\nReading first entry from {CORPUS_FILE}...")
    with open(corpus_path, 'r') as f:
        first_line = f.readline()
        doc = json.loads(first_line)
    
    doc_id = doc.get("_id")
    title = doc.get("title", "")
    text = doc.get("text", "")
    
    print(f"\nDocument ID: {doc_id}")
    print(f"Title: {title}")
    print(f"Text: {text[:200]}...")
    
    # Generate embedding
    print(f"\nLoading embedding model: {EMBEDDING_MODEL}...")
    model = SentenceTransformer(EMBEDDING_MODEL)
    
    print("Generating embedding...")
    combined_text = f"{title} {text}".strip()
    embedding = model.encode(combined_text, convert_to_numpy=True)
    embedding_list = embedding.tolist()
    
    print(f"Embedding dimension: {len(embedding_list)}")
    print(f"First 5 values: {embedding_list[:5]}")
    
    # Create DQL query
    vector_str = json.dumps(embedding_list)
    
    dql_query = f"""{{
  results(func: similar_to(embedding, {TOP_K}, "{vector_str}")) {{
    doc_id
    title
    text
  }}
}}"""
    
    print("\n" + "=" * 80)
    print("DQL QUERY FOR RATEL")
    print("=" * 80)
    print(dql_query)
    print("=" * 80)
    
    # Also save to file
    output_file = "test_query.dql"
    with open(output_file, 'w') as f:
        f.write(dql_query)
    
    print(f"\nQuery saved to: {output_file}")
    
    # Print instructions
    print("\n" + "=" * 80)
    print("INSTRUCTIONS")
    print("=" * 80)
    print("1. Open Ratel at: http://localhost:8000")
    print("2. Go to the 'Query' tab")
    print("3. Paste the query above")
    print("4. Click 'Run'")
    print("\nExpected: The first result should be the document itself (same doc_id)")
    print(f"Expected doc_id: {doc_id}")
    print(f"Expected title: {title}")
    print("=" * 80)
    
    # Also print a simpler version for quick testing
    print("\n" + "=" * 80)
    print("SIMPLIFIED QUERY (just IDs)")
    print("=" * 80)
    simple_query = f"""{{
  results(func: similar_to(embedding, {TOP_K}, "{vector_str}")) {{
    doc_id
  }}
}}"""
    print(simple_query)
    print("=" * 80)


if __name__ == "__main__":
    main()
