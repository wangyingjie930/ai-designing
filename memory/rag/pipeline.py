import re
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional, Sequence

from .config import PipelineConfig, is_gemini_embedding
from .manifest import (
    RAGManifest,
    build_corpus_revision,
    build_raw_doc_fingerprints,
    build_release_manifest,
    resolve_raw_doc_paths,
    write_yaml,
)


CHINESE_OR_WORD = re.compile(r"[\u4e00-\u9fff]|[A-Za-z0-9_]+")
VECTOR_STORE_FILENAME = "default__vector_store.json"
DOCSTORE_FILENAME = "docstore.json"
CHUNKER_SECONDARY_REGEX = r"[^。！？；;.!?\n]+[。！？；;.!?]?|[\n]+"
SUPPORTED_HYBRID_INDEX_VERSION = "bm25-cn-v1+dense-v3"
PAGE_POLICY_METADATA = {
    "1": {
        "evidence_id": "POLICY-2026-ELIGIBILITY",
        "allowed_roles": ["employee", "hrbp", "finance", "payroll"],
        "expires_at": "2026-12-31",
        "document_status": "active",
    },
    "2": {
        "evidence_id": "POLICY-2026-EXCEPTION-APPROVAL",
        "allowed_roles": ["hrbp", "finance"],
        "expires_at": "2026-12-31",
        "document_status": "active",
    },
    "3": {
        "evidence_id": "POLICY-2026-DEFERRED-PAYOUT",
        "allowed_roles": ["hrbp", "payroll"],
        "expires_at": "2026-12-31",
        "document_status": "active",
    },
    "4": {
        "evidence_id": "POLICY-2026-RELEASE-GATE",
        "allowed_roles": ["release_manager"],
        "expires_at": "2026-12-31",
        "document_status": "active",
    },
    "5": {
        "evidence_id": "POLICY-2025-EXPIRED-AUTO-BONUS",
        "allowed_roles": ["hrbp", "finance", "payroll"],
        "expires_at": "2025-12-31",
        "document_status": "expired",
    },
}


# 表示一次 ingestion 运行后的核心产物路径和统计。
@dataclass(frozen=True)
class IngestionResult:
    raw_doc_paths: List[Path]
    collection_dir: Path
    dense_index_dir: Path
    bm25_index_dir: Path
    docstore_path: Path
    docstore_hashes_path: Path
    vector_store_path: Path
    collection_state_path: Path
    release_manifest_path: Path
    corpus_revision: str
    source_count: int
    chunk_count: int
    processed_source_count: int
    processed_chunk_count: int
    changed_document_ids: List[str]
    dry_run: bool


# 运行 ingestion 主流程；dry-run 只校验配置和文档路径，不调用 LlamaIndex。
def run_ingestion(
    manifest: RAGManifest,
    config: PipelineConfig,
    dry_run: bool = False,
) -> IngestionResult:
    _validate_preflight_manifest(manifest, config)
    raw_doc_paths = resolve_raw_doc_paths(manifest)
    raw_doc_fingerprints = build_raw_doc_fingerprints(raw_doc_paths)
    corpus_revision = build_corpus_revision(manifest, raw_doc_fingerprints)
    collection_dir = config.output_dir / manifest.collection
    dense_index_dir = collection_dir / "dense"
    bm25_index_dir = collection_dir / "bm25"
    docstore_path = collection_dir / "docstore" / DOCSTORE_FILENAME
    docstore_hashes_path = collection_dir / "docstore" / "document-hashes.yaml"
    vector_store_path = dense_index_dir / VECTOR_STORE_FILENAME
    collection_state_path = collection_dir / "collection-state.yaml"
    release_manifest_path = collection_dir / "release-manifest.yaml"

    if dry_run:
        return IngestionResult(
            raw_doc_paths=raw_doc_paths,
            collection_dir=collection_dir,
            dense_index_dir=dense_index_dir,
            bm25_index_dir=bm25_index_dir,
            docstore_path=docstore_path,
            docstore_hashes_path=docstore_hashes_path,
            vector_store_path=vector_store_path,
            collection_state_path=collection_state_path,
            release_manifest_path=release_manifest_path,
            corpus_revision=corpus_revision,
            source_count=0,
            chunk_count=0,
            processed_source_count=0,
            processed_chunk_count=0,
            changed_document_ids=[],
            dry_run=True,
        )

    embed_model = _build_embed_model(config)
    _probe_embedding_service(embed_model, manifest.embedding_dim, config.embedder.model)
    documents = _load_documents(raw_doc_paths, manifest)
    docstore = _load_docstore(docstore_path)
    vector_store = _load_vector_store(vector_store_path, reuse_existing=docstore_path.exists())
    changed_document_ids = _find_changed_document_ids(docstore, documents)
    nodes = _run_ingestion_pipeline(documents, manifest, config, embed_model, docstore, vector_store)
    vector_count, actual_embedding_dim = _validate_vector_store_embeddings(vector_store, manifest.embedding_dim)
    _persist_incremental_stores(docstore, vector_store, docstore_path, vector_store_path)
    _persist_document_hash_snapshot(docstore, documents, docstore_hashes_path, changed_document_ids, manifest)
    bm25_nodes = _split_documents_for_sparse_index(documents, manifest, config)
    _persist_bm25_index(bm25_nodes, bm25_index_dir, manifest)
    _write_collection_state(
        manifest=manifest,
        collection_state_path=collection_state_path,
        release_manifest_path=release_manifest_path,
        corpus_revision=corpus_revision,
        actual_source_count=len(documents),
        actual_chunk_count=vector_count,
        processed_source_count=len(changed_document_ids),
        processed_chunk_count=len(nodes),
    )
    release_manifest = build_release_manifest(
        manifest=manifest,
        raw_doc_paths=raw_doc_paths,
        raw_doc_fingerprints=raw_doc_fingerprints,
        corpus_revision=corpus_revision,
        dense_index_dir=dense_index_dir,
        bm25_index_dir=bm25_index_dir,
        docstore_path=docstore_path,
        docstore_hashes_path=docstore_hashes_path,
        vector_store_path=vector_store_path,
        collection_state_path=collection_state_path,
        actual_source_count=len(documents),
        actual_chunk_count=vector_count,
        processed_source_count=len(changed_document_ids),
        processed_chunk_count=len(nodes),
        changed_document_ids=changed_document_ids,
        actual_embedding_model=config.embedder.model,
        actual_embedding_dim=actual_embedding_dim,
        built_at=datetime.now(timezone.utc).isoformat(),
    )
    write_yaml(release_manifest_path, release_manifest)
    return IngestionResult(
        raw_doc_paths=raw_doc_paths,
        collection_dir=collection_dir,
        dense_index_dir=dense_index_dir,
        bm25_index_dir=bm25_index_dir,
        docstore_path=docstore_path,
        docstore_hashes_path=docstore_hashes_path,
        vector_store_path=vector_store_path,
        collection_state_path=collection_state_path,
        release_manifest_path=release_manifest_path,
        corpus_revision=corpus_revision,
        source_count=len(documents),
        chunk_count=vector_count,
        processed_source_count=len(changed_document_ids),
        processed_chunk_count=len(nodes),
        changed_document_ids=changed_document_ids,
        dry_run=False,
    )


# 在执行前校验 manifest 每个控制字段，避免 silent fallback 生成错误索引。
def _validate_preflight_manifest(manifest: RAGManifest, config: PipelineConfig) -> None:
    expected_pairs = [
        ("embedding.params.model", manifest.embedding_model, config.embedder.model),
        ("hybrid_index.version", manifest.hybrid_index_version, SUPPORTED_HYBRID_INDEX_VERSION),
    ]
    mismatches = [
        "%s=%s expected %s" % (name, actual, expected)
        for name, actual, expected in expected_pairs
        if actual != expected
    ]
    if manifest.embedding_dim != config.embedder.dim:
        mismatches.append("embedding.params.dim=%s expected %s" % (manifest.embedding_dim, config.embedder.dim))
    _validate_parser_params(mismatches, manifest.parser.params)
    _validate_chunker_params(mismatches, manifest.chunker.params)
    _validate_embedding_params(mismatches, manifest.embedding.params, config)
    _validate_hybrid_index_params(mismatches, manifest.hybrid_index.params, config)
    if mismatches:
        raise ValueError("manifest preflight failed: " + "; ".join(mismatches))


# 校验 PDF 解析能力边界；version/params 变化用于生成新 collection，但不能声明未实现能力。
def _validate_parser_params(mismatches: List[str], params: Dict[str, Any]) -> None:
    _expect_param(mismatches, params, "engine", "llamaindex-pdfreader", "parser.params")
    _expect_bool_param(mismatches, params, "return_full_document", "parser.params")
    _expect_param(mismatches, params, "ocr_enabled", False, "parser.params")
    _expect_param(mismatches, params, "table_mode", "plain_text", "parser.params")
    _expect_bool_param(mismatches, params, "page_split", "parser.params")


# 校验切块参数的可执行性；具体数值来自 manifest，变化后由 collection 指纹隔离。
def _validate_chunker_params(mismatches: List[str], params: Dict[str, Any]) -> None:
    _expect_param(mismatches, params, "splitter", "llamaindex-sentence-splitter", "chunker.params")
    chunk_size = _expect_positive_int_param(mismatches, params, "chunk_size", "chunker.params")
    chunk_overlap = _expect_non_negative_int_param(mismatches, params, "chunk_overlap", "chunker.params")
    if chunk_size is not None and chunk_overlap is not None and chunk_overlap >= chunk_size:
        mismatches.append("chunker.params.chunk_overlap=%s must be smaller than chunk_size=%s" % (chunk_overlap, chunk_size))
    _expect_non_empty_str_param(mismatches, params, "separator", "chunker.params")
    _expect_regex_param(mismatches, params, "secondary_chunking_regex", "chunker.params")
    _expect_bool_param(mismatches, params, "include_metadata", "chunker.params")
    _expect_bool_param(mismatches, params, "include_prev_next_rel", "chunker.params")


# 校验 embedding 参数，保证向量维度和批处理等行为可以从 manifest 复现。
def _validate_embedding_params(mismatches: List[str], params: Dict[str, Any], config: PipelineConfig) -> None:
    _expect_param(mismatches, params, "model", config.embedder.model, "embedding.params")
    _expect_param(mismatches, params, "dim", config.embedder.dim, "embedding.params")
    _expect_param(mismatches, params, "output_dimensionality", config.embedder.dim, "embedding.params")
    _expect_param(mismatches, params, "embed_batch_size", config.embedder.batch_size, "embedding.params")
    _expect_param(mismatches, params, "timeout_seconds", config.embedder.timeout_seconds, "embedding.params")
    _expect_param(mismatches, params, "max_retries", config.embedder.max_retries, "embedding.params")


# 校验 hybrid index 参数，保证 dense/sparse 构建和排序契约与版本名绑定。
def _validate_hybrid_index_params(mismatches: List[str], params: Dict[str, Any], config: PipelineConfig) -> None:
    dense_params = _nested_params(mismatches, params, "dense", "hybrid_index.params")
    sparse_params = _nested_params(mismatches, params, "sparse", "hybrid_index.params")
    ranking_params = _nested_params(mismatches, params, "ranking", "hybrid_index.params")
    metadata_filters = _nested_params(mismatches, ranking_params, "metadata_filters", "hybrid_index.params.ranking")

    _expect_param(mismatches, dense_params, "engine", "llamaindex-simple-vector-store", "hybrid_index.params.dense")
    _expect_param(mismatches, dense_params, "vector_store_file", VECTOR_STORE_FILENAME, "hybrid_index.params.dense")
    _expect_param(mismatches, dense_params, "similarity", "cosine", "hybrid_index.params.dense")
    _expect_param(mismatches, sparse_params, "engine", "llamaindex-bm25-retriever", "hybrid_index.params.sparse")
    _expect_param(mismatches, sparse_params, "tokenizer", "chinese-char-and-word", "hybrid_index.params.sparse")
    _expect_param(mismatches, sparse_params, "language", config.bm25_language, "hybrid_index.params.sparse")
    _expect_param(mismatches, sparse_params, "skip_stemming", True, "hybrid_index.params.sparse")
    _expect_param(mismatches, sparse_params, "similarity_top_k", config.bm25_top_k, "hybrid_index.params.sparse")
    _expect_param(
        mismatches,
        ranking_params,
        "method",
        "dense_cosine_plus_lexical_overlap",
        "hybrid_index.params.ranking",
    )
    _expect_param(mismatches, ranking_params, "dense_weight", 1.0, "hybrid_index.params.ranking")
    _expect_param(mismatches, ranking_params, "lexical_weight", 1.0, "hybrid_index.params.ranking")
    _expect_param(mismatches, metadata_filters, "role_allowed", True, "hybrid_index.params.ranking.metadata_filters")
    _expect_param(mismatches, metadata_filters, "exclude_expired", True, "hybrid_index.params.ranking.metadata_filters")


# 读取嵌套参数对象，缺失时把错误并入 preflight mismatch 列表。
def _nested_params(mismatches: List[str], params: Dict[str, Any], key: str, path: str) -> Dict[str, Any]:
    value = params.get(key)
    if not isinstance(value, dict):
        mismatches.append("%s.%s must be a mapping" % (path, key))
        return {}
    return value


# 比对 manifest 参数和当前实现/环境期望值。
def _expect_param(mismatches: List[str], params: Dict[str, Any], key: str, expected: Any, path: str) -> None:
    actual = params.get(key)
    if actual != expected:
        mismatches.append("%s.%s=%s expected %s" % (path, key, actual, expected))


# 读取正整数参数，避免切块参数以字符串或非法范围进入 SentenceSplitter。
def _expect_positive_int_param(mismatches: List[str], params: Dict[str, Any], key: str, path: str) -> Optional[int]:
    value = params.get(key)
    if not isinstance(value, int) or isinstance(value, bool) or value <= 0:
        mismatches.append("%s.%s=%s must be a positive integer" % (path, key, value))
        return None
    return value


# 读取非负整数参数，chunk_overlap 可以为 0，但不能为负数。
def _expect_non_negative_int_param(mismatches: List[str], params: Dict[str, Any], key: str, path: str) -> Optional[int]:
    value = params.get(key)
    if not isinstance(value, int) or isinstance(value, bool) or value < 0:
        mismatches.append("%s.%s=%s must be a non-negative integer" % (path, key, value))
        return None
    return value


# 校验布尔参数，防止 YAML 字符串 "true"/"false" 被误当成布尔配置。
def _expect_bool_param(mismatches: List[str], params: Dict[str, Any], key: str, path: str) -> None:
    value = params.get(key)
    if not isinstance(value, bool):
        mismatches.append("%s.%s=%s must be a boolean" % (path, key, value))


# 校验非空字符串参数，避免底层 splitter 收到不可用分隔符。
def _expect_non_empty_str_param(mismatches: List[str], params: Dict[str, Any], key: str, path: str) -> None:
    value = params.get(key)
    if not isinstance(value, str) or value == "":
        mismatches.append("%s.%s=%s must be a non-empty string" % (path, key, value))


# 校验正则参数可以编译，具体切分规则由 manifest 契约控制。
def _expect_regex_param(mismatches: List[str], params: Dict[str, Any], key: str, path: str) -> None:
    value = params.get(key)
    if not isinstance(value, str) or value == "":
        mismatches.append("%s.%s=%s must be a non-empty regex string" % (path, key, value))
        return
    try:
        re.compile(value)
    except re.error as exc:
        mismatches.append("%s.%s has invalid regex: %s" % (path, key, exc))


# 使用 LlamaIndex PDFReader 读取原始文档，并把当前索引元数据写进 Document metadata。
def _load_documents(raw_doc_paths: Sequence[Path], manifest: RAGManifest):
    from llama_index.core import SimpleDirectoryReader
    from llama_index.readers.file import PDFReader

    parser_params = manifest.parser.params
    reader = SimpleDirectoryReader(
        input_files=[str(path) for path in raw_doc_paths],
        file_extractor={".pdf": PDFReader(return_full_document=parser_params["return_full_document"])},
        file_metadata=lambda filename: _file_metadata(filename, manifest),
        raise_on_error=True,
    )
    documents = reader.load_data()
    _assign_stable_document_ids(documents, manifest)
    return documents


# 为每个解析后的源文档生成稳定 doc_id，保证 docstore 可以跨运行比较 document_hash。
def _assign_stable_document_ids(documents, manifest: RAGManifest) -> None:
    for index, document in enumerate(documents):
        source_file = document.metadata.get("source_file") or document.metadata.get("file_name") or "unknown"
        page_label = document.metadata.get("page_label") or str(index + 1)
        document.id_ = "%s::%s::page-%s" % (
            manifest.collection,
            source_file,
            page_label,
        )
        document.metadata.update(PAGE_POLICY_METADATA.get(str(page_label), {}))


# 生成每个源文件共享的元数据，后续 chunk 和检索结果可以追溯到当前索引契约。
def _file_metadata(filename: str, manifest: RAGManifest) -> dict:
    return {
        "source_file": Path(filename).name,
        "corpus_name": manifest.index_manifest.corpus_name,
        "parser_version": manifest.parser_version,
        "collection": manifest.collection,
    }


# 执行 LlamaIndex IngestionPipeline，通过 docstore hash 跳过未变化文档。
def _run_ingestion_pipeline(documents, manifest: RAGManifest, config: PipelineConfig, embed_model, docstore, vector_store):
    from llama_index.core.ingestion import IngestionPipeline
    from llama_index.core.ingestion.pipeline import DocstoreStrategy

    pipeline = IngestionPipeline(
        name=manifest.collection,
        project_name=manifest.index_manifest.corpus_name,
        transformations=[_build_splitter(manifest), _build_metadata_annotator(manifest, config), embed_model],
        docstore=docstore,
        vector_store=vector_store,
        docstore_strategy=DocstoreStrategy.UPSERTS_AND_DELETE,
        disable_cache=True,
    )
    nodes = pipeline.run(documents=list(documents), show_progress=True)
    return nodes


# 构造 clause-aware splitter，dense 和 BM25 复用同一套 chunk 边界。
def _build_splitter(manifest: RAGManifest):
    from llama_index.core.node_parser import SentenceSplitter

    chunker_params = manifest.chunker.params
    return SentenceSplitter(
        chunk_size=chunker_params["chunk_size"],
        chunk_overlap=chunker_params["chunk_overlap"],
        separator=chunker_params["separator"],
        secondary_chunking_regex=chunker_params["secondary_chunking_regex"],
        include_metadata=chunker_params["include_metadata"],
        include_prev_next_rel=chunker_params["include_prev_next_rel"],
    )


# 构建元数据 transformation，确保 manifest 信息在 embedding 前进入节点。
def _build_metadata_annotator(manifest: RAGManifest, config: PipelineConfig):
    from typing import Any

    from llama_index.core.schema import BaseNode, TransformComponent
    from pydantic import Field

    # ManifestMetadataAnnotator 在 IngestionPipeline 内部给 chunk 写入索引版本元数据。
    class ManifestMetadataAnnotator(TransformComponent):
        chunker_version: str = Field()
        hybrid_index_version: str = Field()
        embedding_model: str = Field()
        embedding_dim: int = Field()

        # 在 embedding 前补齐元数据，保证 vector_store 持久化的 metadata 也包含 manifest 版本。
        def __call__(self, nodes: Sequence[BaseNode], **kwargs: Any) -> Sequence[BaseNode]:
            for node in nodes:
                node.metadata["chunker_version"] = self.chunker_version
                node.metadata["hybrid_index_version"] = self.hybrid_index_version
                node.metadata["embedding_model"] = self.embedding_model
                node.metadata["embedding_dim"] = self.embedding_dim
            return nodes

    return ManifestMetadataAnnotator(
        chunker_version=manifest.chunker_version,
        hybrid_index_version=manifest.hybrid_index_version,
        embedding_model=config.embedder.model,
        embedding_dim=manifest.embedding_dim,
    )


# 为 sparse index 重新切出完整 chunk 集合；这里不做 embedding，不破坏 docstore 增量语义。
def _split_documents_for_sparse_index(documents, manifest: RAGManifest, config: PipelineConfig):
    nodes = _build_splitter(manifest).get_nodes_from_documents(documents)
    return _build_metadata_annotator(manifest, config)(nodes)


# 加载持久化 docstore；不存在时创建新 docstore 作为本 collection 的增量基线。
def _load_docstore(docstore_path: Path):
    from llama_index.core.storage.docstore import SimpleDocumentStore

    if docstore_path.exists():
        return SimpleDocumentStore.from_persist_path(str(docstore_path))
    return SimpleDocumentStore()


# 加载 dense vector store；没有 docstore 基线时不复用旧向量，避免旧随机 doc_id 污染。
def _load_vector_store(vector_store_path: Path, reuse_existing: bool):
    from llama_index.core.vector_stores.simple import SimpleVectorStore

    if reuse_existing and vector_store_path.exists():
        return SimpleVectorStore.from_persist_path(str(vector_store_path))
    return SimpleVectorStore()


# 预先计算哪些源文档会被 LlamaIndex docstore 判定为新增或变更。
def _find_changed_document_ids(docstore, documents) -> List[str]:
    changed_document_ids = []
    for document in documents:
        existing_hash = docstore.get_document_hash(document.id_)
        if existing_hash != document.hash:
            changed_document_ids.append(document.id_)
    return changed_document_ids


# 校验 dense vector store 的总向量数量和维度，支持第二次运行时 nodes=0 的跳过场景。
def _validate_vector_store_embeddings(vector_store, expected_dim: int) -> tuple[int, int]:
    embeddings = vector_store.data.embedding_dict
    if not embeddings:
        raise ValueError("embedding validation failed: vector store has no embeddings")
    dims = sorted({len(vector) for vector in embeddings.values()})
    if dims != [expected_dim]:
        raise ValueError("embedding validation failed: vector dims=%s expected_dim=%d" % (dims, expected_dim))
    return len(embeddings), dims[0]


# 持久化 docstore 和 vector_store；docstore 保存 doc_id -> document_hash 增量基线。
def _persist_incremental_stores(docstore, vector_store, docstore_path: Path, vector_store_path: Path) -> None:
    docstore_path.parent.mkdir(parents=True, exist_ok=True)
    vector_store_path.parent.mkdir(parents=True, exist_ok=True)
    docstore.persist(str(docstore_path))
    vector_store.persist(str(vector_store_path))


# 将 LlamaIndex docstore 的 doc_id -> document_hash 基线导出成可审阅快照。
def _persist_document_hash_snapshot(
    docstore,
    documents,
    docstore_hashes_path: Path,
    changed_document_ids: Sequence[str],
    manifest: RAGManifest,
) -> None:
    changed_ids = set(changed_document_ids)
    write_yaml(docstore_hashes_path, {
        "collection": manifest.collection,
        "corpus_name": manifest.index_manifest.corpus_name,
        "docstore_strategy": "DocstoreStrategy.UPSERTS_AND_DELETE",
        "hash_mapping": "doc_id -> document_hash",
        "llamaindex_persisted_field": "docstore/metadata.<doc_id>.doc_hash",
        "documents": [
            {
                "doc_id": document.id_,
                "document_hash": docstore.get_document_hash(document.id_),
                "changed_in_this_run": document.id_ in changed_ids,
            }
            for document in documents
        ],
    })


# 根据 .env 配置生成真实 embedding 模型；自定义模型要走 model_name 才不会被枚举校验拦住。
def _build_embed_model(config: PipelineConfig):
    if not config.embedder.api_key:
        raise ValueError("missing embedding api key: set RAG_EMBEDDING_API_KEY, EMBEDDING_API_KEY, or OPENAI_API_KEY")
    if not config.embedder.base_url:
        raise ValueError("missing embedding base url: set RAG_EMBEDDING_BASE_URL, EMBEDDING_BASE_URL, or LLM_OPENAI_BASE_URL")
    if is_gemini_embedding(config.embedder.model, config.embedder.base_url, config.embedder.endpoint_path):
        from .embeddings import GeminiEmbedding

        return GeminiEmbedding(
            model_name=config.embedder.model,
            api_key=config.embedder.api_key,
            base_url=config.embedder.base_url or "",
            endpoint_path=config.embedder.endpoint_path,
            output_dimensionality=config.embedder.dim,
            embed_batch_size=config.embedder.batch_size,
            timeout_seconds=config.embedder.timeout_seconds,
            max_retries=config.embedder.max_retries,
        )

    from llama_index.embeddings.openai import OpenAIEmbedding

    return OpenAIEmbedding(
        model_name=config.embedder.model,
        dimensions=config.embedder.dim,
        api_key=config.embedder.api_key,
        api_base=config.embedder.base_url,
        embed_batch_size=config.embedder.batch_size,
        timeout=config.embedder.timeout_seconds,
        max_retries=config.embedder.max_retries,
    )


# 用真实 embedding 服务做一次轻量预检，提前确认 .env 模型可用于 embeddings。
def _probe_embedding_service(embed_model, expected_dim: int, model_name: str) -> None:
    try:
        vector = embed_model.get_text_embedding("RAG ingestion embedding preflight")
    except Exception as exc:
        raise RuntimeError("embedding preflight failed for model %s: %s" % (model_name, exc)) from exc
    if len(vector) != expected_dim:
        raise ValueError("embedding preflight dim mismatch for model %s: actual=%d expected=%d" % (
            model_name,
            len(vector),
            expected_dim,
        ))


# 构建并持久化 BM25 index；参数来自 hybrid_index.params.sparse，匹配当前版本契约。
def _persist_bm25_index(nodes, bm25_index_dir: Path, manifest: RAGManifest) -> None:
    from llama_index.retrievers.bm25 import BM25Retriever

    sparse_params = manifest.hybrid_index.params["sparse"]
    bm25_index_dir.mkdir(parents=True, exist_ok=True)
    retriever = BM25Retriever.from_defaults(
        nodes=list(nodes),
        similarity_top_k=sparse_params["similarity_top_k"],
        skip_stemming=sparse_params["skip_stemming"],
        tokenizer=_tokenize_for_bm25,
        language=sparse_params["language"],
    )
    retriever.persist(str(bm25_index_dir))


# 写 collection 状态；raw_docs 增量复用同一 collection，组件契约变化会落到新 collection。
def _write_collection_state(
    manifest: RAGManifest,
    collection_state_path: Path,
    release_manifest_path: Path,
    corpus_revision: str,
    actual_source_count: int,
    actual_chunk_count: int,
    processed_source_count: int,
    processed_chunk_count: int,
) -> None:
    write_yaml(collection_state_path, {
        "status": "collection_updated",
        "collection": manifest.collection,
        "collection_alias": manifest.collection_alias,
        "pipeline_signature": manifest.pipeline_signature,
        "corpus_name": manifest.index_manifest.corpus_name,
        "corpus_revision": corpus_revision,
        "actual_source_count": actual_source_count,
        "actual_chunk_count": actual_chunk_count,
        "processed_source_count": processed_source_count,
        "processed_chunk_count": processed_chunk_count,
        "release_manifest": str(release_manifest_path),
        "note": "raw_docs 增量会直接更新 alias 当前指向的同名 collection；parser/chunker/embedding 变化会生成新 collection，需通过 release.py 评测后切 alias。",
    })


# 为中文 BM25 提供稳定分词：中文按字，英文数字按词，避免默认英文 stemmer 误处理。
def _tokenize_for_bm25(text: str) -> List[str]:
    return [match.group(0).lower() for match in CHINESE_OR_WORD.finditer(text)]
