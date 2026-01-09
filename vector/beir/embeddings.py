"""Embedding generation using sentence-transformers."""

import hashlib
import os
from pathlib import Path
from typing import List, Dict, Optional
import numpy as np
from sentence_transformers import SentenceTransformer


class EmbeddingGenerator:
    """Generate embeddings using sentence-transformers."""
    
    def __init__(self, model_name: str = "all-MiniLM-L6-v2", batch_size: int = 32, cache_dir: Optional[str] = None):
        """
        Initialize the embedding generator.
        
        Args:
            model_name: Name of the sentence-transformers model
            batch_size: Batch size for encoding
            cache_dir: Directory to cache embeddings (default: ~/.cache/beir_embeddings)
        """
        print(f"Loading embedding model: {model_name}")
        self.model = SentenceTransformer(model_name)
        self.model_name = model_name
        self.batch_size = batch_size
        self.embedding_dim = self.model.get_sentence_embedding_dimension()
        
        if cache_dir is None:
            cache_dir = os.path.join(Path.home(), ".cache", "beir_embeddings")
        self.cache_dir = Path(cache_dir)
        self.cache_dir.mkdir(parents=True, exist_ok=True)
        
        print(f"Model loaded. Embedding dimension: {self.embedding_dim}")
        print(f"Cache directory: {self.cache_dir}")
    
    def _get_cache_path(self, dataset_name: str, data_type: str) -> Path:
        """Get the cache file path for a dataset."""
        safe_model_name = self.model_name.replace("/", "_")
        filename = f"{dataset_name}_{data_type}_{safe_model_name}.npz"
        return self.cache_dir / filename
    
    def _compute_content_hash(self, ids: List[str], texts: List[str]) -> str:
        """Compute a hash of the content for cache validation."""
        content = "|".join(f"{id_}:{text[:100]}" for id_, text in zip(ids, texts, strict=True))
        return hashlib.md5(content.encode(), usedforsecurity=False).hexdigest()
    
    def _load_cached_embeddings(self, cache_path: Path, content_hash: str) -> Optional[Dict[str, np.ndarray]]:
        """Load embeddings from cache if valid."""
        if not cache_path.exists():
            return None
        
        try:
            data = np.load(cache_path, allow_pickle=True)
            if data.get("content_hash", "") != content_hash:
                print("Cache content mismatch, regenerating embeddings...")
                return None
            
            ids = data["ids"]
            embeddings = data["embeddings"]
            print(f"Loaded {len(ids)} cached embeddings from {cache_path.name}")
            return {id_: emb for id_, emb in zip(ids, embeddings, strict=True)}
        except Exception as e:
            print(f"Failed to load cache: {e}")
            return None
    
    def _save_embeddings_to_cache(self, cache_path: Path, embedding_map: Dict[str, np.ndarray], content_hash: str):
        """Save embeddings to cache."""
        ids = list(embedding_map.keys())
        embeddings = np.array([embedding_map[id_] for id_ in ids])
        np.savez(cache_path, ids=ids, embeddings=embeddings, content_hash=content_hash)
        print(f"Saved {len(ids)} embeddings to cache: {cache_path.name}")
    
    def embed_texts(self, texts: List[str], show_progress: bool = True) -> np.ndarray:
        """
        Generate embeddings for a list of texts.
        
        Args:
            texts: List of text strings to embed
            show_progress: Whether to show progress bar
            
        Returns:
            numpy array of embeddings with shape (len(texts), embedding_dim)
        """
        embeddings = self.model.encode(
            texts,
            batch_size=self.batch_size,
            show_progress_bar=show_progress,
            convert_to_numpy=True
        )
        return embeddings
    
    def embed_corpus(self, corpus: Dict[str, Dict[str, str]], show_progress: bool = True, 
                      dataset_name: Optional[str] = None, use_cache: bool = True) -> Dict[str, np.ndarray]:
        """
        Generate embeddings for BEIR corpus.
        
        Args:
            corpus: BEIR corpus dictionary {doc_id: {"title": ..., "text": ...}}
            show_progress: Whether to show progress bar
            dataset_name: Name of the dataset (used for caching)
            use_cache: Whether to use cached embeddings if available
            
        Returns:
            Dictionary mapping doc_id to embedding
        """
        doc_ids = list(corpus.keys())
        
        # Combine title and text for each document
        texts = []
        for doc_id in doc_ids:
            title = corpus[doc_id].get("title", "")
            text = corpus[doc_id].get("text", "")
            combined_text = f"{title} {text}".strip()
            texts.append(combined_text)
        
        # Try to load from cache
        if use_cache and dataset_name:
            cache_path = self._get_cache_path(dataset_name, "corpus")
            content_hash = self._compute_content_hash(doc_ids, texts)
            cached = self._load_cached_embeddings(cache_path, content_hash)
            if cached is not None:
                return cached
        
        print(f"Embedding {len(texts)} documents...")
        embeddings = self.embed_texts(texts, show_progress=show_progress)
        
        # Create mapping
        embedding_map = {}
        for doc_id, embedding in zip(doc_ids, embeddings, strict=True):
            embedding_map[doc_id] = embedding
        
        # Save to cache
        if use_cache and dataset_name:
            self._save_embeddings_to_cache(cache_path, embedding_map, content_hash)
        
        return embedding_map
    
    def embed_queries(self, queries: Dict[str, str], show_progress: bool = True,
                       dataset_name: Optional[str] = None, use_cache: bool = True) -> Dict[str, np.ndarray]:
        """
        Generate embeddings for BEIR queries.
        
        Args:
            queries: BEIR queries dictionary {query_id: query_text}
            show_progress: Whether to show progress bar
            dataset_name: Name of the dataset (used for caching)
            use_cache: Whether to use cached embeddings if available
            
        Returns:
            Dictionary mapping query_id to embedding
        """
        query_ids = list(queries.keys())
        query_texts = [queries[qid] for qid in query_ids]
        
        # Try to load from cache
        if use_cache and dataset_name:
            cache_path = self._get_cache_path(dataset_name, "queries")
            content_hash = self._compute_content_hash(query_ids, query_texts)
            cached = self._load_cached_embeddings(cache_path, content_hash)
            if cached is not None:
                return cached
        
        print(f"Embedding {len(query_texts)} queries...")
        embeddings = self.embed_texts(query_texts, show_progress=show_progress)
        
        # Create mapping
        embedding_map = {}
        for query_id, embedding in zip(query_ids, embeddings, strict=True):
            embedding_map[query_id] = embedding
        
        # Save to cache
        if use_cache and dataset_name:
            self._save_embeddings_to_cache(cache_path, embedding_map, content_hash)
        
        return embedding_map
    
    def clear_cache(self, dataset_name: Optional[str] = None):
        """
        Clear cached embeddings.
        
        Args:
            dataset_name: If provided, only clear cache for this dataset. Otherwise clear all.
        """
        if dataset_name:
            for data_type in ["corpus", "queries"]:
                cache_path = self._get_cache_path(dataset_name, data_type)
                if cache_path.exists():
                    cache_path.unlink()
                    print(f"Deleted cache: {cache_path.name}")
        else:
            for cache_file in self.cache_dir.glob("*.npz"):
                cache_file.unlink()
                print(f"Deleted cache: {cache_file.name}")
