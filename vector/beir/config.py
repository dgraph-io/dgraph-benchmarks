"""Configuration management for BEIR Dgraph benchmark."""

import os
from dataclasses import dataclass
from typing import List
from dotenv import load_dotenv

# Load environment variables
load_dotenv()


@dataclass
class DgraphConfig:
    """Dgraph connection configuration."""
    host: str = os.getenv("DGRAPH_HOST", "localhost")
    port: int = int(os.getenv("DGRAPH_PORT", "9080"))
    version: str = os.getenv("DGRAPH_VERSION", "latest")
    
    @property
    def grpc_endpoint(self) -> str:
        """Get gRPC endpoint."""
        return f"{self.host}:{self.port}"


@dataclass
class BEIRConfig:
    """BEIR dataset configuration."""
    dataset_name: str = os.getenv("DATASET_NAME", "scifact")
    embedding_model: str = os.getenv("EMBEDDING_MODEL", "all-MiniLM-L6-v2")
    batch_size: int = int(os.getenv("BATCH_SIZE", "32"))
    insert_batch_size: int = int(os.getenv("INSERT_BATCH_SIZE", "1000"))
    data_dir: str = "./data"


@dataclass
class HNSWConfig:
    """HNSW index configuration."""
    metric: str = os.getenv("HNSW_METRIC", "euclidean")
    max_levels: int = int(os.getenv("HNSW_MAX_LEVELS", "3"))
    ef_search: int = int(os.getenv("HNSW_EF_SEARCH", "40"))
    ef_construction: int = int(os.getenv("HNSW_EF_CONSTRUCTION", "100"))
    # Index type allows switching between standard hnsw and experimental variants
    # such as the new "partionedhnsw" (sic) index.
    index_type: str = os.getenv("HNSW_INDEX_TYPE", "hnsw")
    # Optional number of clusters for partitioned HNSW experimental variants. Interpreted as a
    # string in the index definition to match existing HNSW parameters. Only set
    # when HNSW_NUM_CLUSTERS is provided; otherwise default to 0 (disabled).
    num_clusters: int = int(os.getenv("HNSW_NUM_CLUSTERS")) if os.getenv("HNSW_NUM_CLUSTERS") is not None else 0
    # Optional vector dimension; when using partionedhnsw (sic) this should match the
    # length of the stored embeddings. Experimental, this is not yet released.
    vector_dim: int = 0
    
    def to_index_string(self) -> str:
        """Generate index string for Dgraph schema."""
        # Base index name (e.g., "hnsw" or "partionedhnsw")
        index_name = self.index_type

        # Common parameters shared by both standard and partitioned HNSW
        params = [
            f'metric:"{self.metric}"',
            f'maxLevels:"{self.max_levels}"',
            f'efSearch:"{self.ef_search}"',
            f'efConstruction:"{self.ef_construction}"',
        ]

        # Partitioned HNSW adds numClusters; we include it whenever explicitly
        # configured, regardless of the index name, to keep behavior simple.
        if self.num_clusters > 0:
            params.append(f'numClusters:"{self.num_clusters}"')

        # Some partitioned HNSW variants require the vector dimension to be
        # specified explicitly in the index configuration.
        if self.index_type == "partionedhnsw" and self.vector_dim > 0:
            params.append(f'vectorDimension:"{self.vector_dim}"')

        joined = ",".join(params)
        return f"{index_name}({joined})"


@dataclass
class SearchConfig:
    """Vector search configuration (for PR#9514 new similar_to syntax)."""
    ef_search_query: int = int(os.getenv("SEARCH_EF", "0"))  # 0 means use index default
    distance_threshold: float = float(os.getenv("SEARCH_DISTANCE_THRESHOLD", "0.0"))  # 0 means no threshold
    use_new_syntax: bool = os.getenv("USE_NEW_SIMILAR_TO_SYNTAX", "false").lower() == "true"


@dataclass
class EvaluationConfig:
    """Evaluation configuration."""
    k_values: List[int] = None
    description: str = os.getenv("DESC", "")
    
    def __post_init__(self):
        if self.k_values is None:
            k_values_str = os.getenv("K_VALUES", "1,3,5,10,100")
            self.k_values = [int(k) for k in k_values_str.split(",")]


@dataclass
class Config:
    """Main configuration class."""
    dgraph: DgraphConfig = None
    beir: BEIRConfig = None
    hnsw: HNSWConfig = None
    search: SearchConfig = None
    evaluation: EvaluationConfig = None
    
    def __post_init__(self):
        if self.dgraph is None:
            self.dgraph = DgraphConfig()
        if self.beir is None:
            self.beir = BEIRConfig()
        if self.hnsw is None:
            self.hnsw = HNSWConfig()
        if self.search is None:
            self.search = SearchConfig()
        if self.evaluation is None:
            self.evaluation = EvaluationConfig()


def get_config() -> Config:
    """Get the default configuration."""
    return Config()
