import argparse
import sys
from datetime import date
from pathlib import Path
from typing import Any, Dict

import yaml

if __package__ is None or __package__ == "":
    sys.path.append(str(Path(__file__).resolve().parents[2]))

from memory.rag.config import load_pipeline_config, resolve_output_dir
from memory.rag.ingest import configure_warnings
from memory.rag.alias import resolve_alias
from memory.rag.pipeline import _build_embed_model
from memory.rag.release import build_citation, expired, load_collection_chunks, rank_chunks, role_allowed


# 构建业务侧查询参数；默认通过 alias 找到当前线上 collection。
def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Query the current RAG collection through a stable alias.")
    parser.add_argument("--alias", default="payroll_rag_current", help="Stable alias used by application code.")
    parser.add_argument("--collection", default=None, help="Optional concrete collection for debugging; bypasses alias.")
    parser.add_argument("--question", required=True, help="Business question to retrieve evidence for.")
    parser.add_argument("--role", action="append", default=[], help="Actor role used for permission filtering.")
    parser.add_argument("--top-k", type=int, default=3)
    parser.add_argument("--as-of", default="2026-06-24")
    parser.add_argument("--env", default=None)
    return parser


# CLI 入口模拟业务代码：先解析 alias，再按真实 collection 做检索。
def main() -> int:
    configure_warnings()
    args = build_parser().parse_args()
    env_path = Path(args.env).resolve() if args.env else None
    try:
        output_dir = resolve_output_dir(env_path)
        if args.collection:
            collection = args.collection
            collection_dir = output_dir / args.collection
            alias = None
        else:
            resolved = resolve_alias(output_dir, args.alias)
            collection = resolved.collection
            collection_dir = resolved.collection_dir
            alias = resolved.alias
        embedding_dim = load_embedding_dim(collection_dir)
        config = load_pipeline_config(Path(__file__).resolve().parent, env_path, embedding_dim)
        embed_model = _build_embed_model(config)
        chunks = load_collection_chunks(collection_dir)
        query_embedding = embed_model.get_text_embedding(args.question)
        roles = args.role or ["employee"]
        as_of = date.fromisoformat(args.as_of)
        hits = [
            hit for hit in rank_chunks(args.question, query_embedding, chunks)
            if role_allowed(roles, hit.chunk.metadata) and not expired(as_of, hit.chunk.metadata)
        ][:args.top_k]
        result = {
            "alias": alias,
            "collection": collection,
            "collection_dir": str(collection_dir),
            "question": args.question,
            "actor_roles": roles,
            "citations": [build_citation(hit) for hit in hits],
        }
        print(yaml.safe_dump(result, allow_unicode=True, sort_keys=False).rstrip())
        return 0
    except Exception as exc:
        print("rag app query failed: %s" % exc, file=sys.stderr)
        return 1


# 从当前 collection 的 release manifest 读取 embedding 维度，避免业务查询硬编码模型参数。
def load_embedding_dim(collection_dir: Path) -> int:
    manifest_path = collection_dir / "release-manifest.yaml"
    if not manifest_path.exists():
        raise FileNotFoundError("release manifest not found for collection: %s" % collection_dir)
    data: Dict[str, Any] = yaml.safe_load(manifest_path.read_text(encoding="utf-8")) or {}
    embedding_dim = (
        data.get("build", {}).get("actual_embedding_dim")
        or data.get("embedding", {}).get("params", {}).get("dim")
    )
    if not embedding_dim:
        raise ValueError("release manifest missing embedding_dim: %s" % manifest_path)
    return int(embedding_dim)


if __name__ == "__main__":
    raise SystemExit(main())
