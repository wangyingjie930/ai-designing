import argparse
import json
import math
import sys
from dataclasses import dataclass
from datetime import date, datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Sequence

import yaml

if __package__ is None or __package__ == "":
    sys.path.append(str(Path(__file__).resolve().parents[2]))

from memory.rag.config import load_pipeline_config
from memory.rag.ingest import configure_warnings
from memory.rag.manifest import load_manifest, write_yaml
from memory.rag.alias import switch_alias_to_collection
from memory.rag.pipeline import _build_embed_model


# IndexedChunk 表示当前 collection 里的一个可检索 chunk，携带向量、原文和过滤元数据。
@dataclass(frozen=True)
class IndexedChunk:
    node_id: str
    ref_doc_id: str
    text: str
    embedding: List[float]
    metadata: Dict[str, Any]


# GoldenQuestion 表示一条业务关键问题及其必须满足的召回/引用/过滤要求。
@dataclass(frozen=True)
class GoldenQuestion:
    id: str
    question: str
    actor_roles: List[str]
    expected_evidence_ids: List[str]
    forbidden_evidence_ids: List[str]
    required_phrases: List[str]
    expect_no_results: bool


# RetrievalHit 表示一次 golden 检索命中的候选证据。
@dataclass(frozen=True)
class RetrievalHit:
    chunk: IndexedChunk
    score: float


# 构建命令行参数；release 评估 manifest 对应 collection，通过后默认切换 alias。
def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Run golden-question gate and switch the RAG alias on pass.")
    parser.add_argument("--manifest", default="manifest.yaml", help="Path to current collection manifest YAML.")
    parser.add_argument("--golden", default="golden_questions.yaml", help="Path to golden questions YAML.")
    parser.add_argument("--env", default=None, help="Optional .env path; default is repo root .env.")
    parser.add_argument("--alias", default=None, help="Alias to switch on pass; default is <collection_prefix>_current.")
    parser.add_argument("--no-switch-alias", action="store_true", help="Only evaluate golden questions; do not switch alias.")
    parser.add_argument("--top-k", type=int, default=3, help="Number of filtered retrieval hits to inspect.")
    parser.add_argument("--as-of", default="2026-06-24", help="Date used to exclude expired documents.")
    return parser


# CLI 入口串起 collection 读取、golden gate 和通过后的 alias 切换。
def main() -> int:
    configure_warnings()
    args = build_parser().parse_args()
    rag_dir = Path(__file__).resolve().parent
    manifest_path = _resolve_rag_path(rag_dir, args.manifest)
    golden_path = _resolve_rag_path(rag_dir, args.golden)
    env_path = Path(args.env).resolve() if args.env else None
    try:
        manifest = load_manifest(manifest_path)
        config = load_pipeline_config(rag_dir, env_path, manifest.embedding_dim)
        collection_dir = config.output_dir / manifest.collection
        chunks = load_collection_chunks(collection_dir)
        golden_questions = load_golden_questions(golden_path)
        embed_model = _build_embed_model(config)
        report = evaluate_golden_questions(
            golden_questions=golden_questions,
            chunks=chunks,
            embed_model=embed_model,
            as_of=date.fromisoformat(args.as_of),
            top_k=args.top_k,
        )
        report_path = collection_dir / "golden-report.yaml"
        write_yaml(report_path, report)
        quality_gate = build_quality_gate_result(
            manifest=manifest,
            report=report,
            report_path=report_path,
            collection_dir=collection_dir,
        )
        quality_gate_path = collection_dir / "quality-gate.yaml"
        write_yaml(quality_gate_path, quality_gate)
        update_release_manifest_quality_gate(collection_dir / "release-manifest.yaml", quality_gate)
        alias_state = None
        if report["passed"] and not args.no_switch_alias:
            alias_state = switch_alias_to_collection(
                output_dir=config.output_dir,
                alias=args.alias or manifest.collection_alias,
                collection=manifest.collection,
                collection_dir=collection_dir,
                require_passed=True,
            )
        print("golden_set_passed: %s" % str(report["passed"]).lower())
        print("golden_report: %s" % report_path)
        print("quality_gate: %s" % quality_gate_path)
        print("quality_status: %s" % quality_gate["status"])
        if alias_state:
            print("alias_switched: %s" % alias_state["alias"])
            print("alias_collection: %s" % alias_state["collection"])
        return 0 if report["passed"] else 1
    except Exception as exc:
        print("rag release failed: %s" % exc, file=sys.stderr)
        return 1


# 解析相对于 memory/rag 的输入路径，方便在仓库根目录和子目录都能执行。
def _resolve_rag_path(rag_dir: Path, value: str) -> Path:
    path = Path(value)
    return path if path.is_absolute() else rag_dir / path


# 加载 golden questions，并归一化可选字段。
def load_golden_questions(path: Path) -> List[GoldenQuestion]:
    data = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    questions = []
    for item in data.get("golden_questions", []):
        questions.append(GoldenQuestion(
            id=str(item["id"]),
            question=str(item["question"]),
            actor_roles=[str(role) for role in item.get("actor_roles", [])],
            expected_evidence_ids=[str(evidence_id) for evidence_id in item.get("expected_evidence_ids", [])],
            forbidden_evidence_ids=[str(evidence_id) for evidence_id in item.get("forbidden_evidence_ids", [])],
            required_phrases=[str(phrase) for phrase in item.get("required_phrases", [])],
            expect_no_results=bool(item.get("expect_no_results", False)),
        ))
    if not questions:
        raise ValueError("golden questions are empty")
    return questions


# 从持久化 docstore/vector_store 还原当前 collection 的 chunk，确保引用能追到原文。
def load_collection_chunks(collection_dir: Path) -> List[IndexedChunk]:
    vector_path = collection_dir / "dense" / "default__vector_store.json"
    docstore_path = collection_dir / "docstore" / "docstore.json"
    if not vector_path.exists() or not docstore_path.exists():
        raise FileNotFoundError("collection index is missing vector_store or docstore under %s" % collection_dir)
    vector_data = json.loads(vector_path.read_text(encoding="utf-8"))
    docstore_data = json.loads(docstore_path.read_text(encoding="utf-8"))
    source_docs = docstore_data.get("docstore/data", {})
    chunks = []
    for node_id, embedding in vector_data.get("embedding_dict", {}).items():
        ref_doc_id = vector_data.get("text_id_to_ref_doc_id", {}).get(node_id)
        metadata = vector_data.get("metadata_dict", {}).get(node_id, {})
        source_doc = source_docs.get(ref_doc_id, {}).get("__data__", {})
        text = source_doc.get("text_resource", {}).get("text", "")
        chunks.append(IndexedChunk(
            node_id=node_id,
            ref_doc_id=ref_doc_id,
            text=text,
            embedding=[float(value) for value in embedding],
            metadata=metadata,
        ))
    if not chunks:
        raise ValueError("collection index has no chunks")
    return chunks


# 执行所有 golden questions，汇总 gate 结果。
def evaluate_golden_questions(
    golden_questions: Sequence[GoldenQuestion],
    chunks: Sequence[IndexedChunk],
    embed_model,
    as_of: date,
    top_k: int,
) -> Dict[str, Any]:
    results = []
    for question in golden_questions:
        results.append(evaluate_one_question(question, chunks, embed_model, as_of, top_k))
    return {
        "passed": all(result["passed"] for result in results),
        "as_of": as_of.isoformat(),
        "case_count": len(results),
        "passed_count": sum(1 for result in results if result["passed"]),
        "failed_count": sum(1 for result in results if not result["passed"]),
        "results": results,
    }


# 执行单条 golden question，并检查命中、引用、权限和过期过滤。
def evaluate_one_question(
    question: GoldenQuestion,
    chunks: Sequence[IndexedChunk],
    embed_model,
    as_of: date,
    top_k: int,
) -> Dict[str, Any]:
    query_embedding = embed_model.get_text_embedding(question.question)
    unfiltered_hits = rank_chunks(question.question, query_embedding, chunks)
    filtered_hits = [
        hit for hit in unfiltered_hits
        if role_allowed(question.actor_roles, hit.chunk.metadata) and not expired(as_of, hit.chunk.metadata)
    ][:top_k]
    filtered_evidence_ids = [evidence_id(hit.chunk) for hit in filtered_hits]
    unfiltered_evidence_ids = [evidence_id(hit.chunk) for hit in unfiltered_hits[:top_k]]
    expected_passed = all(evidence in filtered_evidence_ids for evidence in question.expected_evidence_ids)
    forbidden_passed = all(evidence not in filtered_evidence_ids for evidence in question.forbidden_evidence_ids)
    no_results_passed = (not filtered_hits) if question.expect_no_results else True
    citation_passed = citations_trace_to_source(question, filtered_hits)
    permission_passed = forbidden_passed and all(
        role_allowed(question.actor_roles, hit.chunk.metadata) for hit in filtered_hits
    )
    expiry_passed = all(not expired(as_of, hit.chunk.metadata) for hit in filtered_hits)
    passed = expected_passed and forbidden_passed and no_results_passed and citation_passed and permission_passed and expiry_passed
    return {
        "id": question.id,
        "passed": passed,
        "question": question.question,
        "actor_roles": question.actor_roles,
        "expected_evidence_ids": question.expected_evidence_ids,
        "forbidden_evidence_ids": question.forbidden_evidence_ids,
        "unfiltered_top_evidence_ids": unfiltered_evidence_ids,
        "filtered_top_evidence_ids": filtered_evidence_ids,
        "checks": {
            "expected_evidence_hit": expected_passed,
            "citation_traces_to_source": citation_passed,
            "permission_filter_passed": permission_passed,
            "expired_documents_excluded": expiry_passed,
            "forbidden_evidence_excluded": forbidden_passed,
            "expected_no_results": no_results_passed,
        },
        "citations": [build_citation(hit) for hit in filtered_hits],
    }


# 对当前 collection 的 chunk 做简单 hybrid ranking：dense cosine 加上词面重叠。
def rank_chunks(question: str, query_embedding: Sequence[float], chunks: Sequence[IndexedChunk]) -> List[RetrievalHit]:
    hits = []
    for chunk in chunks:
        dense_score = cosine_similarity(query_embedding, chunk.embedding)
        lexical_score = lexical_overlap(question, chunk.text)
        hits.append(RetrievalHit(chunk=chunk, score=dense_score + lexical_score))
    return sorted(hits, key=lambda hit: hit.score, reverse=True)


# 计算余弦相似度，模拟 dense retriever 的候选排序。
def cosine_similarity(left: Sequence[float], right: Sequence[float]) -> float:
    numerator = sum(a * b for a, b in zip(left, right))
    left_norm = math.sqrt(sum(a * a for a in left))
    right_norm = math.sqrt(sum(b * b for b in right))
    if left_norm == 0 or right_norm == 0:
        return 0.0
    return numerator / (left_norm * right_norm)


# 计算轻量词面重叠，模拟 BM25 对关键短语的补强效果。
def lexical_overlap(question: str, text: str) -> float:
    question_terms = set(normalize_terms(question))
    text_terms = set(normalize_terms(text))
    if not question_terms:
        return 0.0
    return len(question_terms & text_terms) / len(question_terms)


# 提取稳定词项，避免标点和大小写影响 golden 检查。
def normalize_terms(text: str) -> List[str]:
    return [token for token in "".join(ch.lower() if ch.isalnum() else " " for ch in text).split() if len(token) > 2]


# 检查当前 actor roles 是否能读取该 chunk。
def role_allowed(actor_roles: Sequence[str], metadata: Dict[str, Any]) -> bool:
    allowed_roles = set(metadata.get("allowed_roles") or [])
    return bool(allowed_roles & set(actor_roles))


# 检查文档是否相对评测日期过期。
def expired(as_of: date, metadata: Dict[str, Any]) -> bool:
    expires_at = metadata.get("expires_at")
    if metadata.get("document_status") == "expired":
        return True
    if not expires_at:
        return False
    return date.fromisoformat(str(expires_at)) < as_of


# 获取 chunk 的证据 ID。
def evidence_id(chunk: IndexedChunk) -> str:
    return str(chunk.metadata.get("evidence_id") or "")


# 检查 golden 里要求的引用短语是否能在候选原文中找到。
def citations_trace_to_source(question: GoldenQuestion, hits: Sequence[RetrievalHit]) -> bool:
    if question.expect_no_results:
        return True
    joined_text = "\n".join(hit.chunk.text for hit in hits)
    return all(phrase in joined_text for phrase in question.required_phrases)


# 构建报告中的引用片段，证明 evidence 可以追到原文。
def build_citation(hit: RetrievalHit) -> Dict[str, Any]:
    return {
        "evidence_id": evidence_id(hit.chunk),
        "ref_doc_id": hit.chunk.ref_doc_id,
        "source_file": hit.chunk.metadata.get("source_file"),
        "page_label": hit.chunk.metadata.get("page_label"),
        "score": round(hit.score, 6),
        "text_excerpt": hit.chunk.text[:240],
    }


# 根据 golden gate 结果生成 collection 的质量状态。
def build_quality_gate_result(
    manifest,
    report: Dict[str, Any],
    report_path: Path,
    collection_dir: Path,
) -> Dict[str, Any]:
    passed = bool(report["passed"])
    return {
        "status": "passed" if passed else "blocked_by_golden_questions",
        "collection": manifest.collection,
        "collection_alias": manifest.collection_alias,
        "collection_dir": str(collection_dir),
        "corpus_name": manifest.index_manifest.corpus_name,
        "golden_set_passed": passed,
        "golden_report": str(report_path),
        "evaluated_at": datetime.now(timezone.utc).isoformat(),
        "note": "通过后 release.py 默认将 alias 切到该 collection；失败时保持线上 alias 不变。",
    }


# 将 quality gate 结果写回 release-manifest，使 golden_set_passed 成为运行产物。
def update_release_manifest_quality_gate(release_manifest_path: Path, quality_gate: Dict[str, Any]) -> None:
    if not release_manifest_path.exists():
        raise FileNotFoundError("release manifest not found for collection: %s" % release_manifest_path)
    data = yaml.safe_load(release_manifest_path.read_text(encoding="utf-8")) or {}
    data["quality_gate"] = dict(quality_gate)
    write_yaml(release_manifest_path, data)


if __name__ == "__main__":
    raise SystemExit(main())
