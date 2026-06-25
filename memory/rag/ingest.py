import argparse
import sys
from pathlib import Path

if __package__ is None or __package__ == "":
    sys.path.append(str(Path(__file__).resolve().parents[2]))

from memory.rag.config import load_pipeline_config
from memory.rag.manifest import load_manifest
from memory.rag.pipeline import run_ingestion


# 屏蔽本机 Python 3.9 + LlamaIndex 依赖组合下的已知 warning，避免淹没执行结果。
def configure_warnings() -> None:
    import logging
    import warnings

    warnings.filterwarnings("ignore", message="urllib3 v2 only supports OpenSSL.*")
    warnings.filterwarnings("ignore", message="The 'validate_default' attribute.*")
    warnings.filterwarnings("ignore", message="The tokenizer parameter is deprecated.*")
    logging.getLogger("llama_index.retrievers.bm25.base").setLevel(logging.ERROR)


# 构建命令行参数，支持在 memory/rag 目录下直接执行。
def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Build payroll RAG dense/BM25 indexes from a manifest.")
    parser.add_argument("--manifest", default="manifest.yaml", help="Path to ingestion manifest YAML.")
    parser.add_argument("--env", default=None, help="Optional .env path; default is repo root .env.")
    parser.add_argument("--dry-run", action="store_true", help="Validate manifest/config without building indexes.")
    return parser


# CLI 入口负责串起 manifest、.env 和 pipeline，不承载具体 ingest 细节。
def main() -> int:
    configure_warnings()
    args = build_parser().parse_args()
    rag_dir = Path(__file__).resolve().parent
    manifest_path = Path(args.manifest)
    if not manifest_path.is_absolute():
        manifest_path = rag_dir / manifest_path
    env_path = Path(args.env).resolve() if args.env else None

    try:
        manifest = load_manifest(manifest_path)
        config = load_pipeline_config(
            rag_dir=rag_dir,
            env_file=env_path,
            manifest_embedding_dim=manifest.embedding_dim,
        )
        result = run_ingestion(
            manifest=manifest,
            config=config,
            dry_run=args.dry_run,
        )
        _print_result(result, config.embedder.model, config.embedder.dim)
        return 0
    except Exception as exc:
        print("rag ingestion failed: %s" % exc, file=sys.stderr)
        return 1


# 输出本次执行的关键路径和统计，不打印任何密钥。
def _print_result(result, embedding_model: str, embedding_dim: int) -> None:
    mode = "dry-run" if result.dry_run else "built"
    print("rag ingestion %s" % mode)
    print("raw_docs:")
    for path in result.raw_doc_paths:
        print("  - %s" % path)
    print("collection_dir: %s" % result.collection_dir)
    print("dense_index_dir: %s" % result.dense_index_dir)
    print("bm25_index_dir: %s" % result.bm25_index_dir)
    print("docstore: %s" % result.docstore_path)
    print("docstore_hashes: %s" % result.docstore_hashes_path)
    print("vector_store: %s" % result.vector_store_path)
    print("collection_state: %s" % result.collection_state_path)
    print("release_manifest: %s" % result.release_manifest_path)
    print("corpus_revision: %s" % result.corpus_revision)
    print("embedding: %s dim=%s" % (embedding_model, embedding_dim))
    if not result.dry_run:
        print("actual_source_count: %s" % result.source_count)
        print("actual_chunk_count: %s" % result.chunk_count)
        print("processed_source_count: %s" % result.processed_source_count)
        print("processed_chunk_count: %s" % result.processed_chunk_count)
        if result.changed_document_ids:
            print("changed_document_ids:")
            for doc_id in result.changed_document_ids:
                print("  - %s" % doc_id)
        else:
            print("changed_document_ids: []")


if __name__ == "__main__":
    raise SystemExit(main())
