"""Dataset registry + download/materialization for the HNSW audit benchmark.

Following the beir suite's pattern, download URLs are hardcoded constants here
(the variable part — which dataset — is chosen by the DATASET env var). A user
never has to find a URL: pick a dataset name and the code knows where to get it.

Each dataset is an ANN-benchmarks-style HDF5 with a `train` matrix (the corpus)
and a `test` matrix (queries; unused here — the audit self-queries the corpus).
materialize() downloads it (cached) and writes the gzipped RDF the live loader
consumes. The index is applied later via `alter` (see run.sh), not here.

Add a dataset by adding one REGISTRY entry.
"""

import gzip
import os
import sys
import urllib.request

import h5py

from config import DATA_DIR, PRED, WORK_DIR

# name -> download URL. HDF5 with float32 `train` (corpus) and `test` (queries).
REGISTRY = {
    # 1M x 960-d GIST descriptors, euclidean. ~3.6 GB.
    "gist-960-euclidean": "http://ann-benchmarks.com/gist-960-euclidean.hdf5",
    # 1M x 128-d SIFT descriptors, euclidean. ~0.5 GB — a fast alternative.
    "sift-128-euclidean": "http://ann-benchmarks.com/sift-128-euclidean.hdf5",
}


def hdf5_path(dataset: str) -> str:
    return os.path.join(DATA_DIR, f"{dataset}.hdf5")


def download(dataset: str) -> str:
    """Download the dataset HDF5 into DATA_DIR (cached); return its path."""
    if dataset not in REGISTRY:
        raise SystemExit(f"unknown dataset {dataset!r}; known: {', '.join(REGISTRY)}")
    os.makedirs(DATA_DIR, exist_ok=True)
    path = hdf5_path(dataset)
    if os.path.exists(path) and os.path.getsize(path) > 0:
        print(f"[data] {dataset} already present: {path}", flush=True)
        return path
    url = REGISTRY[dataset]
    tmp = path + ".part"
    print(f"[data] downloading {dataset} from {url}", flush=True)

    def _progress(blocks, block_size, total):
        if total > 0 and blocks % 500 == 0:
            pct = min(100, blocks * block_size * 100 // total)
            print(f"  {pct}%", end="\r", flush=True)

    urllib.request.urlretrieve(url, tmp, _progress)
    os.replace(tmp, path)
    print(f"\n[data] saved {path}", flush=True)
    return path


def materialize(dataset: str, n: int) -> None:
    """Write work/data.rdf.gz for a live load of N vectors (uid = row index + 1)."""
    path = download(dataset)
    os.makedirs(WORK_DIR, exist_ok=True)
    rdf_path = os.path.join(WORK_DIR, "data.rdf.gz")

    with h5py.File(path, "r") as f:
        train = f["train"]
        dim = train.shape[1]
        n = min(n, train.shape[0])
        print(f"[data] materializing {n} x {dim}-d vectors -> {rdf_path}", flush=True)
        batch = 2000
        with gzip.open(rdf_path, "wt") as out:
            for i in range(0, n, batch):
                chunk = train[i:min(i + batch, n)]
                lines = []
                for j, vec in enumerate(chunk):
                    v = "[" + ",".join(f"{x:.6f}" for x in vec) + "]"
                    # uid = row index + 1 (the audit maps uids back the same way).
                    lines.append(f'<{hex(i + j + 1)}> <{PRED}> "{v}" .')
                out.write("\n".join(lines) + "\n")
    print(f"[data] wrote RDF ({n} vectors)", flush=True)


if __name__ == "__main__":
    # Called by run.sh: `python datasets.py <dataset> <n>`.
    from config import config
    ds = sys.argv[1] if len(sys.argv) > 1 else config.dataset
    count = int(sys.argv[2]) if len(sys.argv) > 2 else config.n
    materialize(ds, count)
