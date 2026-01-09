"""Dgraph client for BEIR benchmark."""

import json
from typing import List, Dict, Tuple
import pydgraph
import numpy as np
from tqdm import tqdm

from config import DgraphConfig, HNSWConfig


class DgraphVectorClient:
    """Client for interacting with Dgraph for vector search."""
    
    def __init__(self, config: DgraphConfig):
        """
        Initialize Dgraph client.
        
        Args:
            config: Dgraph configuration
        """
        self.config = config
        
        # Increase gRPC buffer limits for large vector batches
        # Default is 4MB, increase to 256MB for batch inserts
        grpc_options = [
            ('grpc.max_send_message_length', 256 * 1024 * 1024),
            ('grpc.max_receive_message_length', 256 * 1024 * 1024),
        ]
        self.client_stub = pydgraph.DgraphClientStub(
            addr=config.grpc_endpoint,
            options=grpc_options
        )
        self.client = pydgraph.DgraphClient(self.client_stub)
        print(f"Connected to Dgraph at {config.grpc_endpoint} (256MB gRPC buffers)")
    
    def setup_schema(self, hnsw_config: HNSWConfig):
        """
        Set up the Dgraph schema for BEIR documents.
        
        Args:
            hnsw_config: HNSW index configuration
        """
        print("Setting up Dgraph schema...")
        
        schema = f"""
            doc_id: string @index(hash) .
            title: string @index(term, fulltext) .
            text: string @index(fulltext) .
            embedding: float32vector @index({hnsw_config.to_index_string()}) .
            
            type Document {{
                doc_id
                title
                text
                embedding
            }}
        """
        
        op = pydgraph.Operation(schema=schema)
        self.client.alter(op)
        print("Schema setup complete")
    
    def reset_database(self):
        """Drop all data and schema."""
        print("Dropping all data and schema...")
        op = pydgraph.Operation(drop_all=True)
        self.client.alter(op)
        print("Database reset complete")
    
    def insert_documents(
        self, 
        corpus: Dict[str, Dict[str, str]], 
        embeddings: Dict[str, np.ndarray],
        batch_size: int = 1000
    ):
        """
        Insert documents with embeddings into Dgraph.
        
        Args:
            corpus: BEIR corpus dictionary
            embeddings: Dictionary mapping doc_id to embedding
            batch_size: Number of documents to insert per batch
        """
        doc_ids = list(corpus.keys())
        total_docs = len(doc_ids)
        print(f"Inserting {total_docs} documents in batches of {batch_size}...")
        
        for i in tqdm(range(0, total_docs, batch_size), desc="Inserting documents"):
            batch_ids = doc_ids[i:i+batch_size]
            
            # Prepare mutations
            mutations = []
            for doc_id in batch_ids:
                doc = corpus[doc_id]
                embedding = embeddings[doc_id].tolist()
                
                doc_obj = {
                    "dgraph.type": "Document",
                    "doc_id": doc_id,
                    "title": doc.get("title", ""),
                    "text": doc.get("text", ""),
                    "embedding": json.dumps(embedding)
                }
                
                mutations.append(doc_obj)
            
            # Execute transaction
            txn = self.client.txn()
            try:
                txn.mutate(set_obj=mutations)
                txn.commit()
            finally:
                txn.discard()
        
        print(f"Inserted {total_docs} documents")
    
    def vector_search(
        self, 
        query_embedding: np.ndarray, 
        top_k: int = 10,
        ef: int = 0,
        distance_threshold: float = 0.0,
        use_new_syntax: bool = False
    ) -> List[Tuple[str, float]]:
        """
        Perform vector similarity search with computed similarity scores.
        
        Args:
            query_embedding: Query embedding vector
            top_k: Number of results to return
            ef: Override efSearch for this query (PR#9514, requires use_new_syntax=True)
            distance_threshold: Filter results by distance threshold (PR#9514, requires use_new_syntax=True)
            use_new_syntax: Use new similar_to syntax from PR#9514
            
        Returns:
            List of tuples (doc_id, score) sorted by similarity (higher is better)
        """
        query_vector = query_embedding.tolist()
        vector_str = json.dumps(query_vector)
        
        # Build parameterized query with variables
        variables = {
            "$v": vector_str,
            "$topK": str(top_k),
        }
        
        # Build similar_to function call based on syntax version
        if use_new_syntax:
            # New syntax from PR#9514: similar_to(field, topK, $v, ef: $ef, distance_threshold: $threshold)
            param_parts = []
            var_declarations = ["$v: float32vector", "$topK: int"]
            
            if ef > 0:
                param_parts.append("ef: $ef")
                var_declarations.append("$ef: int")
                variables["$ef"] = str(ef)
            if distance_threshold > 0.0:
                param_parts.append("distance_threshold: $threshold")
                var_declarations.append("$threshold: float")
                variables["$threshold"] = str(distance_threshold)
            
            var_decl_str = ", ".join(var_declarations)
            if param_parts:
                params_str = ", ".join(param_parts)
                similar_to_func = f"similar_to(embedding, $topK, $v, {params_str})"
            else:
                similar_to_func = "similar_to(embedding, $topK, $v)"
            
            query = f"""
            query search({var_decl_str}) {{
                results(func: {similar_to_func}) {{
                    doc_id
                    embedding
                }}
            }}
            """
        else:
            similar_to_func = f'similar_to(embedding, {top_k}, "{vector_str}")'
            query = f"""
            {{
                results(func: {similar_to_func}) {{
                    doc_id
                    embedding
                }}
            }}
            """
            variables = None
        
        txn = self.client.txn(read_only=True, best_effort=True)
        try:
            res = txn.query(query, variables=variables)
            results = json.loads(res.json)
            
            # Debug: Log if no results found (only for first few queries)
            if not results.get("results") and use_new_syntax:
                import os
                if os.getenv("DEBUG_QUERIES", "false").lower() == "true":
                    print("\n[DEBUG] Query returned 0 results:")
                    print(f"  similar_to function: {similar_to_func}")
                    print(f"  Full query: {query[:200]}...")
                    if res.json:
                        print(f"  Response: {res.json[:500]}")
            
            # Calculate similarity scores manually
            doc_results = []
            for doc in results.get("results", []):
                doc_id = doc.get("doc_id")
                embedding_data = doc.get("embedding")
                
                if doc_id and embedding_data:
                    # Embedding is already a list (parsed by json.loads(res.json))
                    # If it's a string, parse it; otherwise use directly
                    if isinstance(embedding_data, str):
                        doc_embedding = np.array(json.loads(embedding_data))
                    else:
                        doc_embedding = np.array(embedding_data)
                    
                    # Calculate cosine similarity (higher is more similar)
                    # cosine_sim = dot(a,b) / (||a|| * ||b||)
                    dot_product = np.dot(query_embedding, doc_embedding)
                    query_norm = np.linalg.norm(query_embedding)
                    doc_norm = np.linalg.norm(doc_embedding)
                    
                    cosine_similarity = dot_product / (query_norm * doc_norm)
                    
                    doc_results.append((doc_id, float(cosine_similarity)))
            
            # Results should already be sorted by Dgraph, but ensure they're sorted by our score
            doc_results.sort(key=lambda x: x[1], reverse=True)
            
            return doc_results[:top_k]
        finally:
            txn.discard()
    
    def batch_vector_search(
        self,
        query_embeddings: Dict[str, np.ndarray],
        top_k: int = 10,
        ef: int = 0,
        distance_threshold: float = 0.0,
        use_new_syntax: bool = False
    ) -> Dict[str, Dict[str, float]]:
        """
        Perform batch vector search.
        
        Args:
            query_embeddings: Dictionary mapping query_id to embedding
            top_k: Number of results per query
            ef: Override efSearch for queries (PR#9514)
            distance_threshold: Filter results by distance threshold (PR#9514)
            use_new_syntax: Use new similar_to syntax from PR#9514
            
        Returns:
            Dictionary mapping query_id to {doc_id: score}
        """
        search_results = {}
        
        for query_id, query_embedding in tqdm(query_embeddings.items(), desc="Searching queries"):
            search_results[query_id] = {}
            doc_scores = self.vector_search(
                query_embedding, 
                top_k, 
                ef=ef,
                distance_threshold=distance_threshold,
                use_new_syntax=use_new_syntax
            )
            
            for doc_id, score in doc_scores:
                search_results[query_id][doc_id] = score
        
        return search_results
    
    def close(self):
        """Close the Dgraph client connection."""
        self.client_stub.close()
        print("Dgraph connection closed")
